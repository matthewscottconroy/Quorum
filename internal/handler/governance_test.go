package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"quorum/internal/model"
	"quorum/internal/repo"
)

const testMeetingID = "33333333-3333-3333-3333-333333333333"
const testMotionID = "44444444-4444-4444-4444-444444444444"
const testMemberID = "66666666-6666-6666-6666-666666666666"

func govHandler(r governanceRepo) *GovernanceHandler { return NewGovernanceHandler(r, testConfig()) }

// ---- Quorum ----

func TestQuorum_ReturnsStatus(t *testing.T) {
	repo := &mockGovernanceRepo{
		ComputeQuorumFn: func(_ context.Context, _ string) (*model.QuorumStatus, error) {
			return &model.QuorumStatus{Mode: "majority", Required: 6, ActiveMembers: 10, PresentCount: 5, ProxiesRepresented: 2, EffectivePresent: 7, Met: true}, nil
		},
	}
	req := reqWithParam("GET", "/meetings/"+testMeetingID+"/quorum", "", map[string]string{"id": testMeetingID})
	rr := httptest.NewRecorder()
	govHandler(repo).Quorum(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var q model.QuorumStatus
	if err := json.NewDecoder(rr.Body).Decode(&q); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !q.Met || q.EffectivePresent != 7 {
		t.Errorf("unexpected quorum: %+v", q)
	}
}

// ---- Settings ----

func TestUpdateSettings_Validation(t *testing.T) {
	h := govHandler(&mockGovernanceRepo{})
	bad := []string{
		`{"quorum_mode":"bogus","default_threshold":"majority"}`,
		`{"quorum_mode":"majority","default_threshold":"bogus"}`,
		`{"quorum_mode":"percent","quorum_value":150,"default_threshold":"majority"}`,
		`{"quorum_mode":"percent","quorum_value":0,"default_threshold":"majority"}`,
	}
	for _, b := range bad {
		rr := httptest.NewRecorder()
		h.UpdateSettings(rr, withCtxUser(httptest.NewRequest("PUT", "/governance/settings", strings.NewReader(b)), "u", "admin"))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", b, rr.Code)
		}
	}
	// Valid.
	rr := httptest.NewRecorder()
	h.UpdateSettings(rr, withCtxUser(httptest.NewRequest("PUT", "/governance/settings", strings.NewReader(`{"quorum_mode":"percent","quorum_value":50,"default_threshold":"two_thirds","proxies_count_toward_quorum":true}`)), "u", "admin"))
	if rr.Code != 200 {
		t.Fatalf("valid settings rejected: %d %s", rr.Code, rr.Body)
	}
}

// ---- Motions ----

