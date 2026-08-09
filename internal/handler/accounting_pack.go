package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"quorum/internal/model"
	"quorum/internal/pdf"
)

// packFundsSource is the slice of *repo.FundsRepo the pack needs.
type packFundsSource interface {
	ListFunds(ctx context.Context) ([]model.Fund, error)
}

// packBillsSource is the slice of *repo.BillsRepo the pack needs for accounts
// payable (optional — set via SetBills).
type packBillsSource interface {
	List(ctx context.Context, status string, limit int) ([]model.Bill, error)
	APAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error)
}

// AccountingPackHandler produces the CPA export pack: one ZIP holding
// machine-readable CSVs, a sealed statements PDF, and the evidence bundle
// pointer — watermarked, hashed, and audited like every export.
type AccountingPackHandler struct {
	gl       glRepo
	c        glRepoC
	funds    packFundsSource
	bills    packBillsSource
	verifier chainVerifier
	audit    auditRepo
	users    exporterLookup
}

// NewAccountingPackHandler constructs the handler.
func NewAccountingPackHandler(gl glRepo, c glRepoC, funds packFundsSource, v chainVerifier, a auditRepo, u exporterLookup) *AccountingPackHandler {
	return &AccountingPackHandler{gl: gl, c: c, funds: funds, verifier: v, audit: a, users: u}
}

// SetBills wires the accounts-payable source so the pack includes bills.csv
// and ap-aging.csv. Optional; without it those files are simply omitted.
func (h *AccountingPackHandler) SetBills(b packBillsSource) { h.bills = b }

func rulesCSV(rules []model.PostingRule) [][]string {
	out := [][]string{{"rule_key", "account_code", "account_name"}}
	for _, p := range rules {
		out = append(out, []string{p.Key, p.AccountCode, p.AccountName})
	}
	return out
}

func csvBytes(rows [][]string) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.WriteAll(rows)
	w.Flush()
	return buf.Bytes()
}

func balancesCSV(header string, rows []model.GLBalance) [][]string {
	out := [][]string{{"code", "name", "type", "currency", "debits_minor", "credits_minor", "balance_minor"}}
	_ = header
	for _, b := range rows {
		out = append(out, []string{b.Code, b.Name, b.Type, b.Currency,
			fmt.Sprint(b.Debits), fmt.Sprint(b.Credits), fmt.Sprint(b.Balance)})
	}
	return out
}

