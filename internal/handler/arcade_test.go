package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
)

type mockArcadeRepo struct {
	InsertCreditFn func(ctx context.Context, userID, game string) (int, error)
	SubmitScoreFn  func(ctx context.Context, userID, game string, score int64) error
	TopScoresFn    func(ctx context.Context, game string, n int) ([]model.ArcadeScore, error)
	StatsFn        func(ctx context.Context, userID string) ([]model.ArcadeGameStats, error)
}

func (m *mockArcadeRepo) InsertCredit(ctx context.Context, userID, game string) (int, error) {
	return m.InsertCreditFn(ctx, userID, game)
}
func (m *mockArcadeRepo) SubmitScore(ctx context.Context, userID, game string, score int64) error {
	return m.SubmitScoreFn(ctx, userID, game, score)
}
func (m *mockArcadeRepo) TopScores(ctx context.Context, game string, n int) ([]model.ArcadeScore, error) {
	return m.TopScoresFn(ctx, game, n)
}
func (m *mockArcadeRepo) Stats(ctx context.Context, userID string) ([]model.ArcadeGameStats, error) {
	return m.StatsFn(ctx, userID)
}

func TestArcadeInsertCredit_Success(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{
		InsertCreditFn: func(_ context.Context, userID, game string) (int, error) {
			if userID != "u1" || game != "brickfall" {
				t.Errorf("passed through: user=%q game=%q", userID, game)
			}
			return 3, nil
		},
	})
	req := chiRequest("POST", "/arcade/brickfall/credit", "", map[string]string{"game": "brickfall"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.InsertCredit(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["credits"].(float64) != 3 {
		t.Errorf("credits: got %v", got["credits"])
	}
}

func TestArcadeInsertCredit_UnknownCabinet(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{})
	req := chiRequest("POST", "/arcade/pacman/credit", "", map[string]string{"game": "pacman"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.InsertCredit(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for unknown cabinet", rr.Code)
	}
}

func TestArcadeSubmitScore_RequiresCredit(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{
		SubmitScoreFn: func(_ context.Context, _, _ string, _ int64) error {
			return pgx.ErrNoRows // no credit ever inserted
		},
	})
	req := chiRequest("POST", "/arcade/chess/score", `{"score":100}`, map[string]string{"game": "chess"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.SubmitScore(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d, want 409 without a credited play", rr.Code)
	}
}

func TestArcadeSubmitScore_Success(t *testing.T) {
	var got int64
	h := NewArcadeHandler(&mockArcadeRepo{
		SubmitScoreFn: func(_ context.Context, _, _ string, score int64) error {
			got = score
			return nil
		},
	})
	req := chiRequest("POST", "/arcade/hexfection/score", `{"score":4200}`, map[string]string{"game": "hexfection"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.SubmitScore(rr, req)
	if rr.Code != 204 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if got != 4200 {
		t.Errorf("score: got %d", got)
	}
}

func TestArcadeSubmitScore_Bounds(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{})
	for _, body := range []string{`{"score":-1}`, `{"score":100000001}`} {
		req := chiRequest("POST", "/arcade/go/score", body, map[string]string{"game": "go"})
		req = withCtxUser(req, "u1", "member")
		rr := httptest.NewRecorder()
		h.SubmitScore(rr, req)
		if rr.Code != 400 {
			t.Errorf("body %s: status got %d, want 400", body, rr.Code)
		}
	}
}

func TestArcadeStats_AllCabinets(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{
		StatsFn: func(_ context.Context, _ string) ([]model.ArcadeGameStats, error) {
			return []model.ArcadeGameStats{{Game: "chess", TotalPlays: 7}}, nil
		},
	})
	req := httptest.NewRequest("GET", "/arcade/stats", nil)
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d", rr.Code)
	}
}
