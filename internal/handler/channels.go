package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
)

// channelsRepo is satisfied by *repo.ChannelsRepo.
type channelsRepo interface {
	List(ctx context.Context, viewerID string) ([]model.Channel, error)
	Get(ctx context.Context, id, viewerID string) (*model.Channel, error)
	Create(ctx context.Context, name string, topic *string, createdBy string) (*model.Channel, error)
	Update(ctx context.Context, id string, name, topic *string) error
	Delete(ctx context.Context, id string) error
	IsMember(ctx context.Context, channelID, userID string) (bool, error)
	AddMember(ctx context.Context, channelID, userID, addedBy string) error
	RemoveMember(ctx context.Context, channelID, userID string) error
	Messages(ctx context.Context, channelID, parentID string, limit int) ([]model.ChannelMessage, error)
	PostMessage(ctx context.Context, channelID, parentID, authorID, body, resourceID string) (*model.ChannelMessage, error)
	GetMessage(ctx context.Context, id string) (*model.ChannelMessage, error)
	DeleteMessage(ctx context.Context, id string) error
	ListChatUsers(ctx context.Context) ([]model.ChannelMember, error)
}

// ChannelsHandler serves the discussions area. Policy: any member+ creates
// channels and adds people; reading and posting require membership (admins
// may moderate any channel: read, delete messages, manage members); the
// creator or an admin renames/deletes a channel. Messages carry no files —
// they reference library documents, which keep their own visibility rules.
type ChannelsHandler struct {
	repo      channelsRepo
	resources resourcesRepo
}

// NewChannelsHandler constructs the handler.
func NewChannelsHandler(r channelsRepo, res resourcesRepo) *ChannelsHandler {
	return &ChannelsHandler{repo: r, resources: res}
}

// requireChannelAccess loads the channel and enforces read access:
// member-of-channel, or admin+ (moderation). A denied channel is 403 — its
// existence is not secret (the list shows all channels).
func (h *ChannelsHandler) requireChannelAccess(w http.ResponseWriter, r *http.Request) (*model.Channel, bool) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return nil, false
	}
	c, err := h.repo.Get(r.Context(), id, userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found", "not_found")
		return nil, false
	}
	if !c.IsMember && !roleAtLeast(roleFromCtx(r), "admin") {
		writeError(w, http.StatusForbidden, "join the channel to read it (ask any member to add you)", "forbidden")
		return nil, false
	}
	return c, true
}

