package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ---- payment reports ----

type mockPayReports struct {
	create  func(ctx context.Context, invoiceID, memberID, method, reference, note string) (*model.PaymentReport, error)
	resolve func(ctx context.Context, id, status, by string) (string, error)
	confirm func(ctx context.Context, id, by string) (*repo.ConfirmOutcome, error)
}

func (m *mockPayReports) Create(ctx context.Context, inv, mem, method, ref, note string) (*model.PaymentReport, error) {
	if m.create != nil {
		return m.create(ctx, inv, mem, method, ref, note)
	}
	return &model.PaymentReport{ID: "pr1", InvoiceID: inv}, nil
}
func (m *mockPayReports) ListPending(ctx context.Context, limit int) ([]model.PaymentReport, error) {
	return []model.PaymentReport{}, nil
}
func (m *mockPayReports) PendingCount(ctx context.Context) (int, error) { return 0, nil }
func (m *mockPayReports) Resolve(ctx context.Context, id, status, by string) (string, error) {
	if m.resolve != nil {
		return m.resolve(ctx, id, status, by)
	}
	return "inv1", nil
}
func (m *mockPayReports) ConfirmAndPost(ctx context.Context, id, by string) (*repo.ConfirmOutcome, error) {
	if m.confirm != nil {
		return m.confirm(ctx, id, by)
	}
	return &repo.ConfirmOutcome{InvoiceID: "inv1", Posted: true, AmountMinor: 5000}, nil
}

type mockPRDues struct {
	inv *model.DuesInvoice
}

func (m *mockPRDues) GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error) {
	if m.inv != nil {
		return m.inv, nil
	}
	return &model.DuesInvoice{ID: id, MemberID: "member-1", AmountMinor: 5000, Currency: "USD", Status: "pending"}, nil
}

const testInvID = "11111111-1111-1111-1111-111111111111"

// A member can only report payment on their own invoice.
func TestPaymentReport_OwnInvoiceOnly(t *testing.T) {
	h := NewPaymentReportsHandler(&mockPayReports{}, &mockPRDues{
		inv: &model.DuesInvoice{ID: testInvID, MemberID: "member-1", Status: "pending"},
	})
	req := reqWithParam("POST", "/dues/"+testInvID+"/report-payment", `{"method":"zelle"}`, map[string]string{"id": testInvID})
	req = withCtxUserMember(req, "u-other", "member", "member-2") // not the owner
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("stranger report: got %d, want 403 (%s)", rr.Code, rr.Body)
	}
}

// Method is required.
func TestPaymentReport_MethodRequired(t *testing.T) {
	h := NewPaymentReportsHandler(&mockPayReports{}, &mockPRDues{
		inv: &model.DuesInvoice{ID: testInvID, MemberID: "member-1", Status: "pending"},
	})
	req := reqWithParam("POST", "/dues/"+testInvID+"/report-payment", `{"method":""}`, map[string]string{"id": testInvID})
	req = withCtxUserMember(req, "u", "member", "member-1")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("no method: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
}

// The owner's report succeeds (201).
func TestPaymentReport_OwnerSucceeds(t *testing.T) {
	h := NewPaymentReportsHandler(&mockPayReports{}, &mockPRDues{
		inv: &model.DuesInvoice{ID: testInvID, MemberID: "member-1", Status: "pending"},
	})
	req := reqWithParam("POST", "/dues/"+testInvID+"/report-payment", `{"method":"check"}`, map[string]string{"id": testInvID})
	req = withCtxUserMember(req, "u", "member", "member-1")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner report: got %d, want 201 (%s)", rr.Code, rr.Body)
	}
}