func TestCreateMotion_DefaultsThresholdFromSettings(t *testing.T) {
	var gotThreshold string
	repo := &mockGovernanceRepo{
		GetSettingsFn: func(_ context.Context) (*model.GovernanceSettings, error) {
			return &model.GovernanceSettings{DefaultThreshold: "two_thirds", QuorumMode: "majority"}, nil
		},
		CreateMotionFn: func(_ context.Context, m *model.Motion, _ string) (*model.Motion, error) {
			gotThreshold = m.Threshold
			m.ID = testMotionID
			return m, nil
		},
	}
	req := reqWithParam("POST", "/meetings/"+testMeetingID+"/motions", `{"title":"Approve the budget"}`, map[string]string{"id": testMeetingID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).CreateMotion(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if gotThreshold != "two_thirds" {
		t.Errorf("expected threshold defaulted to two_thirds, got %q", gotThreshold)
	}
}

func TestCreateMotion_RequiresTitle(t *testing.T) {
	req := reqWithParam("POST", "/meetings/"+testMeetingID+"/motions", `{"title":""}`, map[string]string{"id": testMeetingID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(&mockGovernanceRepo{}).CreateMotion(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestOpenMotion_RequiresSeconder(t *testing.T) {
	repo := &mockGovernanceRepo{
		GetMotionFn: func(_ context.Context, _ string) (*model.Motion, error) {
			return &model.Motion{ID: testMotionID, Status: "draft", SeconderID: nil}, nil
		},
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/open", "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).OpenMotion(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (needs seconder), got %d: %s", rr.Code, rr.Body)
	}
}

func TestCloseMotion_AutoDecidesFromTally(t *testing.T) {
	var closedStatus string
	repo := &mockGovernanceRepo{
		CloseAndDecideFn: func(_ context.Context, id, requested string) (*model.Motion, string, error) {
			if requested != "" {
				t.Fatalf("auto-decide must pass an empty request, got %q", requested)
			}
			closedStatus = "carried" // the repo decides under the motion lock
			return &model.Motion{ID: id, Status: "carried"}, "carried", nil
		},
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/close", "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).CloseMotion(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if closedStatus != "carried" {
		t.Errorf("expected auto-decide to carried, got %q", closedStatus)
	}
}

func TestCloseMotion_FailedWhenTallyShort(t *testing.T) {
	var closedStatus string
	repo := &mockGovernanceRepo{
		CloseAndDecideFn: func(_ context.Context, id, _ string) (*model.Motion, string, error) {
			closedStatus = "failed"
			return &model.Motion{ID: id, Status: "failed"}, "failed", nil
		},
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/close", "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).CloseMotion(rr, req)
	if closedStatus != "failed" {
		t.Errorf("expected failed, got %q", closedStatus)
	}
}

func TestCloseMotion_ExplicitTabled(t *testing.T) {
	var closedStatus string
	repo := &mockGovernanceRepo{
		CloseAndDecideFn: func(_ context.Context, id, requested string) (*model.Motion, string, error) {
			closedStatus = requested
			return &model.Motion{ID: id, Status: requested}, requested, nil
		},
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/close", `{"status":"tabled"}`, map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).CloseMotion(rr, req)
	if closedStatus != "tabled" {
		t.Errorf("explicit tabled ignored; got %q", closedStatus)
	}
}

// ---- Voting ----

func TestCastVote_RequiresMemberLink(t *testing.T) {
	// Officer with no linked member cannot self-vote.
	req := reqWithParam("POST", "/motions/"+testMotionID+"/vote", `{"choice":"for"}`, map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer") // no member id in ctx
	rr := httptest.NewRecorder()
	govHandler(&mockGovernanceRepo{}).CastVote(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (no member link), got %d: %s", rr.Code, rr.Body)
	}
}

func TestCastVote_OnlyWhenOpen(t *testing.T) {
	repo := &mockGovernanceRepo{
		MotionStatusFn: func(_ context.Context, _ string) (string, string, error) { return "draft", testMeetingID, nil },
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/vote", `{"choice":"for"}`, map[string]string{"id": testMotionID})
	req = withCtxUserMember(req, "u", "member", "member-123")
	rr := httptest.NewRecorder()
	govHandler(repo).CastVote(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 (voting not open), got %d: %s", rr.Code, rr.Body)
	}
}

func TestCastVote_Success(t *testing.T) {
	var votedMember, votedChoice string
	repo := &mockGovernanceRepo{
		MotionStatusFn: func(_ context.Context, _ string) (string, string, error) { return "open", testMeetingID, nil },
		CastVoteFn: func(_ context.Context, _, memberID, choice string, _ bool, _ string) error {
			votedMember, votedChoice = memberID, choice
			return nil
		},
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/vote", `{"choice":"against"}`, map[string]string{"id": testMotionID})
	req = withCtxUserMember(req, "u", "member", "member-123")
	rr := httptest.NewRecorder()
	govHandler(repo).CastVote(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if votedMember != "member-123" || votedChoice != "against" {
		t.Errorf("wrong vote recorded: member=%q choice=%q", votedMember, votedChoice)
	}
}

func TestCastVote_InvalidChoice(t *testing.T) {
	req := reqWithParam("POST", "/motions/"+testMotionID+"/vote", `{"choice":"maybe"}`, map[string]string{"id": testMotionID})
	req = withCtxUserMember(req, "u", "member", "member-123")
	rr := httptest.NewRecorder()
	govHandler(&mockGovernanceRepo{}).CastVote(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// ---- Proxies ----

func TestCreateProxy_RejectsSelfProxy(t *testing.T) {
	body := `{"grantor_id":"` + testMemberID + `","holder_id":"` + testMemberID + `"}`
	req := reqWithParam("POST", "/meetings/"+testMeetingID+"/proxies", body, map[string]string{"id": testMeetingID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(&mockGovernanceRepo{}).CreateProxy(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-proxy, got %d: %s", rr.Code, rr.Body)
	}
}

func TestCreateProxy_DuplicateConflict(t *testing.T) {
	repo := &mockGovernanceRepo{
		CreateProxyFn: func(_ context.Context, _, _, _ string) (*model.MeetingProxy, error) {
			// Simulate the unique-index violation surfaced by the repo.
			return nil, &pgconn.PgError{Code: "23505"}
		},
	}
	holder := "55555555-5555-5555-5555-555555555555"
	body := `{"grantor_id":"` + testMemberID + `","holder_id":"` + holder + `"}`
	req := reqWithParam("POST", "/meetings/"+testMeetingID+"/proxies", body, map[string]string{"id": testMeetingID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).CreateProxy(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body)
	}
}

func TestGetMotion_NotFound(t *testing.T) {
	repo := &mockGovernanceRepo{
		GetMotionFn: func(_ context.Context, _ string) (*model.Motion, error) { return nil, pgx.ErrNoRows },
	}
	req := reqWithParam("GET", "/motions/"+testMotionID, "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "member")
	rr := httptest.NewRecorder()
	govHandler(repo).GetMotion(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---- Async ballots ----

func TestSubmitBallot_RecordsVoteAndConsumes(t *testing.T) {
	var gotChoice string
	repo := &mockGovernanceRepo{
		ConsumeBallotAndVoteFn: func(_ context.Context, _, choice string) (string, error) {
			gotChoice = choice
			return testMotionID, nil
		},
	}
	req := httptest.NewRequest("POST", "/public/ballot", strings.NewReader(`{"token":"abc","choice":"for"}`))
	rr := httptest.NewRecorder()
	govHandler(repo).SubmitBallot(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if gotChoice != "for" {
		t.Errorf("ballot not recorded correctly: choice=%q", gotChoice)
	}
}

func TestSubmitBallot_ClosedMotionRejected(t *testing.T) {
	repo := &mockGovernanceRepo{
		ConsumeBallotAndVoteFn: func(_ context.Context, _, _ string) (string, error) {
			return "", repo.ErrMotionNotOpen
		},
	}
	req := httptest.NewRequest("POST", "/public/ballot", strings.NewReader(`{"token":"abc","choice":"for"}`))
	rr := httptest.NewRecorder()
	govHandler(repo).SubmitBallot(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for closed motion, got %d: %s", rr.Code, rr.Body)
	}
}

func TestSubmitBallot_InvalidToken(t *testing.T) {
	repo := &mockGovernanceRepo{
		ConsumeBallotAndVoteFn: func(_ context.Context, _, _ string) (string, error) { return "", pgx.ErrNoRows },
	}
	req := httptest.NewRequest("POST", "/public/ballot", strings.NewReader(`{"token":"bad","choice":"for"}`))
	rr := httptest.NewRecorder()
	govHandler(repo).SubmitBallot(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestGetBallot_InvalidToken(t *testing.T) {
	repo := &mockGovernanceRepo{
		GetBallotContextFn: func(_ context.Context, _ string) (*model.BallotContext, error) { return nil, pgx.ErrNoRows },
	}
	req := httptest.NewRequest("GET", "/public/ballot?token=bad", nil)
	rr := httptest.NewRecorder()
	govHandler(repo).GetBallot(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// ---- New integrity guards ----

func TestCloseMotion_RejectsNeverOpened(t *testing.T) {
	repo := &mockGovernanceRepo{
		CloseAndDecideFn: func(_ context.Context, _, _ string) (*model.Motion, string, error) {
			return nil, "", repo.ErrMotionNotOpen // never opened: repo refuses under lock
		},
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/close", "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).CloseMotion(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 closing a never-opened motion, got %d: %s", rr.Code, rr.Body)
	}
}

func TestUpdateMotion_ThresholdFrozenWhenOpen(t *testing.T) {
	repo := &mockGovernanceRepo{
		MotionStatusFn: func(_ context.Context, _ string) (string, string, error) { return "open", testMeetingID, nil },
	}
	req := reqWithParam("PATCH", "/motions/"+testMotionID, `{"threshold":"unanimous"}`, map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).UpdateMotion(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 changing threshold on an open motion, got %d: %s", rr.Code, rr.Body)
	}
}

func TestCastVote_RejectsInactiveMember(t *testing.T) {
	repo := &mockGovernanceRepo{
		MotionStatusFn:   func(_ context.Context, _ string) (string, string, error) { return "open", testMeetingID, nil },
		MemberIsActiveFn: func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/vote", `{"choice":"for"}`, map[string]string{"id": testMotionID})
	req = withCtxUserMember(req, "u", "member", "member-123")
	rr := httptest.NewRecorder()
	govHandler(repo).CastVote(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for inactive member, got %d: %s", rr.Code, rr.Body)
	}
}

func TestRecordVote_Success(t *testing.T) {
	var gotChoice string
	repo := &mockGovernanceRepo{
		MotionStatusFn: func(_ context.Context, _ string) (string, string, error) { return "open", testMeetingID, nil },
		CastVoteFn: func(_ context.Context, _, _, choice string, _ bool, _ string) error {
			gotChoice = choice
			return nil
		},
	}
	body := `{"member_id":"` + testMemberID + `","choice":"against","is_proxy":true}`
	req := reqWithParam("POST", "/motions/"+testMotionID+"/votes", body, map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo).RecordVote(rr, req)
	if rr.Code != http.StatusNoContent || gotChoice != "against" {
		t.Fatalf("record vote: code=%d choice=%q", rr.Code, gotChoice)
	}
}

func TestSendBallots_NoMailer(t *testing.T) {
	req := reqWithParam("POST", "/motions/"+testMotionID+"/ballots", "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(&mockGovernanceRepo{}).SendBallots(rr, req) // no mailer set
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without a mailer, got %d", rr.Code)
	}
}

// A decided motion is governance history; deleting it must be refused so the
// record (and its cascaded votes) cannot be falsified after the fact.
func TestDeleteMotion_RefusesDecidedMotion(t *testing.T) {
	deleted := false
	h := govHandler(&mockGovernanceRepo{
		GetMotionFn: func(_ context.Context, _ string) (*model.Motion, error) {
			return &model.Motion{ID: "mo1", Title: "Budget 2027", Status: "carried"}, nil
		},
		DeleteMotionFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/motions/mo1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.DeleteMotion(rr, withCtxUser(req, "u", "officer"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting a carried motion, got %d: %s", rr.Code, rr.Body)
	}
	if deleted {
		t.Fatal("a decided motion must never reach the repo delete")
	}
}

// Draft motions are not yet part of the record and may still be deleted.
func TestDeleteMotion_AllowsDraft(t *testing.T) {
	h := govHandler(&mockGovernanceRepo{
		GetMotionFn: func(_ context.Context, _ string) (*model.Motion, error) {
			return &model.Motion{ID: "mo1", Status: "draft"}, nil
		},
		DeleteMotionFn: func(_ context.Context, _ string) error { return nil },
	})
	req := chiRequest("DELETE", "/motions/mo1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.DeleteMotion(rr, withCtxUser(req, "u", "officer"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting a draft, got %d", rr.Code)
	}
}

// Editing WHAT a motion says after ballots may exist is a retroactive
// rewrite: title/detail freeze while voting is open (procedural fields stay
// editable).
func TestUpdateMotion_TextFrozenWhileOpen(t *testing.T) {
	repo2 := &mockGovernanceRepo{
		MotionStatusFn: func(_ context.Context, _ string) (string, string, error) { return "open", testMeetingID, nil },
	}
	req := reqWithParam("PATCH", "/motions/"+testMotionID, `{"title":"Reworded"}`, map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo2).UpdateMotion(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("title edit while open: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Opening a vote on a meeting whose minutes are finalized is refused: the
// outcome could never enter the closed record.
func TestOpenMotion_FinalizedMeetingRefused(t *testing.T) {
	sec := "u-sec"
	repo2 := &mockGovernanceRepo{
		GetMotionFn: func(_ context.Context, _ string) (*model.Motion, error) {
			return &model.Motion{ID: testMotionID, MeetingID: testMeetingID, Status: "seconded", SeconderID: &sec}, nil
		},
		MeetingMinutesFinalizedFn: func(_ context.Context, _ string) (bool, error) { return true, nil },
	}
	req := reqWithParam("POST", "/motions/"+testMotionID+"/open", "", map[string]string{"id": testMotionID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	govHandler(repo2).OpenMotion(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("open on finalized meeting: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}
