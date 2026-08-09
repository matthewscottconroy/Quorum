package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// calendarTokenRepo is the slice of *repo.AuthRepo the calendar feed needs.
type calendarTokenRepo interface {
	EnsureCalendarToken(ctx context.Context, userID string) (string, error)
	RotateCalendarToken(ctx context.Context, userID string) (string, error)
	UserIDByCalendarToken(ctx context.Context, token string) (string, error)
}

// calendarMeetings is the meetings slice the feed reads.
type calendarMeetings interface {
	List(ctx context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error)
}

// CalendarHandler serves per-user calendar subscription: an authenticated
// endpoint to mint/rotate a token, and a public tokenized ICS feed a calendar
// app polls. The token is an opaque read credential; meetings are member-
// visible org-wide, so the feed exposes nothing a member couldn't already see.
type CalendarHandler struct {
	tokens   calendarTokenRepo
	meetings calendarMeetings
	baseURL  string
}

// NewCalendarHandler constructs the handler. baseURL is the public origin used
// to build the subscription URL shown to the user.
func NewCalendarHandler(t calendarTokenRepo, m calendarMeetings, baseURL string) *CalendarHandler {
	return &CalendarHandler{tokens: t, meetings: m, baseURL: strings.TrimRight(baseURL, "/")}
}

// Subscription returns (creating if needed) the caller's calendar feed URL.
func (h *CalendarHandler) Subscription(w http.ResponseWriter, r *http.Request) {
	tok, err := h.tokens.EnsureCalendarToken(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create feed", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": h.feedURL(tok)})
}

// Rotate issues a new token, invalidating the old feed URL.
func (h *CalendarHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	tok, err := h.tokens.RotateCalendarToken(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not rotate feed", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": h.feedURL(tok)})
}

func (h *CalendarHandler) feedURL(token string) string {
	return h.baseURL + "/api/v1/calendar/" + token + ".ics"
}

// Feed is the PUBLIC tokenized ICS feed (no session; the token authorizes).
// Returns meetings from 30 days back onward, inline so calendar apps render
// rather than download it. An unknown token is a 404.
func (h *CalendarHandler) Feed(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(chi.URLParam(r, "token"), ".ics")
	if len(token) != 64 {
		writeError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if _, err := h.tokens.UserIDByCalendarToken(r.Context(), token); err != nil {
		writeError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	from := time.Now().AddDate(0, 0, -30)
	meetings, _, err := h.meetings.List(r.Context(), repo.MeetingFilter{From: &from, Limit: 500})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	// Inline (not attachment) so subscribing clients render the calendar.
	writeICSInline(w, meetings)
}
