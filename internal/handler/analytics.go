package handler

import (
	"net/http"
	"time"

	"quorum/internal/repo"
)

// AnalyticsHandler serves aggregate analytics for the dashboard (officer+).
type AnalyticsHandler struct {
	repo analyticsRepo
}

// NewAnalyticsHandler constructs an AnalyticsHandler.
func NewAnalyticsHandler(r analyticsRepo) *AnalyticsHandler {
	return &AnalyticsHandler{repo: r}
}

// Overview returns the headline KPIs.
func (h *AnalyticsHandler) Overview(w http.ResponseWriter, r *http.Request) {
	o, err := h.repo.Overview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analytics error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// Membership returns roster breakdowns and growth.
func (h *AnalyticsHandler) Membership(w http.ResponseWriter, r *http.Request) {
	m, err := h.repo.Membership(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analytics error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// Attendance returns per-meeting attendance stats.
func (h *AnalyticsHandler) Attendance(w http.ResponseWriter, r *http.Request) {
	a, err := h.repo.Attendance(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analytics error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// AttendanceMembers returns the per-member transpose: attended/held/streak
// over ?from=&to= (defaults: the last 12 months).
func (h *AnalyticsHandler) AttendanceMembers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	if !validISODate(from) || !validISODate(to) {
		writeError(w, http.StatusBadRequest, "from and to must be YYYY-MM-DD", "bad_request")
		return
	}
	rows, err := h.repo.AttendancePerMember(r.Context(), from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analytics error", "internal_error")
		return
	}
	if rows == nil {
		rows = []repo.MemberAttendanceRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "members": rows})
}

// Governance returns motion outcomes and the vote split.
func (h *AnalyticsHandler) Governance(w http.ResponseWriter, r *http.Request) {
	g, err := h.repo.Governance(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analytics error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// Payments returns collection trends and dues status breakdown.
func (h *AnalyticsHandler) Payments(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Payments(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "analytics error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
