package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ContactsHandler handles contact directory endpoints.
type ContactsHandler struct {
	repo contactsRepo
}

// NewContactsHandler constructs a ContactsHandler.
func NewContactsHandler(r contactsRepo) *ContactsHandler {
	return &ContactsHandler{repo: r}
}

func (h *ContactsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.ContactFilter{
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
	contacts, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if contacts == nil {
		contacts = []model.Contact{}
	}
	writeJSON(w, 200, model.Page[model.Contact]{Data: contacts, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (h *ContactsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var c model.Contact
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if c.Name == "" {
		writeError(w, 400, "name required", "bad_request")
		return
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	created, err := h.repo.Create(r.Context(), &c, userIDFromCtx(r))
	if err != nil {
		writeError(w, 500, "create error", "internal_error")
		return
	}
	writeJSON(w, 201, created)
}

func (h *ContactsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, 404, "contact not found", "not_found")
		return
	}
	writeJSON(w, 200, c)
}

func (h *ContactsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var c model.Contact
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if c.Name == "" {
		writeError(w, 400, "name required", "bad_request")
		return
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	updated, err := h.repo.Update(r.Context(), id, &c)
	if err != nil {
		writeError(w, 500, "update error", "internal_error")
		return
	}
	writeJSON(w, 200, updated)
}

func (h *ContactsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeError(w, 500, "delete error", "internal_error")
		return
	}
	w.WriteHeader(204)
}
