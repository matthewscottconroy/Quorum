package handler

import (
	"context"
	"net/http"
	"strings"

	"quorum/internal/model"
)

// groupsRepo is satisfied by *repo.GroupsRepo.
type groupsRepo interface {
	List(ctx context.Context) ([]model.Group, error)
	Get(ctx context.Context, id string) (*model.Group, error)
	Create(ctx context.Context, name string, description *string, createdBy string) (*model.Group, error)
	Update(ctx context.Context, id string, name, description *string) (*model.Group, error)
	Delete(ctx context.Context, id string) error
	SetMembers(ctx context.Context, groupID string, memberIDs []string) error
	SetResourceGroups(ctx context.Context, resourceID string, groupIDs []string) error
	ResourceGroupIDs(ctx context.Context, resourceID string) ([]string, error)
}

// GroupsHandler manages visibility groups: named sets of members that
// constrain who can see a resource. Group management is admin work (it defines
// who sees what); attaching groups to a resource is part of curating the
// library, so that is officer work.
type GroupsHandler struct {
	repo groupsRepo
}

// NewGroupsHandler constructs a GroupsHandler.
func NewGroupsHandler(r groupsRepo) *GroupsHandler {
	return &GroupsHandler{repo: r}
}

// List returns all groups (member+: names are needed to render badges).
func (h *GroupsHandler) List(w http.ResponseWriter, r *http.Request) {
	groups, err := h.repo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if groups == nil {
		groups = []model.Group{}
	}
	writeJSON(w, http.StatusOK, groups)
}

// Get returns one group with its member ids (admin+).
func (h *GroupsHandler) Get(w http.ResponseWriter, r *http.Request) {
	genericGet(w, r, h.repo.Get, "group not found")
}

// MemberIDs returns just the group's member IDs (officer+). The full group
// object stays admin-only; rosters only need the IDs for bulk selection.
func (h *GroupsHandler) MemberIDs(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	g, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "group not found", "query error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member_ids": g.MemberIDs})
}

// Create adds a group (admin+).
func (h *GroupsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required", "bad_request")
		return
	}
	g, err := h.repo.Create(r.Context(), body.Name, body.Description, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"group": g.Name})
	writeJSON(w, http.StatusCreated, g)
}

// Update patches a group's name/description (admin+).
func (h *GroupsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name must not be blank", "bad_request")
		return
	}
	g, err := h.repo.Update(r.Context(), id, body.Name, body.Description)
	if err != nil {
		writeRepoError(w, err, "group not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// Delete removes a group (admin+, confirm-gated: deleting a group WIDENS
// visibility — resources restricted only by it become visible to all members).
func (h *GroupsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	g, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "group not found", "query error")
		return
	}
	if !confirmMatches(w, r, g.Name) {
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "group not found", "delete error")
		return
	}
	setAuditDetail(r, map[string]any{"group": g.Name})
	w.WriteHeader(http.StatusNoContent)
}

// SetMembers replaces a group's membership (admin+). Recorded in the
// chain-protected audit detail: membership changes ARE visibility changes.
func (h *GroupsHandler) SetMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		MemberIDs []string `json:"member_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	for _, m := range body.MemberIDs {
		if !isValidUUID(m) {
			writeError(w, http.StatusBadRequest, "member_ids must be UUIDs", "bad_request")
			return
		}
	}
	if err := h.repo.SetMembers(r.Context(), id, body.MemberIDs); err != nil {
		writeRepoError(w, err, "group not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"members": len(body.MemberIDs)})
	g, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// ResourceGroups returns the group ids restricting a resource (officer+).
func (h *GroupsHandler) ResourceGroups(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	ids, err := h.repo.ResourceGroupIDs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"group_ids": ids})
}

// SetResourceGroups replaces the groups restricting a resource (officer+).
// An empty list makes the resource visible to all members again.
func (h *GroupsHandler) SetResourceGroups(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		GroupIDs []string `json:"group_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	for _, g := range body.GroupIDs {
		if !isValidUUID(g) {
			writeError(w, http.StatusBadRequest, "group_ids must be UUIDs", "bad_request")
			return
		}
	}
	if err := h.repo.SetResourceGroups(r.Context(), id, body.GroupIDs); err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "resource or group does not exist", "bad_request")
			return
		}
		writeRepoError(w, err, "resource not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"groups": len(body.GroupIDs)})
	w.WriteHeader(http.StatusNoContent)
}
