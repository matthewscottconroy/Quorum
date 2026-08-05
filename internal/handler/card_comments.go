package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
)

// cardCommentsRepo is satisfied by *repo.CardCommentsRepo.
type cardCommentsRepo interface {
	List(ctx context.Context, actionItemID string) ([]model.CardComment, error)
	Create(ctx context.Context, actionItemID, authorID, body string) (*model.CardComment, error)
	Get(ctx context.Context, id string) (*model.CardComment, error)
	Delete(ctx context.Context, id string) error
}

// CardCommentsHandler manages conversation threads on work-board cards.
// Any member may read and write (assignees are often plain members); a
// comment can be removed by its author or an admin.
type CardCommentsHandler struct {
	repo  cardCommentsRepo
	items actionItemsRepo
}

// NewCardCommentsHandler constructs a CardCommentsHandler.
func NewCardCommentsHandler(r cardCommentsRepo, items actionItemsRepo) *CardCommentsHandler {
	return &CardCommentsHandler{repo: r, items: items}
}

// requireItem 404s unless the card exists (comments hang off real cards only).
func (h *CardCommentsHandler) requireItem(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return "", false
	}
	if _, err := h.items.Get(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "work item not found", "not_found")
		return "", false
	}
	return id, true
}

// List returns a card's conversation, oldest first (member+).
func (h *CardCommentsHandler) List(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireItem(w, r)
	if !ok {
		return
	}
	comments, err := h.repo.List(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if comments == nil {
		comments = []model.CardComment{}
	}
	writeJSON(w, http.StatusOK, comments)
}

// Create appends a comment to the card's thread (member+).
func (h *CardCommentsHandler) Create(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireItem(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" || len(body.Body) > 4000 {
		writeError(w, http.StatusBadRequest, "comment must be 1-4000 characters", "bad_request")
		return
	}
	created, err := h.repo.Create(r.Context(), id, userIDFromCtx(r), body.Body)
	if err != nil {
		writeRepoError(w, err, "work item not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"chars": len(created.Body)})
	writeJSON(w, http.StatusCreated, created)
}

// Delete removes a comment: the author may delete their own words, an admin
// may moderate anyone's. Everyone else gets 403.
func (h *CardCommentsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	cid := chi.URLParam(r, "cid")
	if !isValidUUID(cid) {
		writeError(w, http.StatusBadRequest, "invalid comment id", "bad_request")
		return
	}
	c, err := h.repo.Get(r.Context(), cid)
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found", "not_found")
		return
	}
	isAuthor := c.AuthorID != nil && *c.AuthorID == userIDFromCtx(r)
	if !isAuthor && !roleAtLeast(roleFromCtx(r), "admin") {
		writeError(w, http.StatusForbidden, "only the author or an admin can delete a comment", "forbidden")
		return
	}
	if err := h.repo.Delete(r.Context(), cid); err != nil {
		writeRepoError(w, err, "comment not found", "delete error")
		return
	}
	setAuditDetail(r, map[string]any{"comment_id": cid, "moderated": !isAuthor})
	w.WriteHeader(http.StatusNoContent)
}
