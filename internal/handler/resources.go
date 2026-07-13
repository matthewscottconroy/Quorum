package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ResourcesHandler handles resource library endpoints.
type ResourcesHandler struct {
	repo resourcesRepo
}

// NewResourcesHandler constructs a ResourcesHandler.
func NewResourcesHandler(r resourcesRepo) *ResourcesHandler {
	return &ResourcesHandler{repo: r}
}

func (h *ResourcesHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.ResourceFilter{
		Search:   q.Get("search"),
		Category: q.Get("category"),
		Tag:      q.Get("tag"),
		Limit:    100,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		f.Limit = v
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
	id := chi.URLParam(r, "id")
	res, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, 404, "resource not found", "not_found")
		return
	}
	writeJSON(w, 200, res)
}

func (h *ResourcesHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
	updated, err := h.repo.Update(r.Context(), id, &res)
	if err != nil {
		writeError(w, 500, "update error", "internal_error")
		return
	}
	writeJSON(w, 200, updated)
}

func (h *ResourcesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeError(w, 500, "delete error", "internal_error")
		return
	}
	w.WriteHeader(204)
}
