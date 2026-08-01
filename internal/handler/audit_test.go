package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuditEntityType(t *testing.T) {
	cases := map[string]string{
		"/api/v1/members/abc":     "members",
		"/api/v1/meetings":        "meetings",
		"/api/v1/dues/invoices/x": "dues",
		"/api/v1/auth/login":      "auth",
	}
	for path, want := range cases {
		r := httptest.NewRequest("POST", path, nil)
		if got := auditEntityType(r); got != want {
			t.Errorf("auditEntityType(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAuditCreatedID(t *testing.T) {
	if got := auditCreatedID([]byte(`{"id":"m-42","name":"x"}`)); got != "m-42" {
		t.Errorf(`created id = %q, want "m-42"`, got)
	}
	if got := auditCreatedID([]byte(`not json`)); got != "" {
		t.Errorf("non-JSON body should yield empty id, got %q", got)
	}
	if got := auditCreatedID([]byte(`{"name":"x"}`)); got != "" {
		t.Errorf("body without id should yield empty id, got %q", got)
	}
}

// A POST create carries no {id} in its URL; the middleware must recover the id
// from the JSON response body and record the route's resource as entity_type.
func TestAuditMiddleware_CapturesCreatedID(t *testing.T) {
	var gotAction, gotType, gotID string
	ar := &mockAuditRepo{LogFn: func(_ context.Context, _, action, entityType, entityID string, _ map[string]any) error {
		gotAction, gotType, gotID = action, entityType, entityID
		return nil
	}}
	created := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new-1","title":"Board"}`))
	})
	h := AuditMiddleware(ar)(created)

	req := httptest.NewRequest("POST", "/api/v1/meetings", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u-1"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotID != "new-1" {
		t.Errorf("entity_id = %q, want the created row id %q", gotID, "new-1")
	}
	if gotType != "meetings" {
		t.Errorf("entity_type = %q, want %q", gotType, "meetings")
	}
	if gotAction != "POST /api/v1/meetings" {
		t.Errorf("action = %q", gotAction)
	}
}

// A failed (non-2xx) request must not be written to the audit log.
func TestAuditMiddleware_SkipsFailures(t *testing.T) {
	logged := false
	ar := &mockAuditRepo{LogFn: func(_ context.Context, _, _, _, _ string, _ map[string]any) error { logged = true; return nil }}
	bad := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	req := httptest.NewRequest("POST", "/api/v1/meetings", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u-1"))
	AuditMiddleware(ar)(bad).ServeHTTP(httptest.NewRecorder(), req)
	if logged {
		t.Error("a 400 response must not be audited")
	}
}

// A 401/403 on a mutating request by a KNOWN user is an insider-threat signal
// and must be recorded as a DENIED action.
func TestAuditMiddleware_RecordsDeniedMutations(t *testing.T) {
	var gotAction string
	ar := &mockAuditRepo{LogFn: func(_ context.Context, _, action, _, _ string, _ map[string]any) error {
		gotAction = action
		return nil
	}}
	forbidden := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	req := httptest.NewRequest("DELETE", "/api/v1/members/abc", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u-1"))
	AuditMiddleware(ar)(forbidden).ServeHTTP(httptest.NewRecorder(), req)
	if gotAction != "DENIED(403) DELETE /api/v1/members/abc" {
		t.Errorf("denied action not recorded, got %q", gotAction)
	}
}

// A denied request with NO authenticated user must not be recorded: outsiders
// could otherwise spray the audit log.
func TestAuditMiddleware_SkipsAnonymousDenials(t *testing.T) {
	logged := false
	ar := &mockAuditRepo{LogFn: func(_ context.Context, _, _, _, _ string, _ map[string]any) error { logged = true; return nil }}
	denied := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	AuditMiddleware(ar)(denied).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/members", nil))
	if logged {
		t.Error("anonymous denial must not be audited")
	}
}

// Handlers can attach chain-protected "what changed" detail to the audit entry
// via setAuditDetail; the middleware records it with the action.
func TestAuditMiddleware_CapturesDetail(t *testing.T) {
	var gotDetail map[string]any
	ar := &mockAuditRepo{LogFn: func(_ context.Context, _, _, _, _ string, detail map[string]any) error {
		gotDetail = detail
		return nil
	}}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAuditDetail(r, map[string]any{"role_old": "member", "role_new": "admin"})
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest("PATCH", "/api/v1/users/u2", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u-1"))
	AuditMiddleware(ar)(inner).ServeHTTP(httptest.NewRecorder(), req)
	if gotDetail["role_old"] != "member" || gotDetail["role_new"] != "admin" {
		t.Errorf("detail not captured: %v", gotDetail)
	}
}

// setAuditDetail outside the middleware (no holder in context) must be a no-op,
// not a panic — auth endpoints live outside the audit group.
func TestSetAuditDetail_NoHolderIsNoop(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", nil)
	setAuditDetail(req, map[string]any{"k": "v"}) // must not panic
}
