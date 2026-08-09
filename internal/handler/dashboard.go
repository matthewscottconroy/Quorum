package handler

import (
	"context"
	"log"
	"net/http"
	"strings"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// openBillsCounter is the slice of *repo.BillsRepo the dashboard needs.
type openBillsCounter interface {
	CountOpen(ctx context.Context) (int, error)
}

// DashboardHandler serves the summary endpoint used by the app's home screen.
type DashboardHandler struct {
	dues            duesRepo
	members         membersRepo
	meetings        meetingsRepo
	actionItems     actionItemsRepo
	bills           openBillsCounter
	settings        orgSettingsRepo
	schedules       scheduleLister
	emailConfigured bool
}

// NewDashboardHandler constructs a DashboardHandler.
func NewDashboardHandler(d duesRepo, m membersRepo, mt meetingsRepo, ai actionItemsRepo) *DashboardHandler {
	return &DashboardHandler{dues: d, members: m, meetings: mt, actionItems: ai}
}

// SetOpenBills wires the accounts-payable source so the summary carries the
// open-bill count. Optional; without it the count is reported as 0.
func (h *DashboardHandler) SetOpenBills(b openBillsCounter) { h.bills = b }

// scheduleLister is the slice of *repo.DuesRepo the setup checklist needs.
type scheduleLister interface {
	ListActiveDuesSchedules(ctx context.Context) ([]model.DuesSchedule, error)
}

// SetSetupDeps wires what the first-run checklist inspects: org settings,
// dues schedules, and whether outbound email is configured.
func (h *DashboardHandler) SetSetupDeps(settings orgSettingsRepo, schedules scheduleLister, emailConfigured bool) {
	h.settings = settings
	h.schedules = schedules
	h.emailConfigured = emailConfigured
}

// SetupStatus reports first-run progress for the admin checklist: which
// foundational steps are done, derived from data that already exists. Admin
// route; returns booleans only, never values.
func (h *DashboardHandler) SetupStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	memberCount, err := h.members.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	schedules := 0
	if h.schedules != nil {
		if ss, err := h.schedules.ListActiveDuesSchedules(ctx); err == nil {
			schedules = len(ss)
		}
	}
	meetings, err := h.meetings.Upcoming(ctx, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	howToPay, twoFA := false, false
	if h.settings != nil {
		if all, err := h.settings.All(ctx); err == nil {
			howToPay = strings.TrimSpace(all["how_to_pay"]) != ""
			twoFA = all["require_2fa"] != "" && all["require_2fa"] != "off"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"members_added":     memberCount > 1, // the bootstrap admin alone doesn't count
		"dues_schedule_set": schedules > 0,
		"how_to_pay_set":    howToPay,
		"email_configured":  h.emailConfigured,
		"require_2fa_set":   twoFA,
		"meeting_scheduled": len(meetings) > 0,
	})
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

	openBills := 0
	if h.bills != nil {
		openBills, err = h.bills.CountOpen(ctx)
		if err != nil {
			log.Printf("dashboard: count open bills: %v", err)
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
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
		OpenBillsCount:    openBills,
		UpcomingMeetings:  meetings,
		OpenActionItems:   openItems,
	})
}
