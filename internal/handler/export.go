package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"quorum/internal/model"
	"quorum/internal/pdf"
	"quorum/internal/repo"
)

// csvSafe neutralizes spreadsheet formula injection: a cell that a spreadsheet
// would evaluate (leading = + - @, or a leading tab/CR that some parsers strip
// to reveal one) is prefixed with an apostrophe so Excel/Sheets treat it as
// literal text. Applied to every user-influenced text field in an export.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// exportPageSize is how many rows each internal pagination step pulls when
// streaming a full-table export. The repo List methods cap a page at 200.
const exportPageSize = 200

// ExportHandler serves bulk data exports: CSV downloads for staff and a
// personal-data (JSON) export for the authenticated user.
type ExportHandler struct {
	members membersRepo
	dues    duesRepo
	auth    authRepo
	audit   auditRepo
}

// SetAuditLogger attaches the audit log: every export is recorded (who, what,
// when) in the tamper-evident chain.
func (h *ExportHandler) SetAuditLogger(a auditRepo) { h.audit = a }

// NewExportHandler constructs an ExportHandler.
func NewExportHandler(m membersRepo, d duesRepo, a authRepo) *ExportHandler {
	return &ExportHandler{members: m, dues: d, auth: a}
}

// setCSVHeaders marks the response as a downloadable CSV with the given filename.
func setCSVHeaders(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
}

// fmtTime renders a timestamp in RFC 3339 for stable, spreadsheet-friendly output.
func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// allMembers pages through the member list until the table is exhausted.
func (h *ExportHandler) allMembers(ctx context.Context) ([]model.Member, error) {
	var out []model.Member
	for offset := 0; ; offset += exportPageSize {
		page, total, err := h.members.List(ctx, repo.MemberFilter{Limit: exportPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(out) >= total || len(page) == 0 {
			break
		}
	}
	return out, nil
}

// allInvoices pages through invoices, optionally scoped to one member.
func (h *ExportHandler) allInvoices(ctx context.Context, memberID string) ([]model.DuesInvoice, error) {
	var out []model.DuesInvoice
	for offset := 0; ; offset += exportPageSize {
		page, total, err := h.dues.ListInvoices(ctx, repo.InvoiceFilter{MemberID: memberID, Limit: exportPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(out) >= total || len(page) == 0 {
			break
		}
	}
	return out, nil
}

// allTransactions pages through transactions, optionally scoped to one member.
func (h *ExportHandler) allTransactions(ctx context.Context, memberID string) ([]model.Transaction, error) {
	var out []model.Transaction
	for offset := 0; ; offset += exportPageSize {
		page, total, err := h.dues.ListTransactions(ctx, repo.TransactionFilter{MemberID: memberID, Limit: exportPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(out) >= total || len(page) == 0 {
			break
		}
	}
	return out, nil
}

// ExportMembersCSV streams every member as CSV (member role and above).
func (h *ExportHandler) ExportMembersCSV(w http.ResponseWriter, r *http.Request) {
	auditExport(r, h.audit, "members.csv", nil)
	members, err := h.allMembers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed", "internal_error")
		return
	}
	setCSVHeaders(w, "members.csv")
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "display_name", "email", "phone", "address", "tier", "status", "joined_at", "dues_status", "created_at"}) //nolint:errcheck
	for _, m := range members {
		cw.Write([]string{ //nolint:errcheck
			m.ID, csvSafe(m.DisplayName), csvSafe(derefStr(m.Email)), csvSafe(derefStr(m.Phone)), csvSafe(derefStr(m.Address)),
			csvSafe(m.Tier), m.Status, fmtTime(m.JoinedAt), m.DuesStatus, fmtTime(m.CreatedAt),
		})
	}
	cw.Flush()
}

// ExportDuesCSV streams every dues invoice as CSV (officer role and above).
func (h *ExportHandler) ExportDuesCSV(w http.ResponseWriter, r *http.Request) {
	auditExport(r, h.audit, "dues.csv", nil)
	invoices, err := h.allInvoices(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed", "internal_error")
		return
	}
	setCSVHeaders(w, "dues.csv")
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "member_id", "member_name", "amount_minor", "amount", "currency", "period_label", "due_date", "status", "created_at"}) //nolint:errcheck
	for _, inv := range invoices {
		cw.Write([]string{ //nolint:errcheck
			inv.ID, inv.MemberID, csvSafe(inv.MemberName),
			strconv.FormatInt(inv.AmountMinor, 10), model.FormatMoney(inv.AmountMinor, inv.Currency), inv.Currency,
			csvSafe(inv.PeriodLabel), fmtTime(inv.DueDate), inv.Status, fmtTime(inv.CreatedAt),
		})
	}
	cw.Flush()
}

// ExportTransactionsCSV streams every payment transaction as CSV (officer and above).
func (h *ExportHandler) ExportTransactionsCSV(w http.ResponseWriter, r *http.Request) {
	auditExport(r, h.audit, "transactions.csv", nil)
	txns, err := h.allTransactions(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed", "internal_error")
		return
	}
	setCSVHeaders(w, "transactions.csv")
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "invoice_id", "member_id", "member_name", "amount_minor", "amount", "currency", "provider", "provider_reference_id", "occurred_at"}) //nolint:errcheck
	for _, t := range txns {
		cw.Write([]string{ //nolint:errcheck
			t.ID, derefStr(t.InvoiceID), derefStr(t.MemberID), csvSafe(t.MemberName),
			strconv.FormatInt(t.AmountMinor, 10), model.FormatMoney(t.AmountMinor, t.Currency), t.Currency,
			csvSafe(t.Provider), csvSafe(derefStr(t.ProviderReferenceID)), fmtTime(t.OccurredAt),
		})
	}
	cw.Flush()
}

