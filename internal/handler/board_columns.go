package handler

import (
	"context"
	"net/http"

	"quorum/internal/model"
)

// boardColumnsRepo is satisfied by *repo.BoardColumnsRepo.
type boardColumnsRepo interface {
	List(ctx context.Context) ([]model.BoardColumn, error)
	Get(ctx context.Context, id string) (*model.BoardColumn, error)
	Create(ctx context.Context, name string, position int, mapsToStatus *string) (*model.BoardColumn, error)
	Update(ctx context.Context, id string, fields map[string]any) (*model.BoardColumn, error)
	Delete(ctx context.Context, id string) error
}

// BoardColumnsHandler manages the kanban board's columns. Cards keep `status`
// as the canonical reporting field; columns are workflow lanes (optionally
// mapped to a status) that officers shape to their process.
type BoardColumnsHandler struct {
	repo boardColumnsRepo
}

// NewBoardColumnsHandler constructs a BoardColumnsHandler.
func NewBoardColumnsHandler(r boardColumnsRepo) *BoardColumnsHandler {
	return &BoardColumnsHandler{repo: r}
}

// List returns all columns in board order (member+).
func (h *BoardColumnsHandler) List(w http.ResponseWriter, r *http.Request) {
	cols, err := h.repo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if cols == nil {
		cols = []model.BoardColumn{}
	}
	writeJSON(w, http.StatusOK, cols)
}

var validCardStatus = map[string]bool{"open": true, "in_progress": true, "done": true, "cancelled": true}

// Create adds a column (officer+). maps_to_status is optional: set, the
// column advances card status on drop; empty, it is a pure workflow lane.
func (h *BoardColumnsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string  `json:"name"`
		Position     int     `json:"position"`
		MapsToStatus *string `json:"maps_to_status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Name == "" || len(body.Name) > 40 {
		writeError(w, http.StatusBadRequest, "name is required (at most 40 characters)", "bad_request")
		return
	}
	if body.MapsToStatus != nil && *body.MapsToStatus == "" {
		body.MapsToStatus = nil
	}
	if body.MapsToStatus != nil && !validCardStatus[*body.MapsToStatus] {
		writeError(w, http.StatusBadRequest, "maps_to_status must be a card status", "bad_request")
		return
	}
	created, err := h.repo.Create(r.Context(), body.Name, body.Position, body.MapsToStatus)
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"name": created.Name})
	writeJSON(w, http.StatusCreated, created)
}

// Update renames or repositions a column (officer+).
func (h *BoardColumnsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	fields := filterAllowedFields(body, map[string]bool{"name": true, "position": true})
	if len(fields) == 0 {
		writeError(w, http.StatusBadRequest, "no valid fields provided", "bad_request")
		return
	}
	if name, present := fields["name"]; present {
		s, ok := name.(string)
		if !ok || s == "" || len(s) > 40 {
			writeError(w, http.StatusBadRequest, "name must be 1-40 characters", "bad_request")
			return
		}
	}
	updated, err := h.repo.Update(r.Context(), id, fields)
	if err != nil {
		writeRepoError(w, err, "column not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"name": updated.Name})
	writeJSON(w, http.StatusOK, updated)
}

// Delete removes a column (officer+, type-to-confirm). Cards in it are not
// lost: their column link nulls out and they fall back to the column that
// maps their status.
func (h *BoardColumnsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	crudDelete(w, r, deleteSpec[model.BoardColumn]{
		entity:      "board column (cards fall back to their status lane)",
		notFoundMsg: "column not found",
		get:         h.repo.Get,
		name:        func(c *model.BoardColumn) string { return c.Name },
		del:         h.repo.Delete,
	})
}
