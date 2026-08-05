package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// downloadsLedger is satisfied by *repo.DocumentDownloadsRepo.
type downloadsLedger interface {
	Insert(ctx context.Context, rec *model.DownloadRecord) error
	ListByResource(ctx context.Context, resourceID string, limit int) ([]model.DownloadRecord, error)
	FindBySHA(ctx context.Context, sha string) (*model.DownloadRecord, error)
	FindOriginalBySHA(ctx context.Context, sha string) (id, title string, err error)
}

// ResourcesHandler handles resource library endpoints.
type ResourcesHandler struct {
	repo      resourcesRepo
	notifier  deletionNotifier
	audit     auditRepo
	users     exporterLookup
	downloads downloadsLedger
	clientIP  func(*http.Request) string
}

// NewResourcesHandler constructs a ResourcesHandler.
func NewResourcesHandler(r resourcesRepo) *ResourcesHandler {
	return &ResourcesHandler{repo: r}
}

// SetNotifier attaches an optional notifier used on gated deletes.
func (h *ResourcesHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

// SetAudit attaches the audit repo used to record document downloads
// (EXPORT entries; GETs bypass the audit middleware).
func (h *ResourcesHandler) SetAudit(a auditRepo) { h.audit = a }

// SetDownloadDeps wires the download ledger: the user lookup (for the
// watermark's name), the ledger repo, and the proxy-aware client-IP resolver.
func (h *ResourcesHandler) SetDownloadDeps(u exporterLookup, d downloadsLedger, ip func(*http.Request) string) {
	h.users = u
	h.downloads = d
	h.clientIP = ip
}

func (h *ResourcesHandler) requestIP(r *http.Request) string {
	if h.clientIP != nil {
		return h.clientIP(r)
	}
	return r.RemoteAddr
}

func (h *ResourcesHandler) requesterEmail(r *http.Request) string {
	if h.users == nil {
		return ""
	}
	if u, err := h.users.GetUserByID(r.Context(), userIDFromCtx(r)); err == nil {
		return u.Email
	}
	return ""
}

// stampDownload watermarks text-family formats with a provenance trailer:
// who downloaded, when, from where, and the ledger record id. Formats that
// cannot carry a comment (binaries) are served unmodified — the ledger row
// still fingerprints the event. Returns the possibly-stamped bytes and
// whether a stamp was applied.
func stampDownload(data []byte, fileName, email, ip, recordID string, at time.Time) ([]byte, bool) {
	line := fmt.Sprintf("Quorum provenance: downloaded by %s at %s from %s; record %s",
		email, at.UTC().Format(time.RFC3339), ip, recordID)
	switch strings.ToLower(path.Ext(fileName)) {
	case ".txt", ".log", ".text":
		return append(data, []byte("\n\n-- "+line+"\n")...), true
	case ".md", ".markdown", ".html", ".htm", ".svg", ".xml":
		return append(data, []byte("\n<!-- "+line+" -->\n")...), true
	case ".mmd", ".mermaid":
		return append(data, []byte("\n%% "+line+"\n")...), true
	}
	return data, false
}

// List handles GET requests for a paginated, filterable list of resources.
func (h *ResourcesHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.ResourceFilter{
		Search:   q.Get("search"),
		Category: q.Get("category"),
		Tag:      q.Get("tag"),
		FolderID: q.Get("folder"),
		Limit:    clampLimit(q.Get("limit"), 100),
		// Visibility scope: officers and above curate the whole library;
		// everyone else sees unrestricted resources plus those shared with a
		// group their member record belongs to.
		ViewerSeesAll:  roleAtLeast(roleFromCtx(r), "officer"),
		ViewerMemberID: memberIDFromCtx(r),
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	resources, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, resources, total, f.Limit, f.Offset)
}

// Create handles creating a resource.
func (h *ResourcesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var res model.Resource
	if err := decodeJSON(r, &res); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if res.Title == "" {
		writeError(w, 400, "title required", "bad_request")
		return
	}
	if res.Tags == nil {
		res.Tags = []string{}
	}
	created, err := h.repo.Create(r.Context(), &res, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, created)
}

// Get handles fetching a single resource by id.
func (h *ResourcesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	// Viewer-scoped read: a restricted resource outside the viewer's groups is
	// indistinguishable from a missing one.
	res, err := h.repo.GetVisible(r.Context(), id, roleAtLeast(roleFromCtx(r), "officer"), memberIDFromCtx(r))
	if err != nil {
		writeError(w, 404, "resource not found", "not_found")
		return
	}
	writeJSON(w, 200, res)
}

// Update applies a partial update: only fields present in the request body
// change, so PATCH semantics hold (omitted fields are never nulled).
func (h *ResourcesHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	allowed := map[string]bool{
		"title": true, "description": true, "url": true, "category": true, "tags": true,
		"folder_id": true, "file_preview_only": true,
	}
	fields := filterAllowedFields(body, allowed)
	if pv, present := fields["file_preview_only"]; present {
		if _, ok := pv.(bool); !ok {
			writeError(w, 400, "file_preview_only must be a boolean", "bad_request")
			return
		}
	}
	if len(fields) == 0 {
		writeError(w, 400, "no valid fields provided", "bad_request")
		return
	}
	if title, present := fields["title"]; present {
		s, ok := title.(string)
		if !ok || s == "" {
			writeError(w, 400, "title must be a non-empty string", "bad_request")
			return
		}
	}
	if tags, present := fields["tags"]; present {
		converted, ok := toStringSlice(tags)
		if !ok {
			writeError(w, 400, "tags must be an array of strings", "bad_request")
			return
		}
		fields["tags"] = converted
	}
	updated, err := h.repo.Update(r.Context(), id, fields)
	if err != nil {
		writeRepoError(w, err, "resource not found", "update error")
		return
	}
	writeJSON(w, 200, updated)
}

// Delete handles deleting a resource.
func (h *ResourcesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	crudDelete(w, r, deleteSpec[model.Resource]{
		entity:      "resource",
		notFoundMsg: "resource not found",
		get:         h.repo.Get,
		name:        func(res *model.Resource) string { return res.Title },
		del:         h.repo.Delete,
		notifier:    h.notifier,
	})
}

// maxUploadBytes caps document uploads (the request-body middleware raises
// its limit to this on the upload route).
const maxUploadBytes = 25 << 20

// UploadFile attaches a document to a resource (officer+): multipart field
// "file". Bytes land in the database, so backups and DR cover documents with
// no extra machinery; group visibility applies to the download exactly as it
// does to the resource.
func (h *ResourcesHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	res, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found", "not_found")
		return
	}
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "upload too large or malformed (25 MiB max)", "bad_request")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "multipart field 'file' is required", "bad_request")
		return
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read upload", "bad_request")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "file is empty", "bad_request")
		return
	}

	name := filepath.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
	name = strings.Map(func(c rune) rune {
		if c < 32 || c == 127 {
			return -1
		}
		return c
	}, name)
	if name == "" || name == "." || len(name) > 255 {
		writeError(w, http.StatusBadRequest, "invalid file name", "bad_request")
		return
	}
	ct := hdr.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = http.DetectContentType(data)
	}

	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if err := h.repo.SetFile(r.Context(), id, name, int64(len(data)), digest, ct, data); err != nil {
		writeRepoError(w, err, "resource not found", "upload error")
		return
	}
	setAuditDetail(r, map[string]any{
		"resource": res.Title, "file_name": name, "bytes": len(data), "sha256": digest,
	})
	updated, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// fetchVisibleFile authorizes and loads a document: same visibility as the
