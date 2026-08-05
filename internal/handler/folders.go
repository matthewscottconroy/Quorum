package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// foldersRepo is satisfied by *repo.FoldersRepo.
type foldersRepo interface {
	List(ctx context.Context) ([]model.Folder, error)
	Get(ctx context.Context, id string) (*model.Folder, error)
	Create(ctx context.Context, name string, parentID *string) (*model.Folder, error)
	Rename(ctx context.Context, id string, name *string, parentID *string, moveParent bool) (*model.Folder, error)
	Delete(ctx context.Context, id string) error
}

// FoldersHandler manages resource-library folders. Folders organize; they do
// not protect — visibility stays with roles and visibility groups on the
// resources themselves.
type FoldersHandler struct {
	repo foldersRepo
}

// NewFoldersHandler constructs a FoldersHandler.
func NewFoldersHandler(r foldersRepo) *FoldersHandler {
	return &FoldersHandler{repo: r}
}

// List returns all folders (member+).
func (h *FoldersHandler) List(w http.ResponseWriter, r *http.Request) {
	folders, err := h.repo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if folders == nil {
		folders = []model.Folder{}
	}
	writeJSON(w, http.StatusOK, folders)
}

func validFolderName(s string) bool { return s != "" && len(s) <= 80 }

// Create adds a folder (officer+).
func (h *FoldersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !validFolderName(body.Name) {
		writeError(w, http.StatusBadRequest, "name is required (at most 80 characters)", "bad_request")
		return
	}
	if body.ParentID != nil && !isValidUUID(*body.ParentID) {
		writeError(w, http.StatusBadRequest, "parent_id must be a UUID", "bad_request")
		return
	}
	created, err := h.repo.Create(r.Context(), body.Name, body.ParentID)
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"name": created.Name})
	writeJSON(w, http.StatusCreated, created)
}

// Update renames and/or moves a folder (officer+). parent_id: absent =
// unchanged, null = move to root, uuid = move inside that folder. Moving a
// folder into its own subtree is refused (409).
func (h *FoldersHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	var namePtr *string
	if nameRaw, present := raw["name"]; present {
		var name string
		if err := json.Unmarshal(nameRaw, &name); err != nil {
			writeError(w, http.StatusBadRequest, "name must be a string", "bad_request")
			return
		}
		name = strings.TrimSpace(name)
		if !validFolderName(name) {
			writeError(w, http.StatusBadRequest, "name is required (at most 80 characters)", "bad_request")
			return
		}
		namePtr = &name
	}
	var parentPtr *string
	moveParent := false
	if parentRaw, present := raw["parent_id"]; present {
		moveParent = true
		if err := json.Unmarshal(parentRaw, &parentPtr); err != nil {
			writeError(w, http.StatusBadRequest, "parent_id must be a UUID or null", "bad_request")
			return
		}
		if parentPtr != nil && !isValidUUID(*parentPtr) {
			writeError(w, http.StatusBadRequest, "parent_id must be a UUID or null", "bad_request")
			return
		}
	}
	if namePtr == nil && !moveParent {
		writeError(w, http.StatusBadRequest, "nothing to update", "bad_request")
		return
	}
	updated, err := h.repo.Rename(r.Context(), id, namePtr, parentPtr, moveParent)
	if errors.Is(err, repo.ErrFolderCycle) {
		writeError(w, http.StatusConflict, "a folder cannot be moved inside itself", "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "folder not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"name": updated.Name})
	writeJSON(w, http.StatusOK, updated)
}

// Delete removes a folder (officer+, type-to-confirm). Its resources return
// to the library root; their visibility is unchanged.
func (h *FoldersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	crudDelete(w, r, deleteSpec[model.Folder]{
		entity:      "folder (its documents return to the library root)",
		notFoundMsg: "folder not found",
		get:         h.repo.Get,
		name:        func(f *model.Folder) string { return f.Name },
		del:         h.repo.Delete,
	})
}
