package handler

import (
	"log"
	"net/http"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// DashboardHandler serves the summary endpoint used by the app's home screen.
type DashboardHandler struct {
	dues        duesRepo
	members     membersRepo
	meetings    meetingsRepo
	actionItems actionItemsRepo
}

// NewDashboardHandler constructs a DashboardHandler.
func NewDashboardHandler(d duesRepo, m membersRepo, mt meetingsRepo, ai actionItemsRepo) *DashboardHandler {
	return &DashboardHandler{dues: d, members: m, meetings: mt, actionItems: ai}
}

// Summary returns the dashboard counts and recent items.
func (h *DashboardHandler) Summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Any query failure is a real error: returning zeroed counts with a 200
	// would silently mislead users and hide outages from monitoring.
	overdue, err := h.dues.CountByStatus(ctx, "overdue")
	if err != nil {
		log.Printf("dashboard: count overdue: %v", err)
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	pending, err := h.dues.CountByStatus(ctx, "pending")
	if err != nil {
		log.Printf("dashboard: count pending: %v", err)
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	memberCount, err := h.members.Count(ctx)
	if err != nil {
		log.Printf("dashboard: count members: %v", err)
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	meetings, err := h.meetings.Upcoming(ctx, 5)
	if err != nil {
		log.Printf("dashboard: upcoming meetings: %v", err)
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	openItems, _, err := h.actionItems.List(ctx, repo.ActionItemFilter{Status: "open", Limit: 10})
	if err != nil {
		log.Printf("dashboard: open action items: %v", err)
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}

	if meetings == nil {
		meetings = []model.Meeting{}
	}
	if openItems == nil {
		openItems = []model.ActionItem{}
	}

	writeJSON(w, 200, model.DashboardSummary{
		OverdueDuesCount:  overdue,
		PendingDuesCount:  pending,
		ActiveMemberCount: memberCount,
		UpcomingMeetings:  meetings,
		OpenActionItems:   openItems,
	})
}