// resource itself — outside your groups, the document does not exist.
func (h *ResourcesHandler) fetchVisibleFile(w http.ResponseWriter, r *http.Request) (*model.Resource, string, []byte, bool) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return nil, "", nil, false
	}
	res, err := h.repo.GetVisible(r.Context(), id, roleAtLeast(roleFromCtx(r), "officer"), memberIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found", "not_found")
		return nil, "", nil, false
	}
	if res.FileName == nil {
		writeError(w, http.StatusNotFound, "resource has no document attached", "not_found")
		return nil, "", nil, false
	}
	ct, data, err := h.repo.GetFile(r.Context(), res.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "resource has no document attached", "not_found")
		} else {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		}
		return nil, "", nil, false
	}
	return res, ct, data, true
}

// DownloadFile streams a resource's document (member+), refusing
// preview-only documents. Every download: (1) lands in the permanent
// document_downloads ledger — who, when, from which IP, and the SHA-256 of
// the exact bytes served; (2) is stamped with a provenance trailer when the
// format can carry one, so the served hash maps back to this one event; and
// (3) is recorded as an EXPORT in the audit log.
func (h *ResourcesHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	res, ct, data, ok := h.fetchVisibleFile(w, r)
	if !ok {
		return
	}
	if res.FilePreviewOnly {
		writeError(w, http.StatusForbidden,
			"this document is preview-only; view it in the app", "preview_only")
		return
	}

	served := data
	stamped := false
	var recordID string
	if h.downloads != nil {
		var err error
		recordID, err = repo.NewDownloadID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "id error", "internal_error")
			return
		}
		now := time.Now()
		served, stamped = stampDownload(data, *res.FileName, h.requesterEmail(r), h.requestIP(r), recordID, now)
		sum := sha256.Sum256(served)
		uid := userIDFromCtx(r)
		rid := res.ID
		rec := &model.DownloadRecord{
			ID: recordID, ResourceID: &rid, UserID: &uid,
			FileName: *res.FileName, SHA256: hex.EncodeToString(sum[:]), IP: h.requestIP(r),
		}
		// The ledger row is written BEFORE bytes leave: a download that
		// cannot be recorded does not happen.
		if err := h.downloads.Insert(r.Context(), rec); err != nil {
			writeError(w, http.StatusInternalServerError, "ledger error", "internal_error")
			return
		}
		if h.audit != nil {
			auditExport(r, h.audit, "resource:"+res.Title, map[string]any{
				"file_name": *res.FileName, "bytes": len(served), "sha256": rec.SHA256,
				"download_id": recordID, "ip": rec.IP, "stamped": stamped,
			})
		}
	} else if h.audit != nil {
		auditExport(r, h.audit, "resource:"+res.Title, map[string]any{"file_name": *res.FileName})
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": *res.FileName}))
	if recordID != "" {
		w.Header().Set("X-Download-Record", recordID)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(served)))
	w.WriteHeader(http.StatusOK)
	w.Write(served) //nolint:errcheck
}

