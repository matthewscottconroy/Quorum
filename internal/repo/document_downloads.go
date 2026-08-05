package repo

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// DocumentDownloadsRepo is the forensic ledger of document downloads.
type DocumentDownloadsRepo struct {
	db *pgxpool.Pool
}

// NewDocumentDownloadsRepo constructs the repo.
func NewDocumentDownloadsRepo(db *pgxpool.Pool) *DocumentDownloadsRepo {
	return &DocumentDownloadsRepo{db: db}
}

// NewDownloadID mints a v4 UUID. Generated app-side because the watermark
// stamped into the served file includes the record id, so the id must exist
// before the row's hash can be computed.
func NewDownloadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Insert records one download event.
func (r *DocumentDownloadsRepo) Insert(ctx context.Context, rec *model.DownloadRecord) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO document_downloads (id, resource_id, user_id, file_name, sha256, ip)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
		RETURNING downloaded_at`,
		rec.ID, rec.ResourceID, rec.UserID, rec.FileName, rec.SHA256, rec.IP).
		Scan(&rec.DownloadedAt)
}

// ListByResource returns a resource's download history, newest first.
func (r *DocumentDownloadsRepo) ListByResource(ctx context.Context, resourceID string, limit int) ([]model.DownloadRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.Query(ctx, `
		SELECT id::text, resource_id::text, user_id::text, file_name, sha256, ip, downloaded_at
		FROM document_downloads WHERE resource_id = $1::uuid
		ORDER BY downloaded_at DESC LIMIT $2`, resourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DownloadRecord
	for rows.Next() {
		var d model.DownloadRecord
		if err := rows.Scan(&d.ID, &d.ResourceID, &d.UserID, &d.FileName, &d.SHA256, &d.IP, &d.DownloadedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FindBySHA returns the download event that served bytes with this hash, or
// pgx.ErrNoRows. This is the provenance lookup: a file found in the wild
// either matches a stored original, matches exactly one download event, or
// has been altered since it left the system.
func (r *DocumentDownloadsRepo) FindBySHA(ctx context.Context, sha string) (*model.DownloadRecord, error) {
	var d model.DownloadRecord
	err := r.db.QueryRow(ctx, `
		SELECT id::text, resource_id::text, user_id::text, file_name, sha256, ip, downloaded_at
		FROM document_downloads WHERE sha256 = $1
		ORDER BY downloaded_at LIMIT 1`, sha).
		Scan(&d.ID, &d.ResourceID, &d.UserID, &d.FileName, &d.SHA256, &d.IP, &d.DownloadedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// FindOriginalBySHA reports whether any stored document's canonical hash
// matches, returning its resource id and title.
func (r *DocumentDownloadsRepo) FindOriginalBySHA(ctx context.Context, sha string) (id, title string, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT id::text, title FROM resources WHERE file_sha256 = $1 LIMIT 1`, sha).Scan(&id, &title)
	if err == pgx.ErrNoRows {
		return "", "", pgx.ErrNoRows
	}
	return id, title, err
}
