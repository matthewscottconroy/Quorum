package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func strptr(s string) *string { return &s }

func TestExportMembersCSV(t *testing.T) {
	mem := &mockMembersRepo{
		ListFn: func(_ context.Context, f repo.MemberFilter) ([]model.Member, int, error) {
			if f.Offset > 0 {
				return nil, 2, nil
			}
			return []model.Member{
				{ID: "m1", DisplayName: "Ada Lovelace", Email: strptr("ada@e.com"), Tier: "standard", Status: "active", DuesStatus: "paid"},
				{ID: "m2", DisplayName: "Grace, Hopper", Tier: "premium", Status: "active", DuesStatus: "pending"},
			}, 2, nil
		},
	}
	h := NewExportHandler(mem, &mockDuesRepo{}, &mockAuthRepo{})
	rr := httptest.NewRecorder()
	h.ExportMembersCSV(rr, httptest.NewRequest("GET", "/export/members.csv", nil))

	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content-type: %s", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "members.csv") {
		t.Errorf("content-disposition: %s", cd)
	}
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(rows) != 3 { // header + 2 members
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0][1] != "display_name" {
		t.Errorf("unexpected header: %v", rows[0])
	}
	// The comma inside "Grace, Hopper" must be properly quoted (round-trips).
	if rows[2][1] != "Grace, Hopper" {
		t.Errorf("comma-bearing name not preserved: %q", rows[2][1])
	}
}

func TestExportDuesCSV_FormatsMoney(t *testing.T) {
	dues := &mockDuesRepo{
		ListInvoicesFn: func(_ context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
			if f.Offset > 0 {
				return nil, 1, nil
			}
			return []model.DuesInvoice{
				{ID: "i1", MemberID: "m1", MemberName: "Ada", AmountMinor: 5000, Currency: "USD", PeriodLabel: "2026", Status: "paid"},
			}, 1, nil
		},
	}
	h := NewExportHandler(&mockMembersRepo{}, dues, &mockAuthRepo{})
	rr := httptest.NewRecorder()
	h.ExportDuesCSV(rr, httptest.NewRequest("GET", "/export/dues.csv", nil))
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV: %v", err)
	}
	// amount_minor=5000, amount=50.00 for USD (exponent 2).
	got := rows[1]
	if got[3] != "5000" || got[4] != "50.00" || got[5] != "USD" {
		t.Errorf("money columns wrong: minor=%q amount=%q currency=%q", got[3], got[4], got[5])
	}
}

func TestExportMyData_ScopedToMember(t *testing.T) {
	memberID := "22222222-2222-2222-2222-222222222222"
	user := testUser(testUserID, "restricted")
	user.MemberID = &memberID

	var invFilterMember, txnFilterMember string
	auth := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, _ string) (*model.User, error) { return user, nil },
	}
	mem := &mockMembersRepo{
		GetFn: func(_ context.Context, id string) (*model.Member, error) {
			return &model.Member{ID: id, DisplayName: "Ada"}, nil
		},
	}
	dues := &mockDuesRepo{
		ListInvoicesFn: func(_ context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
			invFilterMember = f.MemberID
			return nil, 0, nil
		},
		ListTransactionsFn: func(_ context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error) {
			txnFilterMember = f.MemberID
			return nil, 0, nil
		},
	}
	h := NewExportHandler(mem, dues, auth)
	rr := httptest.NewRecorder()
	req := withCtxUserMember(httptest.NewRequest("GET", "/auth/me/export", nil), testUserID, "restricted", memberID)
	h.ExportMyData(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	// Both financial queries must be scoped to the caller's own member id.
	if invFilterMember != memberID || txnFilterMember != memberID {
		t.Errorf("export not scoped to member: inv=%q txn=%q want=%q", invFilterMember, txnFilterMember, memberID)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["account"] == nil || resp["member"] == nil {
		t.Errorf("export should include account and member sections: %v", resp)
	}
}
