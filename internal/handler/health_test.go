package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

// fakePinger satisfies the pinger interface used by Readyz.
type fakePinger struct {
	err error
}

func (f fakePinger) Ping(_ context.Context) error { return f.err }

func TestHealthz_AlwaysOK(t *testing.T) {
	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	Healthz(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestReadyz_DBUp(t *testing.T) {
	h := Readyz(fakePinger{err: nil})
	req := httptest.NewRequest("GET", "/readyz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 when ping succeeds", rr.Code)
	}
}

func TestReadyz_DBDown(t *testing.T) {
	h := Readyz(fakePinger{err: errors.New("connection refused")})
	req := httptest.NewRequest("GET", "/readyz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != 503 {
		t.Errorf("status: got %d, want 503 when ping fails", rr.Code)
	}
}
