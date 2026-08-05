package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ResourcesHandler handles resource library endpoints.
type ResourcesHandler struct {
	repo     resourcesRepo
	notifier deletionNotifier
	audit    auditRepo
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
		"folder_id": true,
	}
	fields := filterAllowedFields(body, allowed)
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

// DownloadFile streams a resource's document (member+). Visibility is the
// same as for the resource itself: outside your groups, the document does
// not exist. Every download is recorded as an EXPORT in the audit log with
// the file's SHA-256 — same accountability as PDF reports.
func (h *ResourcesHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	res, err := h.repo.GetVisible(r.Context(), id, roleAtLeast(roleFromCtx(r), "officer"), memberIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found", "not_found")
		return
	}
	if res.FileName == nil {
		writeError(w, http.StatusNotFound, "resource has no document attached", "not_found")
		return
	}
	ct, data, err := h.repo.GetFile(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "resource has no document attached", "not_found")
			return
		}
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}

	if h.audit != nil {
		detail := map[string]any{"file_name": *res.FileName, "bytes": len(data)}
		if res.FileSHA256 != nil {
			detail["sha256"] = *res.FileSHA256
		}
		auditExport(r, h.audit, "resource:"+res.Title, detail)
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": *res.FileName}))
	if res.FileSHA256 != nil {
		w.Header().Set("X-Document-SHA256", *res.FileSHA256)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}