// Confirming an already-resolved report is a 409.
func TestPaymentReport_ConfirmGone409(t *testing.T) {
	h := NewPaymentReportsHandler(&mockPayReports{
		confirm: func(context.Context, string, string) (*repo.ConfirmOutcome, error) { return nil, pgx.ErrNoRows },
	}, &mockPRDues{})
	req := reqWithParam("POST", "/payment-reports/"+testInvID+"/confirm", "", map[string]string{"id": testInvID})
	req = withCtxUser(req, "u-officer", "officer")
	rr := httptest.NewRecorder()
	h.Confirm(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("confirm resolved: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Confirming a report on a waived invoice confirms but does not post.
func TestPaymentReport_ConfirmWaivedNotPosted(t *testing.T) {
	h := NewPaymentReportsHandler(&mockPayReports{
		confirm: func(context.Context, string, string) (*repo.ConfirmOutcome, error) {
			return &repo.ConfirmOutcome{InvoiceID: "inv-waived", Reason: "invoice is waived"}, nil
		},
	}, &mockPRDues{inv: &model.DuesInvoice{ID: "inv-waived", MemberID: "m", Status: "waived"}})
	req := reqWithParam("POST", "/payment-reports/"+testInvID+"/confirm", "", map[string]string{"id": testInvID})
	req = withCtxUser(req, "u-officer", "officer")
	rr := httptest.NewRecorder()
	h.Confirm(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"posted":false`) {
		t.Fatalf("confirm waived: got %d body=%s, want 200 with posted:false", rr.Code, rr.Body)
	}
}

// ---- report subscriptions ----

type mockReportSubs struct {
	set func(ctx context.Context, u, r, c string) error
}

func (m *mockReportSubs) ForUser(ctx context.Context, u string) ([]model.ReportSubscription, error) {
	return nil, nil
}
func (m *mockReportSubs) Set(ctx context.Context, u, r, c string) error {
	if m.set != nil {
		return m.set(ctx, u, r, c)
	}
	return nil
}
func (m *mockReportSubs) Delete(ctx context.Context, u, r string) error { return nil }

func TestReportSubs_Validation(t *testing.T) {
	h := NewReportSubsHandler(&mockReportSubs{})
	// unknown report
	req := httptest.NewRequest("PUT", "/me/report-subscriptions", strings.NewReader(`{"report":"nope","cadence":"weekly"}`))
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad report: got %d, want 400", rr.Code)
	}
	// bad cadence
	req = httptest.NewRequest("PUT", "/me/report-subscriptions", strings.NewReader(`{"report":"ar_aging","cadence":"hourly"}`))
	req = withCtxUser(req, "u", "officer")
	rr = httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad cadence: got %d, want 400", rr.Code)
	}
	// valid
	req = httptest.NewRequest("PUT", "/me/report-subscriptions", strings.NewReader(`{"report":"ar_aging","cadence":"weekly"}`))
	req = withCtxUser(req, "u", "officer")
	rr = httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid sub: got %d, want 200 (%s)", rr.Code, rr.Body)
	}
}

// ---- CSV member import ----

type mockImporter struct {
	created int
}

func (m *mockImporter) ExistingEmails(ctx context.Context) (map[string]bool, error) {
	return map[string]bool{"dup@example.com": true}, nil
}
func (m *mockImporter) BatchCreate(ctx context.Context, members []*model.Member) (int, error) {
	m.created = len(members)
	return len(members), nil
}

func TestMemberImport_DryRunAndCommit(t *testing.T) {
	csv := "name,email,status\n" +
		"Valid One,v1@example.com,active\n" +
		"Dupe,dup@example.com,active\n" +
		",noname@example.com,active\n" + // invalid: no name
		"Bad Status,bs@example.com,ghost\n" // invalid status

	// Dry run: counts only, nothing created.
	imp := &mockImporter{}
	h := NewMemberImportHandler(imp)
	req := httptest.NewRequest("POST", "/members/import", strings.NewReader(csv))
	req.Header.Set("Content-Type", "text/csv")
	req = withCtxUser(req, "u-admin", "admin")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("dry run: got %d (%s)", rr.Code, body)
	}
	if !strings.Contains(body, `"new":1`) || !strings.Contains(body, `"duplicate":1`) || !strings.Contains(body, `"invalid":2`) {
		t.Fatalf("dry-run counts wrong: %s", body)
	}
	if imp.created != 0 {
		t.Fatalf("dry run created %d members, want 0", imp.created)
	}

	// Commit: the one valid row is inserted.
	imp2 := &mockImporter{}
	h2 := NewMemberImportHandler(imp2)
	req2 := httptest.NewRequest("POST", "/members/import?commit=true", strings.NewReader(csv))
	req2.Header.Set("Content-Type", "text/csv")
	req2 = withCtxUser(req2, "u-admin", "admin")
	rr2 := httptest.NewRecorder()
	h2.Import(rr2, req2)
	if imp2.created != 1 {
		t.Fatalf("commit created %d, want 1 (%s)", imp2.created, rr2.Body)
	}
}
