package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
)

// ---- mockOrgFeaturesRepo ----

type mockOrgFeaturesRepo struct {
	AddOfficeTermFn       func(ctx context.Context, memberID, title, startedOn string) (*model.OfficeTerm, error)
	EndOfficeTermFn       func(ctx context.Context, id, endedOn string) error
	ListOfficeTermsFn     func(ctx context.Context, current bool) ([]model.OfficeTerm, error)
	CreateCommitteeFn     func(ctx context.Context, name string, purpose, chairID *string) (*model.Committee, error)
	GetCommitteeFn        func(ctx context.Context, id string) (*model.Committee, error)
	ListCommitteesFn      func(ctx context.Context) ([]model.Committee, error)
	UpdateCommitteeFn     func(ctx context.Context, id, name string, purpose, chairID *string) error
	DeleteCommitteeFn     func(ctx context.Context, id string) error
	SetCommitteeMembersFn func(ctx context.Context, id string, memberIDs []string) error
	AddRecusalFn          func(ctx context.Context, subjectType, subjectID, memberID, reason string) error
	ListRecusalsFn        func(ctx context.Context, subjectType, subjectID string) ([]model.Recusal, error)
	CreateJoinRequestFn   func(ctx context.Context, name, email, message string) error
	ListJoinRequestsFn    func(ctx context.Context, status string, limit int) ([]model.JoinRequest, error)
	ApproveJoinRequestFn  func(ctx context.Context, id, tier, resolvedBy string) (string, error)
	RejectJoinRequestFn   func(ctx context.Context, id, resolvedBy string) error
}

func (m *mockOrgFeaturesRepo) AddOfficeTerm(ctx context.Context, memberID, title, startedOn string) (*model.OfficeTerm, error) {
	return m.AddOfficeTermFn(ctx, memberID, title, startedOn)
}
func (m *mockOrgFeaturesRepo) EndOfficeTerm(ctx context.Context, id, endedOn string) error {
	return m.EndOfficeTermFn(ctx, id, endedOn)
}
func (m *mockOrgFeaturesRepo) ListOfficeTerms(ctx context.Context, current bool) ([]model.OfficeTerm, error) {
	return m.ListOfficeTermsFn(ctx, current)
}
func (m *mockOrgFeaturesRepo) CreateCommittee(ctx context.Context, name string, purpose, chairID *string) (*model.Committee, error) {
	return m.CreateCommitteeFn(ctx, name, purpose, chairID)
}
func (m *mockOrgFeaturesRepo) GetCommittee(ctx context.Context, id string) (*model.Committee, error) {
	return m.GetCommitteeFn(ctx, id)
}
func (m *mockOrgFeaturesRepo) ListCommittees(ctx context.Context) ([]model.Committee, error) {
	return m.ListCommitteesFn(ctx)
}
func (m *mockOrgFeaturesRepo) UpdateCommittee(ctx context.Context, id, name string, purpose, chairID *string) error {
	return m.UpdateCommitteeFn(ctx, id, name, purpose, chairID)
}
func (m *mockOrgFeaturesRepo) DeleteCommittee(ctx context.Context, id string) error {
	return m.DeleteCommitteeFn(ctx, id)
}
func (m *mockOrgFeaturesRepo) SetCommitteeMembers(ctx context.Context, id string, memberIDs []string) error {
	return m.SetCommitteeMembersFn(ctx, id, memberIDs)
}
func (m *mockOrgFeaturesRepo) AddRecusal(ctx context.Context, subjectType, subjectID, memberID, reason string) error {
	return m.AddRecusalFn(ctx, subjectType, subjectID, memberID, reason)
}
func (m *mockOrgFeaturesRepo) ListRecusals(ctx context.Context, subjectType, subjectID string) ([]model.Recusal, error) {
	return m.ListRecusalsFn(ctx, subjectType, subjectID)
}
func (m *mockOrgFeaturesRepo) CreateJoinRequest(ctx context.Context, name, email, message string) error {
	return m.CreateJoinRequestFn(ctx, name, email, message)
}
func (m *mockOrgFeaturesRepo) ListJoinRequests(ctx context.Context, status string, limit int) ([]model.JoinRequest, error) {
	return m.ListJoinRequestsFn(ctx, status, limit)
}
func (m *mockOrgFeaturesRepo) ApproveJoinRequest(ctx context.Context, id, tier, resolvedBy string) (string, error) {
	return m.ApproveJoinRequestFn(ctx, id, tier, resolvedBy)
}
func (m *mockOrgFeaturesRepo) RejectJoinRequest(ctx context.Context, id, resolvedBy string) error {
	return m.RejectJoinRequestFn(ctx, id, resolvedBy)
}

// ---- office terms ----