// ExportMyStatement renders the caller's annual member statement PDF: their
// invoices and payments for a year, for reimbursement or donation records.
// ?year=YYYY (defaults to the current year).
func (h *ExportHandler) ExportMyStatement(w http.ResponseWriter, r *http.Request) {
	user, err := h.auth.GetUserByID(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found", "not_found")
		return
	}
	if user.MemberID == nil || *user.MemberID == "" {
		writeError(w, http.StatusBadRequest, "your login isn't linked to a member record", "bad_request")
		return
	}
	year := nowUTC(r).Year()
	if y, perr := strconv.Atoi(r.URL.Query().Get("year")); perr == nil && y >= 2000 && y <= 3000 {
		year = y
	}
	member, _ := h.members.Get(r.Context(), *user.MemberID)
	invs, _ := h.allInvoices(r.Context(), *user.MemberID)
	txns, _ := h.allTransactions(r.Context(), *user.MemberID)

	name := user.Email
	if member != nil {
		name = member.DisplayName
	}
	d := pdf.New("Member statement")
	d.Title("Member statement")
	d.Line(fmt.Sprintf("%s — %d", name, year))
	d.Space()

	d.Heading("Invoices")
	invCount := 0
	for _, inv := range invs {
		if inv.DueDate.Year() != year {
			continue
		}
		invCount++
		d.Line(fmt.Sprintf("%-14s %-22s %10s %-10s due %s",
			inv.PeriodLabel, trunc(inv.MemberName, 22),
			model.FormatMoney(inv.AmountMinor, inv.Currency), inv.Status,
			inv.DueDate.Format("2006-01-02")))
	}
	if invCount == 0 {
		d.Line("(no invoices this year)")
	}
	d.Space()

	d.Heading("Payments")
	var paid = map[string]int64{}
	payCount := 0
	for _, t := range txns {
		if t.OccurredAt.Year() != year {
			continue
		}
		// Failed provider transactions never counted toward an invoice; a
		// donation/reimbursement statement must not count them either.
		if t.ProviderStatus != nil && *t.ProviderStatus == "failed" {
			continue
		}
		payCount++
		paid[t.Currency] += t.AmountMinor
		d.Line(fmt.Sprintf("%s  %10s  via %s",
			t.OccurredAt.Format("2006-01-02"), model.FormatMoney(t.AmountMinor, t.Currency), t.Provider))
	}
	if payCount == 0 {
		d.Line("(no payments this year)")
	}
	d.Space()
	d.Heading("Total paid")
	if len(paid) == 0 {
		d.Line("0")
	}
	for cur, amt := range paid {
		d.Line(fmt.Sprintf("%s %s", model.FormatMoney(amt, cur), cur))
	}
	d.Watermark(fmt.Sprintf("Statement for %s — generated %s", name, nowUTC(r).Format("2006-01-02")))
	body, _ := d.Finalize()

	auditExport(r, h.audit, fmt.Sprintf("statement-%d.pdf", year), map[string]any{"year": year})
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("member-statement-%d.pdf", year)))
	_, _ = w.Write(body)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ExportMyData returns the authenticated user's own account, linked member
// record, and their dues invoices and transactions as a single JSON document —
// a portable personal-data export. A user not linked to a member record still
// gets their account back with empty member sections.
func (h *ExportHandler) ExportMyData(w http.ResponseWriter, r *http.Request) {
	auditExport(r, h.audit, "my-data.json", nil)
	userID := userIDFromCtx(r)
	user, err := h.auth.GetUserByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found", "not_found")
		return
	}

	out := map[string]any{
		"account":      user,
		"member":       nil,
		"invoices":     []model.DuesInvoice{},
		"transactions": []model.Transaction{},
		"exported_at":  fmtTime(nowUTC(r)),
	}

	if user.MemberID != nil && *user.MemberID != "" {
		if m, merr := h.members.Get(r.Context(), *user.MemberID); merr == nil {
			out["member"] = m
		}
		if invs, ierr := h.allInvoices(r.Context(), *user.MemberID); ierr == nil && invs != nil {
			out["invoices"] = invs
		}
		if txns, terr := h.allTransactions(r.Context(), *user.MemberID); terr == nil && txns != nil {
			out["transactions"] = txns
		}
	}

	w.Header().Set("Content-Disposition", `attachment; filename="my-quorum-data.json"`)
	writeJSON(w, http.StatusOK, out)
}

// nowUTC returns the request time; isolated so the JSON export has a single
// timestamp source (kept trivial — request handling has no injected clock).
func nowUTC(_ *http.Request) time.Time { return time.Now() }
