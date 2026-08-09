package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// arcadeRepo is satisfied by *repo.ArcadeRepo.
type arcadeRepo interface {
	InsertCredit(ctx context.Context, userID, game string) (int, error)
	SubmitScore(ctx context.Context, userID, game string, score int64) error
	TopScores(ctx context.Context, game string, n int) ([]model.ArcadeScore, error)
	Stats(ctx context.Context, userID string) ([]model.ArcadeGameStats, error)
}

// ArcadeHandler serves the Top Secret arcade: credit insertions (play tokens,
// not money — deliberately disjoint from the ledger) and high-score tables.
type ArcadeHandler struct {
	repo arcadeRepo
}

// NewArcadeHandler constructs the handler.
func NewArcadeHandler(r arcadeRepo) *ArcadeHandler {
	return &ArcadeHandler{repo: r}
}

var validArcadeGame = func() map[string]bool {
	m := make(map[string]bool, len(repo.ArcadeGames))
	for _, g := range repo.ArcadeGames {
		m[g] = true
	}
	return m
}()

func arcadeGame(w http.ResponseWriter, r *http.Request) (string, bool) {
	game := chi.URLParam(r, "game")
	if !validArcadeGame[game] {
		writeError(w, http.StatusNotFound, "unknown cabinet", "not_found")
		return "", false
	}
	return game, true
}

// InsertCredit records one credit for the caller on a cabinet (member+).
func (h *ArcadeHandler) InsertCredit(w http.ResponseWriter, r *http.Request) {
	game, ok := arcadeGame(w, r)
	if !ok {
		return
	}
	total, err := h.repo.InsertCredit(r.Context(), userIDFromCtx(r), game)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credit error", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"game": game, "credits": total})
}

// SubmitScore records the caller's final score for a cabinet (member+).
// Requires at least one inserted credit for that cabinet, ever.
func (h *ArcadeHandler) SubmitScore(w http.ResponseWriter, r *http.Request) {
	game, ok := arcadeGame(w, r)
	if !ok {
		return
	}
	var body struct {
		Score int64 `json:"score"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Score < 0 || body.Score > 100_000_000 {
		writeError(w, http.StatusBadRequest, "score must be between 0 and 100000000", "bad_request")
		return
	}
	if err := h.repo.SubmitScore(r.Context(), userIDFromCtx(r), game, body.Score); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusConflict, "insert a credit before submitting a score", "conflict")
			return
		}
		writeRepoError(w, err, "", "score error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TopScores returns a cabinet's leaderboard (member+): each player's best.
func (h *ArcadeHandler) TopScores(w http.ResponseWriter, r *http.Request) {
	game, ok := arcadeGame(w, r)
	if !ok {
		return
	}
	scores, err := h.repo.TopScores(r.Context(), game, clampLimit(r.URL.Query().Get("limit"), 10))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, scores)
}

// Stats returns one summary per cabinet, including the caller's own numbers.
func (h *ArcadeHandler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.repo.Stats(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
