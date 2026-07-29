package handler

import (
	"context"
	"net/http"
	"strconv"

	"quorum/internal/model"
)

// eventNotifier is the fire-and-forget notification emitter the domain handlers
// call. *service.NotificationService satisfies it. Methods are non-blocking.
type eventNotifier interface {
	NotifyMembers(notifType, title string, body, link *string)
	NotifyMember(memberID, notifType, title string, body, link *string)
}

// notifyRepo is the data access the notifications API needs.
type notifyRepo interface {
	ListForUser(ctx context.Context, userID string, unreadOnly bool, limit int) ([]model.Notification, error)
	UnreadCount(ctx context.Context, userID string) (int, error)
	MarkRead(ctx context.Context, userID, id string) error
	MarkAllRead(ctx context.Context, userID string) error
	GetPreferences(ctx context.Context, userID string) (*model.NotificationPreferences, error)
	UpdatePreferences(ctx context.Context, userID string, p model.NotificationPreferences) error
}

// NotificationsHandler serves a user's own in-app notifications and preferences.
type NotificationsHandler struct {
	repo notifyRepo
}

// NewNotificationsHandler constructs a NotificationsHandler.
func NewNotificationsHandler(r notifyRepo) *NotificationsHandler {
	return &NotificationsHandler{repo: r}
}

const notificationsPageLimit = 50

// List returns the caller's notifications (newest first), optionally unread-only.
func (h *NotificationsHandler) List(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	unread := r.URL.Query().Get("unread") == "true"
	limit := notificationsPageLimit
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= notificationsPageLimit {
		limit = v
	}
	items, err := h.repo.ListForUser(r.Context(), uid, unread, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if items == nil {
		items = []model.Notification{}
	}
	writeJSON(w, http.StatusOK, items)
}

// UnreadCount returns the caller's unread notification count (for the nav badge).
func (h *NotificationsHandler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.repo.UnreadCount(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": n})
}

// MarkRead marks one of the caller's notifications read.
func (h *NotificationsHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.MarkRead(r.Context(), userIDFromCtx(r), id); err != nil {
		writeRepoError(w, err, "notification not found", "update error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkAllRead marks all of the caller's notifications read.
func (h *NotificationsHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.MarkAllRead(r.Context(), userIDFromCtx(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetPreferences returns the caller's email notification preferences.
func (h *NotificationsHandler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.GetPreferences(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// UpdatePreferences writes the caller's email notification preferences.
func (h *NotificationsHandler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	var body model.NotificationPreferences
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if err := h.repo.UpdatePreferences(r.Context(), userIDFromCtx(r), body); err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, body)
}