// List returns all channels with the viewer's membership flags (member+).
func (h *ChannelsHandler) List(w http.ResponseWriter, r *http.Request) {
	channels, err := h.repo.List(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if channels == nil {
		channels = []model.Channel{}
	}
	writeJSON(w, http.StatusOK, channels)
}

// Get returns one channel with roster (members of the channel, or admin).
func (h *ChannelsHandler) Get(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// Create adds a channel (any member+); the creator joins automatically.
func (h *ChannelsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string  `json:"name"`
		Topic *string `json:"topic"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 80 {
		writeError(w, http.StatusBadRequest, "name is required (at most 80 characters)", "bad_request")
		return
	}
	created, err := h.repo.Create(r.Context(), body.Name, body.Topic, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"name": created.Name})
	writeJSON(w, http.StatusCreated, created)
}

// canManage: the channel's creator or an admin.
func canManage(r *http.Request, c *model.Channel) bool {
	return (c.CreatedBy != nil && *c.CreatedBy == userIDFromCtx(r)) || roleAtLeast(roleFromCtx(r), "admin")
}

// Update renames/retopics (creator or admin).
func (h *ChannelsHandler) Update(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	if !canManage(r, c) {
		writeError(w, http.StatusForbidden, "only the channel's creator or an admin can edit it", "forbidden")
		return
	}
	var body struct {
		Name  *string `json:"name"`
		Topic *string `json:"topic"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" || len(n) > 80 {
			writeError(w, http.StatusBadRequest, "name must be 1-80 characters", "bad_request")
			return
		}
		body.Name = &n
	}
	if err := h.repo.Update(r.Context(), c.ID, body.Name, body.Topic); err != nil {
		writeRepoError(w, err, "channel not found", "update error")
		return
	}
	updated, err := h.repo.Get(r.Context(), c.ID, userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// Delete removes a channel and its history (creator or admin, confirm-gated).
func (h *ChannelsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	if !canManage(r, c) {
		writeError(w, http.StatusForbidden, "only the channel's creator or an admin can delete it", "forbidden")
		return
	}
	if !confirmMatches(w, r, c.Name) {
		return
	}
	if err := h.repo.Delete(r.Context(), c.ID); err != nil {
		writeRepoError(w, err, "channel not found", "delete error")
		return
	}
	setAuditDetail(r, map[string]any{"name": c.Name, "messages": c.MessageCount})
	w.WriteHeader(http.StatusNoContent)
}

// AddMember: any channel member (or admin) adds a user.
func (h *ChannelsHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(r, &body); err != nil || !isValidUUID(body.UserID) {
		writeError(w, http.StatusBadRequest, "user_id (uuid) required", "bad_request")
		return
	}
	if err := h.repo.AddMember(r.Context(), c.ID, body.UserID, userIDFromCtx(r)); err != nil {
		writeRepoError(w, err, "user not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"channel": c.Name, "user_id": body.UserID})
	w.WriteHeader(http.StatusNoContent)
}

// RemoveMember: yourself (leave), or the creator/an admin removing someone.
func (h *ChannelsHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	uid := chi.URLParam(r, "uid")
	if !isValidUUID(uid) {
		writeError(w, http.StatusBadRequest, "invalid user id", "bad_request")
		return
	}
	if uid != userIDFromCtx(r) && !canManage(r, c) {
		writeError(w, http.StatusForbidden, "you can leave yourself; only the creator or an admin removes others", "forbidden")
		return
	}
	if err := h.repo.RemoveMember(r.Context(), c.ID, uid); err != nil {
		writeRepoError(w, err, "not a member", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"channel": c.Name, "user_id": uid, "left_self": uid == userIDFromCtx(r)})
	w.WriteHeader(http.StatusNoContent)
}

// Messages returns roots (?thread absent) or one thread's replies (members).
func (h *ChannelsHandler) Messages(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	parent := r.URL.Query().Get("thread")
	if parent != "" && !isValidUUID(parent) {
		writeError(w, http.StatusBadRequest, "thread must be a message id", "bad_request")
		return
	}
	msgs, err := h.repo.Messages(r.Context(), c.ID, parent, clampLimit(r.URL.Query().Get("limit"), 200))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if msgs == nil {
		msgs = []model.ChannelMessage{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

// Post appends a message; optional parent_id (thread) and resource_id (a
// library document the AUTHOR can see — validated here so nobody attaches
// documents blindly; viewers still resolve it under their own visibility).
func (h *ChannelsHandler) Post(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	if !c.IsMember {
		writeError(w, http.StatusForbidden, "join the channel to post", "forbidden")
		return
	}
	var body struct {
		Body       string `json:"body"`
		ParentID   string `json:"parent_id"`
		ResourceID string `json:"resource_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" || len(body.Body) > 4000 {
		writeError(w, http.StatusBadRequest, "message must be 1-4000 characters", "bad_request")
		return
	}
	if body.ParentID != "" && !isValidUUID(body.ParentID) {
		writeError(w, http.StatusBadRequest, "parent_id must be a message id", "bad_request")
		return
	}
	if body.ResourceID != "" {
		if !isValidUUID(body.ResourceID) {
			writeError(w, http.StatusBadRequest, "resource_id must be a UUID", "bad_request")
			return
		}
		if _, err := h.resources.GetVisible(r.Context(), body.ResourceID,
			roleAtLeast(roleFromCtx(r), "officer"), memberIDFromCtx(r), roleRank[roleFromCtx(r)]); err != nil {
			writeError(w, http.StatusNotFound, "linked document not found", "not_found")
			return
		}
	}
	msg, err := h.repo.PostMessage(r.Context(), c.ID, body.ParentID, userIDFromCtx(r), body.Body, body.ResourceID)
	if err != nil {
		writeRepoError(w, err, "message not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"channel": c.Name, "chars": len(msg.Body), "thread": msg.ParentID != nil})
	writeJSON(w, http.StatusCreated, msg)
}

// DeleteMessage: the author, or an admin moderating.
func (h *ChannelsHandler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireChannelAccess(w, r)
	if !ok {
		return
	}
	mid := chi.URLParam(r, "mid")
	if !isValidUUID(mid) {
		writeError(w, http.StatusBadRequest, "invalid message id", "bad_request")
		return
	}
	msg, err := h.repo.GetMessage(r.Context(), mid)
	if err != nil || msg.ChannelID != c.ID {
		writeError(w, http.StatusNotFound, "message not found", "not_found")
		return
	}
	isAuthor := msg.AuthorID != nil && *msg.AuthorID == userIDFromCtx(r)
	if !isAuthor && !roleAtLeast(roleFromCtx(r), "admin") {
		writeError(w, http.StatusForbidden, "only the author or an admin can delete a message", "forbidden")
		return
	}
	if err := h.repo.DeleteMessage(r.Context(), mid); err != nil {
		writeRepoError(w, err, "message not found", "delete error")
		return
	}
	setAuditDetail(r, map[string]any{"channel": c.Name, "moderated": !isAuthor, "replies": msg.ReplyCount})
	w.WriteHeader(http.StatusNoContent)
}

// Users lists addable accounts for the picker (member+).
func (h *ChannelsHandler) Users(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.ListChatUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if users == nil {
		users = []model.ChannelMember{}
	}
	writeJSON(w, http.StatusOK, users)
}
