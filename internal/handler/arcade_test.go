package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

type mockArcadeRepo struct {
	InsertCreditFn func(ctx context.Context, userID, game string) (int, error)
	SubmitScoreFn  func(ctx context.Context, userID, game string, score int64) error
	TopScoresFn    func(ctx context.Context, game string, n int) ([]model.ArcadeScore, error)
	StatsFn        func(ctx context.Context, userID string) ([]model.ArcadeGameStats, error)
	SaveLevelFn    func(ctx context.Context, game, name, author, data string) (string, error)
	GetLevelFn     func(ctx context.Context, id string) (*model.ArcadeLevel, error)
	DeleteLevelFn  func(ctx context.Context, id, authorOnly string) error
	BumpStatsFn    func(ctx context.Context, userID, game string, stats map[string]int64) error
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
func (m *mockArcadeRepo) SaveLevel(ctx context.Context, game, name, author, data string) (string, error) {
	if m.SaveLevelFn != nil {
		return m.SaveLevelFn(ctx, game, name, author, data)
	}
	return "lvl-1", nil
}
func (m *mockArcadeRepo) ListLevels(ctx context.Context, game string) ([]model.ArcadeLevel, error) {
	return []model.ArcadeLevel{}, nil
}
func (m *mockArcadeRepo) GetLevel(ctx context.Context, id string) (*model.ArcadeLevel, error) {
	if m.GetLevelFn != nil {
		return m.GetLevelFn(ctx, id)
	}
	return nil, pgx.ErrNoRows
}
func (m *mockArcadeRepo) DeleteLevel(ctx context.Context, id, authorOnly string) error {
	if m.DeleteLevelFn != nil {
		return m.DeleteLevelFn(ctx, id, authorOnly)
	}
	return nil
}
func (m *mockArcadeRepo) BumpStats(ctx context.Context, userID, game string, stats map[string]int64) error {
	if m.BumpStatsFn != nil {
		return m.BumpStatsFn(ctx, userID, game, stats)
	}
	return nil
}
func (m *mockArcadeRepo) PlayerStats(_ context.Context, _ string) (map[string]map[string]int64, error) {
	return map[string]map[string]int64{"chess": {"moves_played": 42}}, nil
}
func (m *mockArcadeRepo) Players(_ context.Context) ([]model.ArcadePlayer, error) {
	return []model.ArcadePlayer{{UserID: testUUID, Name: "Ada Lovelace", TotalPlays: 7}}, nil
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

func TestArcadeReportStats_FiltersJunkKeepsTheHonest(t *testing.T) {
	var got map[string]int64
	h := NewArcadeHandler(&mockArcadeRepo{
		BumpStatsFn: func(_ context.Context, userID, game string, stats map[string]int64) error {
			if userID != "u1" || game != "powder-keg" {
				t.Errorf("passed through: %q %q", userID, game)
			}
			got = stats
			return nil
		},
	})
	body := `{"stats":{"bombs_laid":12,"kills":3,"grandmas_startled":9,"deaths":-4,"steps_walked":99999999}}`
	req := chiRequest("POST", "/arcade/powder-keg/stats-report", body, map[string]string{"game": "powder-keg"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.ReportStats(rr, req)
	if rr.Code != 204 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	// Unknown key, negative value, and firehose value all dropped.
	if len(got) != 2 || got["bombs_laid"] != 12 || got["kills"] != 3 {
		t.Errorf("cleaned stats: %v", got)
	}
}

func TestArcadeReportStats_GlobalCountersWorkOnEveryCabinet(t *testing.T) {
	var got map[string]int64
	h := NewArcadeHandler(&mockArcadeRepo{
		BumpStatsFn: func(_ context.Context, _, _ string, stats map[string]int64) error {
			got = stats
			return nil
		},
	})
	req := chiRequest("POST", "/arcade/brickfall/stats-report",
		`{"stats":{"seconds_played":95,"rounds_finished":1,"lines_cleared":4}}`,
		map[string]string{"game": "brickfall"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.ReportStats(rr, req)
	if rr.Code != 204 || len(got) != 3 {
		t.Fatalf("status %d, stats %v", rr.Code, got)
	}
}

func TestArcadePlayerStats_DefaultsToSelfAndValidatesUUID(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{})
	req := httptest.NewRequest("GET", "/arcade/player-stats", nil)
	req = withCtxUser(req, "u-self", "member")
	rr := httptest.NewRecorder()
	h.PlayerStats(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var out struct {
		UserID string                      `json:"user_id"`
		Games  map[string]map[string]int64 `json:"games"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.UserID != "u-self" || out.Games["chess"]["moves_played"] != 42 {
		t.Errorf("got %+v", out)
	}

	req = httptest.NewRequest("GET", "/arcade/player-stats?user=not-a-uuid", nil)
	req = withCtxUser(req, "u-self", "member")
	rr = httptest.NewRecorder()
	h.PlayerStats(rr, req)
	if rr.Code != 400 {
		t.Errorf("bad uuid: got %d, want 400", rr.Code)
	}
}

func TestArcadeGate_SwitchesTheFloorOff(t *testing.T) {
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(200)
	})
	on := true
	gate := ArcadeGate(func() bool { return on })(inner)

	rr := httptest.NewRecorder()
	gate.ServeHTTP(rr, httptest.NewRequest("GET", "/arcade/stats", nil))
	if rr.Code != 200 || !reached {
		t.Errorf("visible: got %d (reached=%v), want pass-through", rr.Code, reached)
	}

	on, reached = false, false
	rr = httptest.NewRecorder()
	gate.ServeHTTP(rr, httptest.NewRequest("GET", "/arcade/stats", nil))
	if rr.Code != 404 || reached {
		t.Errorf("switched off: got %d (reached=%v), want 404 without reaching the handler", rr.Code, reached)
	}
}

// ---- community levels ----

func TestArcadeSaveLevel_ValidatesAndStores(t *testing.T) {
	var gotGame, gotName, gotAuthor string
	h := NewArcadeHandler(&mockArcadeRepo{
		SaveLevelFn: func(_ context.Context, game, name, author, data string) (string, error) {
			gotGame, gotName, gotAuthor = game, name, author
			if !json.Valid([]byte(data)) {
				t.Error("data should be JSON")
			}
			return "lvl-9", nil
		},
	})
	body := `{"name":"  Ramp Works ","data":{"v":1,"rects":[[0,200,360,20]]}}`
	req := chiRequest("POST", "/arcade/interns/levels", body, map[string]string{"game": "interns"})
	req = withCtxUser(req, "u-author", "member")
	rr := httptest.NewRecorder()
	h.SaveLevel(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if gotGame != "interns" || gotName != "Ramp Works" || gotAuthor != "u-author" {
		t.Errorf("passed through: %q %q %q", gotGame, gotName, gotAuthor)
	}
}

func TestArcadeSaveLevel_RefusesGarbage(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{})
	for _, body := range []string{
		`{"name":"","data":{"v":1}}`, // empty name
		`{"name":"x"}`,               // no data
		`{"name":"x","data":"` + strings.Repeat("A", maxLevelBytes+10) + `"}`, // too big
	} {
		req := chiRequest("POST", "/arcade/interns/levels", body, map[string]string{"game": "interns"})
		req = withCtxUser(req, "u", "member")
		rr := httptest.NewRecorder()
		h.SaveLevel(rr, req)
		if rr.Code != 400 {
			t.Errorf("body %.40s...: got %d, want 400", body, rr.Code)
		}
	}
}

func TestArcadeSaveLevel_NameConflict(t *testing.T) {
	h := NewArcadeHandler(&mockArcadeRepo{
		SaveLevelFn: func(_ context.Context, _, _, _, _ string) (string, error) {
			return "", repo.ErrArcadeLevelNameTaken
		},
	})
	req := chiRequest("POST", "/arcade/interns/levels", `{"name":"Taken","data":{"v":1}}`, map[string]string{"game": "interns"})
	req = withCtxUser(req, "u", "member")
	rr := httptest.NewRecorder()
	h.SaveLevel(rr, req)
	if rr.Code != 409 {
		t.Errorf("got %d, want 409", rr.Code)
	}
}

func TestArcadeDeleteLevel_AuthorScopedUnlessAdmin(t *testing.T) {
	var gotAuthorOnly *string
	h := NewArcadeHandler(&mockArcadeRepo{
		DeleteLevelFn: func(_ context.Context, _, authorOnly string) error {
			gotAuthorOnly = &authorOnly
			return nil
		},
	})
	req := chiRequest("DELETE", "/arcade/levels/"+testUUID, "", map[string]string{"id": testUUID})
	req = withCtxUser(req, "u-member", "member")
	rr := httptest.NewRecorder()
	h.DeleteLevel(rr, req)
	if rr.Code != 204 || gotAuthorOnly == nil || *gotAuthorOnly != "u-member" {
		t.Errorf("member delete: status %d, authorOnly %v", rr.Code, gotAuthorOnly)
	}
	req = chiRequest("DELETE", "/arcade/levels/"+testUUID, "", map[string]string{"id": testUUID})
	req = withCtxUser(req, "u-admin", "admin")
	rr = httptest.NewRecorder()
	h.DeleteLevel(rr, req)
	if gotAuthorOnly == nil || *gotAuthorOnly != "" {
		t.Errorf("admin delete should moderate any level, got %v", gotAuthorOnly)
	}
}
