package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	SaveLevel(ctx context.Context, game, name, author, data string) (string, error)
	ListLevels(ctx context.Context, game string) ([]model.ArcadeLevel, error)
	GetLevel(ctx context.Context, id string) (*model.ArcadeLevel, error)
	DeleteLevel(ctx context.Context, id, authorOnly string) error
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

// ---- community levels ----

const maxLevelBytes = 48 * 1024

// SaveLevel stores a level from the editor (member+). Re-saving your own
// name updates it; someone else's name is a conflict.
func (h *ArcadeHandler) SaveLevel(w http.ResponseWriter, r *http.Request) {
	game, ok := arcadeGame(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 60 {
		writeError(w, http.StatusBadRequest, "name (1-60 chars) required", "bad_request")
		return
	}
	if len(body.Data) == 0 || len(body.Data) > maxLevelBytes {
		writeError(w, http.StatusBadRequest, "level data must be 1 byte to 48 KB of JSON", "bad_request")
		return
	}
	if !json.Valid(body.Data) {
		writeError(w, http.StatusBadRequest, "level data must be valid JSON", "bad_request")
		return
	}
	id, err := h.repo.SaveLevel(r.Context(), game, body.Name, userIDFromCtx(r), string(body.Data))
	switch {
	case errors.Is(err, repo.ErrArcadeLevelQuota):
		writeError(w, http.StatusConflict, "this cabinet's level shelf is full", "conflict")
		return
	case errors.Is(err, repo.ErrArcadeLevelNameTaken):
		writeError(w, http.StatusConflict, "another member owns a level with this name", "conflict")
		return
	case err != nil:
		writeRepoError(w, err, "", "save error")
		return
	}
	setAuditDetail(r, map[string]any{"game": game, "level": body.Name})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": body.Name})
}

// ListLevels returns a cabinet's community levels (member+), sans data.
func (h *ArcadeHandler) ListLevels(w http.ResponseWriter, r *http.Request) {
	game, ok := arcadeGame(w, r)
	if !ok {
		return
	}
	levels, err := h.repo.ListLevels(r.Context(), game)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, levels)
}

// GetLevel returns one level including its data document (member+).
func (h *ArcadeHandler) GetLevel(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	l, err := h.repo.GetLevel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "level not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// DeleteLevel removes a level: its author may always; admins may moderate.
func (h *ArcadeHandler) DeleteLevel(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	authorOnly := userIDFromCtx(r)
	if roleAtLeast(roleFromCtx(r), "admin") {
		authorOnly = "" // moderation: any level
	}
	if err := h.repo.DeleteLevel(r.Context(), id, authorOnly); err != nil {
		writeRepoError(w, err, "level not found (or not yours)", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
