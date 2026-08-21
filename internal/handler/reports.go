package handler

import (
	"context"
	"fmt"
	"log"
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

// exporterLookup resolves the exporting account for the watermark.
type exporterLookup interface {
	GetUserByID(ctx context.Context, id string) (*model.User, error)
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
	users    exporterLookup
	tzSource func(ctx context.Context) *time.Location
}

// SetTimezoneSource attaches the org display-timezone resolver (same source
// the minutes .md uses), so the PDF's timestamps agree with the document.
func (h *ReportsHandler) SetTimezoneSource(fn func(ctx context.Context) *time.Location) {
	h.tzSource = fn
}

func (h *ReportsHandler) location(ctx context.Context) *time.Location {
	if h.tzSource != nil {
		if loc := h.tzSource(ctx); loc != nil {
			return loc
		}
	}
	return time.Local
}

// pdfFromMinutesDoc typesets the finalized minutes MARKDOWN SNAPSHOT into a
// PDF. Finality (0054) means the snapshot IS the official record: the PDF
// must be built from those bytes, never re-rendered from live rows, where a
// member rename or motion edit would silently rewrite history the .md keeps
// verbatim.
func pdfFromMinutesDoc(title, doc string) *pdf.Doc {
	d := pdf.New(title)
	for _, raw := range strings.Split(doc, "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case line == "":
			d.Space()
		case strings.HasPrefix(line, "# "):
			d.Title(strings.TrimPrefix(line, "# "))
		case strings.HasPrefix(line, "## "):
			d.Heading(strings.TrimPrefix(line, "## "))
		case strings.HasPrefix(line, "### "):
			d.Heading(strings.TrimPrefix(line, "### "))
		case strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && len(line) > 4:
			d.Bold(strings.TrimSuffix(strings.TrimPrefix(line, "**"), "**"))
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			d.Indented("• " + line[2:])
		default:
			d.Line(strings.ReplaceAll(line, "**", ""))
		}
	}
	return d
}

// NewReportsHandler constructs a ReportsHandler.
func NewReportsHandler(m membersRepo, d duesRepo, mt meetingsRepo, g minutesGovSource, ard auditReader, v chainVerifier, a auditRepo, u exporterLookup) *ReportsHandler {
	return &ReportsHandler{members: m, dues: d, meetings: mt, gov: g, auditRd: ard, verifier: v, audit: a, users: u}
}

// stamp watermarks the document with who is exporting and when, and returns
// the finished bytes plus the document's SHA-256 (spliced into the PDF and
// recorded in the audit entry, so the file is verifiable offline AND anchored
// to the tamper-evident chain).
func (h *ReportsHandler) stamp(r *http.Request, d *pdf.Doc) ([]byte, string) {
	who := userIDFromCtx(r)
	if h.users != nil {
		if u, err := h.users.GetUserByID(r.Context(), who); err == nil && u.Email != "" {
			who = u.Email
		}
	}
	d.Watermark(fmt.Sprintf("Exported by %s - %s", who, time.Now().UTC().Format("2006-01-02 15:04 UTC")))
	return d.Finalize()
}

func servePDF(w http.ResponseWriter, data []byte, digest, filename string) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Document-SHA256", digest)
	_, _ = w.Write(data)
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
	data, digest := h.stamp(r, d)
	auditExport(r, h.audit, "members.pdf", map[string]any{"members": total, "sha256": digest})
	servePDF(w, data, digest, "quorum-members.pdf")
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
	data, digest := h.stamp(r, d)
	auditExport(r, h.audit, "dues.pdf", map[string]any{"invoices": total, "sha256": digest})
	servePDF(w, data, digest, "quorum-dues.pdf")
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
	loc := h.location(r.Context())
	// Finalized minutes: the PDF is typeset from the frozen snapshot — the
	// same bytes the .md serves — with the append-only corrections beneath.
	// A live re-render here would let a member rename rewrite the "official
	// record" and would hide filed errata entirely.
	if mt.MinutesFinalizedAt != nil {
		if snap, serr := h.meetings.GetMinutesSnapshot(r.Context(), id); serr == nil && snap != "" {
			d := pdfFromMinutesDoc("Minutes - "+mt.Title, snap)
			if corrs, cerr := h.meetings.ListCorrections(r.Context(), id); cerr == nil && len(corrs) > 0 {
				d.Space()
				d.Heading("Corrections (errata)")
				d.Line("Filed after finalization; the approved document above is unchanged.")
				for _, c := range corrs {
					who := c.AuthorName
					if who == "" {
						who = "unknown"
					}
					d.Indented(fmt.Sprintf("%s — %s (%s)", c.CreatedAt.In(loc).Format("2006-01-02"), c.Body, who))
				}
			}
			data, digest := h.stamp(r, d)
			auditExport(r, h.audit, "meetings/"+id+"/minutes.pdf",
				map[string]any{"meeting": mt.Title, "source": "snapshot", "sha256": digest})
			servePDF(w, data, digest, "minutes-"+mt.ScheduledAt.In(loc).Format("2006-01-02")+".pdf")
			return
		}
		log.Printf("minutes pdf %s: finalized but no snapshot — serving a live render", id)
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
		votes, verr := h.gov.VotesByMeeting(r.Context(), id)
		if verr != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
		for i := range motions {
			motions[i].Votes = votes[motions[i].ID]
		}
	}

	d := pdf.New("Minutes - " + mt.Title)
	d.Title("Minutes - " + mt.Title)
	if mt.MinutesFinalizedAt == nil {
		d.Bold("DRAFT - these minutes have not been finalized.")
	} else {
		d.Line("Finalized " + mt.MinutesFinalizedAt.In(loc).Format("2006-01-02 15:04 MST"))
	}
	d.Line("Date: " + mt.ScheduledAt.In(loc).Format("Monday, 2 January 2006, 15:04 MST"))
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
	data, digest := h.stamp(r, d)
	auditExport(r, h.audit, "meetings/"+id+"/minutes.pdf", map[string]any{"meeting": mt.Title, "sha256": digest})
	servePDF(w, data, digest, "minutes-"+mt.ScheduledAt.In(loc).Format("2006-01-02")+".pdf")
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
	data, digest := h.stamp(r, d)
	auditExport(r, h.audit, "audit.pdf", map[string]any{"entries_shown": len(entries), "sha256": digest})
	servePDF(w, data, digest, "quorum-audit.pdf")
}

func joinSorted(names []string) string {
	sort.Strings(names)
	return strings.Join(names, ", ")
}