// Preview streams the ORIGINAL bytes for in-app rendering (member+, same
// visibility as the resource; allowed for preview-only documents — that is
// their purpose). Recorded in the audit log as a PREVIEW, not an EXPORT.
func (h *ResourcesHandler) Preview(w http.ResponseWriter, r *http.Request) {
	res, ct, data, ok := h.fetchVisibleFile(w, r)
	if !ok {
		return
	}
	if h.audit != nil {
		h.audit.Log(r.Context(), userIDFromCtx(r), "PREVIEW resource:"+res.Title, "resource", res.ID, //nolint:errcheck
			map[string]any{"file_name": *res.FileName, "ip": h.requestIP(r)})
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("inline", map[string]string{"filename": *res.FileName}))
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// ListDownloads returns a document's download history (officer+).
func (h *ResourcesHandler) ListDownloads(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if h.downloads == nil {
		writeJSON(w, http.StatusOK, []model.DownloadRecord{})
		return
	}
	recs, err := h.downloads.ListByResource(r.Context(), id, clampLimit(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if recs == nil {
		recs = []model.DownloadRecord{}
	}
	writeJSON(w, http.StatusOK, recs)
}

// VerifyDownload answers the provenance question for a file hash (admin):
// sha256=<hex> either matches a stored original, matches exactly the bytes
// served by one recorded download (naming who/when/where), or is unknown —
// meaning the file was altered after it left Quorum, or never came from it.
func (h *ResourcesHandler) VerifyDownload(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sha256")))
	if len(sha) != 64 {
		writeError(w, http.StatusBadRequest, "sha256 query parameter (64 hex chars) required", "bad_request")
		return
	}
	if h.downloads == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger unavailable", "unavailable")
		return
	}
	if id, title, err := h.downloads.FindOriginalBySHA(r.Context(), sha); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "original", "resource_id": id, "title": title,
			"meaning": "hash matches a stored document exactly as uploaded",
		})
		return
	}
	if rec, err := h.downloads.FindBySHA(r.Context(), sha); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "download", "record": rec,
			"meaning": "hash matches the exact bytes served by this recorded download",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "unknown",
		"meaning": "no stored document or recorded download produced these bytes: the file was altered after it left Quorum, or did not come from it",
	})
}
