package main

import (
	"context"
	"fmt"

	"quorum/internal/model"
)

// digestGL is the slice of *repo.GLRepo the report digest reads.
type digestGL interface {
	ARAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error)
	StatementCash(ctx context.Context, from, to string) ([]model.GLBalance, error)
}

// digestBills is the slice of *repo.BillsRepo the digest reads.
type digestBills interface {
	APAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error)
}

// buildReportDigest renders one subscribed report as a plain-text email body.
// Kept out of main() so the multi-line formatting doesn't fight inline string
// escaping. Query failures degrade to an empty section rather than no email.
func buildReportDigest(ctx context.Context, report, cadence, asOf, yearStart string, gl digestGL, bills digestBills) (subject, body string) {
	footer := "\n\nManage these emails under Settings in Quorum.\n"
	switch report {
	case "ar_aging":
		rows, _ := gl.ARAging(ctx, asOf)
		subject = "Quorum: receivables aging (" + cadence + ")"
		body = "Receivables aging as of " + asOf + "\n\n"
		for _, a := range rows {
			body += fmt.Sprintf("  %s  %-8s  %d invoices  %s\n", a.Currency, a.Bucket, a.Invoices, model.FormatMoney(a.Amount, a.Currency))
		}
		if len(rows) == 0 {
			body += "  (no open receivables)\n"
		}
	case "ap_aging":
		rows, _ := bills.APAging(ctx, asOf)
		subject = "Quorum: payables aging (" + cadence + ")"
		body = "Payables aging as of " + asOf + "\n\n"
		for _, a := range rows {
			body += fmt.Sprintf("  %s  %-8s  %d bills  %s\n", a.Currency, a.Bucket, a.Invoices, model.FormatMoney(a.Amount, a.Currency))
		}
		if len(rows) == 0 {
			body += "  (no open bills)\n"
		}
	case "income_statement":
		rows, _ := gl.StatementCash(ctx, yearStart, asOf)
		subject = "Quorum: income statement (" + cadence + ")"
		body = "Income statement (cash basis), " + yearStart + " to " + asOf + "\n\n"
		for _, b := range rows {
			if b.Type == "income" || b.Type == "expense" {
				body += fmt.Sprintf("  %s  %-24s  %s %s\n", b.Code, b.Name, model.FormatMoney(-b.Balance, b.Currency), b.Currency)
			}
		}
	default:
		subject = "Quorum report"
	}
	return subject, body + footer
}