func TestAddOfficeTerm_Success(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{
		AddOfficeTermFn: func(_ context.Context, memberID, title, _ string) (*model.OfficeTerm, error) {
			return &model.OfficeTerm{ID: "t1", MemberID: memberID, Title: title, MemberName: "Alice"}, nil
		},
	})
	body := `{"member_id":"` + testUUID + `","title":"Treasurer"}`
	req := httptest.NewRequest("POST", "/office-terms", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.AddOfficeTerm(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestAddOfficeTerm_BadMember(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := httptest.NewRequest("POST", "/office-terms", strings.NewReader(`{"member_id":"nope","title":"X"}`))
	rr := httptest.NewRecorder()
	h.AddOfficeTerm(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestAddOfficeTerm_EmptyTitle(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := httptest.NewRequest("POST", "/office-terms", strings.NewReader(`{"member_id":"`+testUUID+`","title":"  "}`))
	rr := httptest.NewRecorder()
	h.AddOfficeTerm(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- committees ----

func TestCreateCommittee_Success(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{
		CreateCommitteeFn: func(_ context.Context, name string, _, _ *string) (*model.Committee, error) {
			return &model.Committee{ID: "c1", Name: name}, nil
		},
	})
	req := httptest.NewRequest("POST", "/committees", strings.NewReader(`{"name":"Finance"}`))
	rr := httptest.NewRecorder()
	h.CreateCommittee(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestCreateCommittee_BadChair(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := httptest.NewRequest("POST", "/committees", strings.NewReader(`{"name":"Finance","chair_id":"not-a-uuid"}`))
	rr := httptest.NewRecorder()
	h.CreateCommittee(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestSetCommitteeMembers_BadMember(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := chiRequest("PUT", "/committees/"+testUUID+"/members", `{"member_ids":["bad"]}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.SetCommitteeMembers(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- recusals ----

func TestAddRecusal_Success(t *testing.T) {
	var gotType, gotSubject, gotMember string
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{
		AddRecusalFn: func(_ context.Context, subjectType, subjectID, memberID, _ string) error {
			gotType, gotSubject, gotMember = subjectType, subjectID, memberID
			return nil
		},
	})
	req := chiRequest("POST", "/recusals/"+testUUID, `{"type":"motion","reason":"conflict"}`, map[string]string{"id": testUUID})
	req = withCtxUserMember(req, "u1", "member", "mem-1")
	rr := httptest.NewRecorder()
	h.AddRecusal(rr, req)
	if rr.Code != 204 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if gotType != "motion" || gotSubject != testUUID || gotMember != "mem-1" {
		t.Errorf("passed through: type=%q subject=%q member=%q", gotType, gotSubject, gotMember)
	}
}

func TestAddRecusal_NoMemberLink(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := chiRequest("POST", "/recusals/"+testUUID, `{"type":"motion"}`, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u1", "admin") // no member_id in context
	rr := httptest.NewRecorder()
	h.AddRecusal(rr, req)
	if rr.Code != 403 {
		t.Errorf("status: got %d, want 403 when login has no member link", rr.Code)
	}
}

func TestAddRecusal_BadType(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := chiRequest("POST", "/recusals/"+testUUID, `{"type":"banana"}`, map[string]string{"id": testUUID})
	req = withCtxUserMember(req, "u1", "member", "mem-1")
	rr := httptest.NewRecorder()
	h.AddRecusal(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid subject type", rr.Code)
	}
}

// ---- join requests ----

func TestCreateJoinRequest_Public(t *testing.T) {
	var gotName, gotEmail string
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{
		CreateJoinRequestFn: func(_ context.Context, name, email, _ string) error {
			gotName, gotEmail = name, email
			return nil
		},
	})
	req := httptest.NewRequest("POST", "/public/join-request", strings.NewReader(`{"name":"Jo Applicant","email":"jo@example.com","message":"Hi"}`))
	rr := httptest.NewRecorder()
	h.CreateJoinRequest(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if gotName != "Jo Applicant" || gotEmail != "jo@example.com" {
		t.Errorf("passed through: name=%q email=%q", gotName, gotEmail)
	}
}

func TestCreateJoinRequest_BadEmail(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := httptest.NewRequest("POST", "/public/join-request", strings.NewReader(`{"name":"Jo","email":"not-an-email"}`))
	rr := httptest.NewRecorder()
	h.CreateJoinRequest(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid email", rr.Code)
	}
}

func TestCreateJoinRequest_EmptyName(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := httptest.NewRequest("POST", "/public/join-request", strings.NewReader(`{"name":"  ","email":"a@b.com"}`))
	rr := httptest.NewRecorder()
	h.CreateJoinRequest(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for empty name", rr.Code)
	}
}

func TestApproveJoinRequest_Success(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{
		ApproveJoinRequestFn: func(_ context.Context, id, _, resolvedBy string) (string, error) {
			if resolvedBy != "u-approver" {
				t.Errorf("resolvedBy: got %q", resolvedBy)
			}
			return "new-member-id", nil
		},
	})
	req := chiRequest("POST", "/join-requests/"+testUUID+"/approve", `{}`, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u-approver", "officer")
	rr := httptest.NewRecorder()
	h.ApproveJoinRequest(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["member_id"] != "new-member-id" {
		t.Errorf("member_id: got %q", got["member_id"])
	}
}

func TestListJoinRequests_BadStatus(t *testing.T) {
	h := NewOrgFeaturesHandler(&mockOrgFeaturesRepo{})
	req := httptest.NewRequest("GET", "/join-requests?status=weird", nil)
	rr := httptest.NewRecorder()
	h.ListJoinRequests(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad status filter", rr.Code)
	}
}
