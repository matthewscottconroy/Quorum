package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"quorum/internal/model"
	"quorum/internal/pdf"
	"quorum/internal/repo"
)

// PDF reports for the people who need paper: the member roster, the dues/
// receivables picture, a meeting's minutes, and the audit log. Every report —
// and every other export in the application — is logged to the tamper-evident
// audit chain: who exported what, and when (action "EXPORT <what>").

// auditExport records an export event in the audit log. GET requests bypass
// the AuditMiddleware by design, so export endpoints log explicitly. A missing
// logger or anonymous request records nothing (public endpoints never reach
// here; every export route requires auth).
func auditExport(r *http.Request, ar auditRepo, what string, detail map[string]any) {
	if ar == nil {
		return
	}
	uid := userIDFromCtx(r)
	if uid == "" {
		return
	}
	ar.Log(r.Context(), uid, "EXPORT "+what, "export", "", detail) //nolint:errcheck
}

// ReportsHandler renders the PDF reports.
type ReportsHandler struct {
	members  membersRepo
	dues     duesRepo
	meetings meetingsRepo
	gov      minutesGovSource
	auditRd  auditReader
	verifier chainVerifier
	audit    auditRepo
}

// NewReportsHandler constructs a ReportsHandler.
func NewReportsHandler(m membersRepo, d duesRepo, mt meetingsRepo, g minutesGovSource, ard auditReader, v chainVerifier, a auditRepo) *ReportsHandler {
	return &ReportsHandler{members: m, dues: d, meetings: mt, gov: g, auditRd: ard, verifier: v, audit: a}
}

func servePDF(w http.ResponseWriter, d *pdf.Doc, filename string) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write(d.Bytes())
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MembersPDF renders the member roster (officer+).
func (h *ReportsHandler) MembersPDF(w http.ResponseWriter, r *http.Request) {
	members, total, err := h.members.List(r.Context(), repo.MemberFilter{Limit: 200})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	d := pdf.New("Member roster")
	d.Title("Member roster")
	d.Line(fmt.Sprintf("Generated %s - %d member(s)", time.Now().Format("2 January 2006 15:04 MST"), total))
	d.Space()
	byStatus := map[string][]model.Member{}
	for _, m := range members {
		byStatus[m.Status] = append(byStatus[m.Status], m)
	}
	for _, status := range []string{"active", "suspended", "inactive"} {
		group := byStatus[status]
		if len(group) == 0 {
			continue
		}
		d.Heading(fmt.Sprintf("%s (%d)", status, len(group)))
		for _, m := range group {
			line := m.DisplayName + " - " + m.Tier
			if e := deref(m.Email); e != "" {
				line += " - " + e
			}
			if p := deref(m.Phone); p != "" {
				line += " - " + p
			}
			d.Line(line)
			d.Indented("joined " + m.JoinedAt.Format("2006-01-02") + " - dues: " + m.DuesStatus)
		}
	}
	auditExport(r, h.audit, "members.pdf", map[string]any{"members": total})
	servePDF(w, d, "quorum-members.pdf")
}

// DuesPDF renders the receivables/collections picture (officer+).
func (h *ReportsHandler) DuesPDF(w http.ResponseWriter, r *http.Request) {
	invoices, total, err := h.dues.ListInvoices(r.Context(), repo.InvoiceFilter{Limit: 500})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	txs, _, err := h.dues.ListTransactions(r.Context(), repo.TransactionFilter{Limit: 50})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}

	d := pdf.New("Dues report")
	d.Title("Dues & receivables report")
	d.Line(fmt.Sprintf("Generated %s - %d invoice(s) considered (most recent %d shown in detail)",
		time.Now().Format("2 January 2006 15:04 MST"), total, len(invoices)))
	d.Space()

	// Per-status, per-currency rollup: never sum across currencies at par.
	type key struct{ status, currency string }
	sums := map[key]int64{}
	counts := map[key]int{}
	for _, inv := range invoices {
		k := key{inv.Status, inv.Currency}
		sums[k] += inv.AmountMinor
		counts[k]++
	}
	d.Heading("Invoices by status")
	for _, status := range []string{"pending", "overdue", "partial", "paid", "cancelled"} {
		for k, n := range counts {
			if k.status != status {
				continue
			}
			d.Line(fmt.Sprintf("%s: %d invoice(s) - %s %s", status, n,
				model.FormatMoney(sums[k], k.currency), k.currency))
		}
	}
	d.Space()
	d.Heading("Recent payments")
	if len(txs) == 0 {
		d.Line("No payments recorded.")
	}
	for _, t := range txs {
		name := t.MemberName
		if name == "" {
			name = "(unlinked)"
		}
		d.Line(fmt.Sprintf("%s - %s %s - %s - %s",
			t.OccurredAt.Format("2006-01-02"), model.FormatMoney(t.AmountMinor, t.Currency),
			t.Currency, name, t.Provider))
	}
	auditExport(r, h.audit, "dues.pdf", map[string]any{"invoices": total})
	servePDF(w, d, "quorum-dues.pdf")
}