// Zip renders the pack (admin). ?from=&to= bound the ledger and income
// statement; the balance sheet and aging are as of `to`.
func (h *AccountingPackHandler) Zip(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !validISODate(from) || !validISODate(to) {
		writeError(w, http.StatusBadRequest, "from and to (YYYY-MM-DD) required", "bad_request")
		return
	}
	ctx := r.Context()

	tb, err := h.gl.TrialBalance(ctx)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	rec, err := h.gl.Reconcile(ctx)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	ledger, err := h.c.LedgerCSVRows(ctx, from, to)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	income, err := h.c.Statement(ctx, from, to, []string{"income", "expense"})
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	position, err := h.c.Statement(ctx, "0001-01-01", to, []string{"asset", "liability", "net_assets"})
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	aging, err := h.c.ARAging(ctx, to)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	cashIncome, err := h.c.StatementCash(ctx, from, to)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	purchases, err := h.c.PurchasesCSVRows(ctx, from, to)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	rules, err := h.c.PostingRules(ctx)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	funds, err := h.funds.ListFunds(ctx)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	chainOK, chainHead := false, "unavailable"
	if st, err := h.verifier.VerifyChain(ctx); err == nil && st != nil {
		chainOK, chainHead = st.OK, st.HeadHash
	}

	// --- the sealed statements PDF ---
	who := userIDFromCtx(r)
	if u, err := h.users.GetUserByID(ctx, who); err == nil && u.Email != "" {
		who = u.Email
	}
	d := pdf.New("Financial statements")
	d.Title("Financial statements")
	d.Line(fmt.Sprintf("Period %s to %s - amounts in currency minor units", from, to))
	d.Space()
	d.Heading("Income statement (statement of activities)")
	for _, b := range income {
		d.Line(fmt.Sprintf("%s %-28s %8s %12d", b.Code, b.Name, b.Currency, -b.Balance))
	}
	if len(income) == 0 {
		d.Line("(no activity in period)")
	}
	d.Space()
	d.Heading(fmt.Sprintf("Balance sheet (statement of financial position) as of %s", to))
	for _, b := range position {
		d.Line(fmt.Sprintf("%s %-28s %8s %12d", b.Code, b.Name, b.Currency, b.Balance))
	}
	d.Space()
	d.Heading("Receivables aging")
	for _, a := range aging {
		d.Line(fmt.Sprintf("%s %-8s %4d invoices %12d", a.Currency, a.Bucket, a.Invoices, a.Amount))
	}
	if len(aging) == 0 {
		d.Line("(no open receivables)")
	}
	d.Space()
	d.Heading("Integrity")
	d.Line(fmt.Sprintf("Subledger reconciliation: %d mismatch(es)", len(rec)))
	d.Line(fmt.Sprintf("Audit chain: intact=%v head=%s", chainOK, chainHead))
	d.Watermark(fmt.Sprintf("Exported by %s - %s", who, time.Now().UTC().Format("2006-01-02 15:04 UTC")))
	statementsPDF, pdfDigest := d.Finalize()

	// --- assemble the zip ---
	fundsRows := [][]string{{"fund", "gl_code", "approvals_required", "signers", "currency", "balance_minor"}}
	for _, f := range funds {
		signers := ""
		for i, s := range f.Signers {
			if i > 0 {
				signers += "; "
			}
			signers += s.Name
		}
		if len(f.Balances) == 0 {
			fundsRows = append(fundsRows, []string{csvSafe(f.Name), f.CashAccountCode, fmt.Sprint(f.ApprovalsRequired), csvSafe(signers), "", "0"})
		}
		for _, b := range f.Balances {
			fundsRows = append(fundsRows, []string{csvSafe(f.Name), f.CashAccountCode, fmt.Sprint(f.ApprovalsRequired), csvSafe(signers), b.Currency, fmt.Sprint(b.Balance)})
		}
	}
	agingRows := [][]string{{"currency", "bucket", "invoices", "amount_minor"}}
	for _, a := range aging {
		agingRows = append(agingRows, []string{a.Currency, a.Bucket, fmt.Sprint(a.Invoices), fmt.Sprint(a.Amount)})
	}

	// Accounts payable (optional): all bills, and open-bill aging as of `to`.
	var billsRows, apAgingRows [][]string
	if h.bills != nil {
		bills, err := h.bills.List(ctx, "", 5000)
		if err != nil {
			writeError(w, 500, "query error", "internal_error")
			return
		}
		billsRows = [][]string{{"vendor", "amount_minor", "currency", "expense_code", "expense_name", "bill_date", "due_date", "status", "paid_at", "memo"}}
		for _, b := range bills {
			due, paid, memo := "", "", ""
			if b.DueDate != nil {
				due = b.DueDate.Format("2006-01-02")
			}
			if b.PaidAt != nil {
				paid = b.PaidAt.Format("2006-01-02")
			}
			if b.Memo != nil {
				memo = *b.Memo
			}
			billsRows = append(billsRows, []string{csvSafe(b.ContactName), fmt.Sprint(b.Amount), b.Currency,
				b.ExpenseAccountCode, csvSafe(b.ExpenseAccountName), b.BillDate.Format("2006-01-02"), due, b.Status, paid, csvSafe(memo)})
		}
		apAging, err := h.bills.APAging(ctx, to)
		if err != nil {
			writeError(w, 500, "query error", "internal_error")
			return
		}
		apAgingRows = [][]string{{"currency", "bucket", "bills", "amount_minor"}}
		for _, a := range apAging {
			apAgingRows = append(apAgingRows, []string{a.Currency, a.Bucket, fmt.Sprint(a.Invoices), fmt.Sprint(a.Amount)})
		}
	}
	evidence := fmt.Sprintf(`Quorum CPA export pack
Period: %s to %s
Generated: %s by %s

Integrity:
- statements.pdf embeds its own SHA-256 (verify offline with ops/verify-pdf-export.py); digest: %s
- subledger reconciliation mismatches: %d (0 = GL and dues subledger agree)
- audit hash chain: intact=%v head=%s
- this pack's EXPORT audit entry carries the ZIP's SHA-256; compare with your download.

All amounts are integer minor units in the row's currency.
Verification tooling ships in the repository under ops/.
`, from, to, time.Now().UTC().Format(time.RFC3339), who, pdfDigest, len(rec), chainOK, chainHead)

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	files := []struct {
		name string
		data []byte
	}{
		{"statements.pdf", statementsPDF},
		{"trial-balance.csv", csvBytes(balancesCSV("", tb))},
		{"general-ledger.csv", csvBytes(ledger)},
		{"income-statement-accrual.csv", csvBytes(balancesCSV("", income))},
		{"income-statement-cash.csv", csvBytes(balancesCSV("", cashIncome))},
		{"purchases.csv", csvBytes(purchases)},
		{"posting-rules.csv", csvBytes(rulesCSV(rules))},
		{"balance-sheet.csv", csvBytes(balancesCSV("", position))},
		{"funds.csv", csvBytes(fundsRows)},
		{"ar-aging.csv", csvBytes(agingRows)},
		{"EVIDENCE.txt", []byte(evidence)},
	}
	if billsRows != nil {
		files = append(files,
			struct {
				name string
				data []byte
			}{"bills.csv", csvBytes(billsRows)},
			struct {
				name string
				data []byte
			}{"ap-aging.csv", csvBytes(apAgingRows)})
	}
	for _, f := range files {
		fw, err := zw.Create(f.name)
		if err == nil {
			_, err = fw.Write(f.data)
		}
		if err != nil {
			writeError(w, 500, "zip error", "internal_error")
			return
		}
	}
	if err := zw.Close(); err != nil {
		writeError(w, 500, "zip error", "internal_error")
		return
	}

	sum := sha256.Sum256(zbuf.Bytes())
	digest := hex.EncodeToString(sum[:])
	auditExport(r, h.audit, "accounting-pack.zip", map[string]any{
		"from": from, "to": to, "sha256": digest, "files": len(files), "chain_intact": chainOK,
	})
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "quorum-accounting-"+from+"-"+to+".zip"))
	w.Header().Set("X-Document-SHA256", digest)
	_, _ = w.Write(zbuf.Bytes())
}
