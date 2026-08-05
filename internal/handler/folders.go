package handler

import (
	"context"
	"net/http"
	"strings"

	"quorum/internal/model"
)

// foldersRepo is satisfied by *repo.FoldersRepo.
type foldersRepo interface {
	List(ctx context.Context) ([]model.Folder, error)
	Get(ctx context.Context, id string) (*model.Folder, error)
	Create(ctx context.Context, name string) (*model.Folder, error)
	Rename(ctx context.Context, id, name string) (*model.Folder, error)
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
		Name string `json:"name"`
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
	created, err := h.repo.Create(r.Context(), body.Name)
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"name": created.Name})
	writeJSON(w, http.StatusCreated, created)
}

// Update renames a folder (officer+).
func (h *FoldersHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
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
	updated, err := h.repo.Rename(r.Context(), id, body.Name)
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