// MinutesPDF renders one meeting's minutes as PDF (officer+), same content as
// the Markdown document.
func (h *ReportsHandler) MinutesPDF(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	mt, err := h.meetings.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found", "not_found")
		return
	}
	entries, err := h.meetings.ListMinutes(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	var motions []model.Motion
	if h.gov != nil {
		if motions, err = h.gov.ListMotions(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
		for i := range motions {
			if motions[i].Votes, err = h.gov.GetVotes(r.Context(), motions[i].ID); err != nil {
				writeError(w, http.StatusInternalServerError, "query error", "internal_error")
				return
			}
		}
	}

	d := pdf.New("Minutes - " + mt.Title)
	d.Title("Minutes - " + mt.Title)
	if mt.MinutesFinalizedAt == nil {
		d.Bold("DRAFT - these minutes have not been finalized.")
	} else {
		d.Line("Finalized " + mt.MinutesFinalizedAt.Format("2006-01-02 15:04 MST"))
	}
	d.Line("Date: " + mt.ScheduledAt.Format("Monday, 2 January 2006, 15:04 MST"))
	if mt.Location != nil && *mt.Location != "" {
		d.Line("Location: " + *mt.Location)
	}
	d.Space()

	if len(mt.Attendees) > 0 {
		d.Heading("Attendance")
		var present, absent []string
		for _, a := range mt.Attendees {
			if a.Present {
				present = append(present, a.MemberName)
			} else {
				absent = append(absent, a.MemberName)
			}
		}
		if len(present) > 0 {
			d.Line(fmt.Sprintf("Present (%d): %s", len(present), joinSorted(present)))
		}
		if len(absent) > 0 {
			d.Line(fmt.Sprintf("Absent (%d): %s", len(absent), joinSorted(absent)))
		}
	}

	if len(entries) > 0 {
		d.Heading("Proceedings")
		for _, e := range entries {
			line := fmt.Sprintf("%d. %s - %s", e.Seq, minutesKindLabels[e.Kind], e.Body)
			if e.MotionTitle != nil {
				line += fmt.Sprintf(" (re: motion \"%s\")", *e.MotionTitle)
			}
			d.Line(line)
		}
	}

	if len(motions) > 0 {
		d.Heading("Motions")
		for _, m := range motions {
			biz := "New business"
			if m.Business == "old" {
				biz = "Old business"
			}
			d.Bold(m.Title)
			d.Indented(fmt.Sprintf("%s - threshold: %s", biz, m.Threshold))
			if m.MoverName != nil {
				d.Indented("Moved by: " + *m.MoverName)
			}
			if m.SeconderName != nil {
				d.Indented("Seconded by: " + *m.SeconderName)
			}
			result := fmt.Sprintf("Result: %s", m.Status)
			tally := model.MotionTally{}
			for _, v := range m.Votes {
				switch v.Choice {
				case "for":
					tally.For++
				case "against":
					tally.Against++
				case "abstain":
					tally.Abstain++
				}
			}
			if tally.For+tally.Against+tally.Abstain > 0 {
				result += fmt.Sprintf(" - %d for, %d against, %d abstain", tally.For, tally.Against, tally.Abstain)
			}
			d.Indented(result)
			d.Space()
		}
	}

	if len(mt.Decisions) > 0 {
		d.Heading("Decisions recorded")
		for _, dec := range mt.Decisions {
			d.Line(fmt.Sprintf("%s - %s", dec.Outcome, dec.Summary))
		}
	}
	auditExport(r, h.audit, "meetings/"+id+"/minutes.pdf", map[string]any{"meeting": mt.Title})
	servePDF(w, d, "minutes-"+mt.ScheduledAt.Format("2006-01-02")+".pdf")
}

// AuditPDF renders the recent audit log with the chain status (admin+).
func (h *ReportsHandler) AuditPDF(w http.ResponseWriter, r *http.Request) {
	const window = 500
	entries, total, err := h.auditRd.List(r.Context(), repo.AuditFilter{Limit: window})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	var st *repo.ChainStatus
	if h.verifier != nil {
		if st, err = h.verifier.VerifyChain(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "verification error", "internal_error")
			return
		}
	}

	d := pdf.New("Audit log")
	d.Title("Audit log report")
	d.Line(fmt.Sprintf("Generated %s - showing the most recent %d of %d entries",
		time.Now().Format("2 January 2006 15:04 MST"), len(entries), total))
	if st != nil {
		if st.OK {
			d.Bold(fmt.Sprintf("Hash chain VERIFIED at generation time - %d entries, head %s", st.Entries, st.HeadHash))
		} else {
			d.Bold(fmt.Sprintf("WARNING: HASH CHAIN BROKEN at seq %d - treat this log as compromised", st.BrokenSeq))
		}
	}
	d.Line("The authoritative, independently verifiable export is the evidence CSV (see COMPLIANCE.md).")
	d.Space()
	for _, e := range entries {
		who := deref(e.UserEmail)
		if who == "" {
			who = "(deleted account)"
		}
		line := fmt.Sprintf("%s - %s - %s", e.CreatedAt.Format("2006-01-02 15:04:05"), who, e.Action)
		if et := deref(e.EntityType); et != "" {
			line += " - " + et
		}
		d.Line(line)
		if e.Detail != "" {
			d.Indented(e.Detail)
		}
	}
	auditExport(r, h.audit, "audit.pdf", map[string]any{"entries_shown": len(entries)})
	servePDF(w, d, "quorum-audit.pdf")
}

func joinSorted(names []string) string {
	sort.Strings(names)
	return strings.Join(names, ", ")
}
