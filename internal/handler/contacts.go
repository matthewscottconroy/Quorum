package handler

import (
	"net/http"
	"strconv"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ContactsHandler handles contact directory endpoints.
type ContactsHandler struct {
	repo     contactsRepo
	notifier deletionNotifier
}

// NewContactsHandler constructs a ContactsHandler.
func NewContactsHandler(r contactsRepo) *ContactsHandler {
	return &ContactsHandler{repo: r}
}

// SetNotifier attaches an optional notifier used on gated deletes.
func (h *ContactsHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

// List handles GET requests for a paginated, filterable list of contacts.
func (h *ContactsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.ContactFilter{
		Search:   q.Get("search"),
		Category: q.Get("category"),
		Tag:      q.Get("tag"),
		Limit:    clampLimit(q.Get("limit"), 100),
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	contacts, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, contacts, total, f.Limit, f.Offset)
}

// Create handles creating a contact.
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
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, created)
}

// Get handles fetching a single contact by id.
func (h *ContactsHandler) Get(w http.ResponseWriter, r *http.Request) {
	genericGet(w, r, h.repo.Get, "contact not found")
}

// Update applies a partial update: only fields present in the request body
// change, so PATCH semantics hold (omitted fields are never nulled).
func (h *ContactsHandler) Update(w http.ResponseWriter, r *http.Request) {
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
		"name": true, "organization": true, "email": true, "phone": true,
		"address": true, "category": true, "tags": true, "notes": true,
	}
	fields := filterAllowedFields(body, allowed)
	if len(fields) == 0 {
		writeError(w, 400, "no valid fields provided", "bad_request")
		return
	}
	if name, present := fields["name"]; present {
		s, ok := name.(string)
		if !ok || s == "" {
			writeError(w, 400, "name must be a non-empty string", "bad_request")
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
		writeRepoError(w, err, "contact not found", "update error")
		return
	}
	writeJSON(w, 200, updated)
}

// Delete handles deleting a contact.
func (h *ContactsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	crudDelete(w, r, deleteSpec[model.Contact]{
		entity:      "contact",
		notFoundMsg: "contact not found",
		get:         h.repo.Get,
		name:        func(c *model.Contact) string { return c.Name },
		del:         h.repo.Delete,
		notifier:    h.notifier,
	})
}
