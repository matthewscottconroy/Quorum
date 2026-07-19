package handler

import (
	"net/http"
	"strconv"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ResourcesHandler handles resource library endpoints.
type ResourcesHandler struct {
	repo     resourcesRepo
	notifier deletionNotifier
}

// NewResourcesHandler constructs a ResourcesHandler.
func NewResourcesHandler(r resourcesRepo) *ResourcesHandler {
	return &ResourcesHandler{repo: r}
}

// SetNotifier attaches an optional notifier used on gated deletes.
func (h *ResourcesHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

func (h *ResourcesHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.ResourceFilter{
		Search:   q.Get("search"),
		Category: q.Get("category"),
		Tag:      q.Get("tag"),
		Limit:    clampLimit(q.Get("limit"), 100),
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	resources, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if resources == nil {
		resources = []model.Resource{}
	}
	writeJSON(w, 200, model.Page[model.Resource]{Data: resources, Total: total, Limit: f.Limit, Offset: f.Offset})
}

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
		writeError(w, 500, "create error", "internal_error")
		return
	}
	writeJSON(w, 201, created)
}

func (h *ResourcesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	res, err := h.repo.Get(r.Context(), id)
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
	}
	fields := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			fields[k] = v
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

func (h *ResourcesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	res, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "resource not found", "query error")
		return
	}
	if !confirmMatches(w, r, res.Title) {
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "resource not found", "delete error")
		return
	}
	if h.notifier != nil {
		h.notifier.NotifyDeletion(r.Context(), userIDFromCtx(r), "resource", res.Title, nil)
	}
	w.WriteHeader(204)
}
