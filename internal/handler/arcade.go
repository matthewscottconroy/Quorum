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
	BumpStats(ctx context.Context, userID, game string, stats map[string]int64) error
	PlayerStats(ctx context.Context, userID string) (map[string]map[string]int64, error)
	Players(ctx context.Context) ([]model.ArcadePlayer, error)
}

// The service-record vocabulary: every counter a cartridge may report, per
// cabinet. Anything else in a report is silently dropped — the table stays
// tidy no matter what a creative client sends. Values are self-reported,
// like scores: friendly numbers, not forensic ones.
var arcadeStatNames = map[string][]string{
	"": {"seconds_played", "rounds_finished"}, // every cabinet reports these
	"comet-buster": {
		"bullets_fired", "rocks_smashed", "saucers_downed", "ships_lost",
		"hyperspace_jumps", "hyperspace_misfires", "waves_cleared", "extra_ships",
	},
	"penny-pincher": {
		"coins_pocketed", "gold_bars", "auditors_bitten", "times_audited",
		"shifts_cleared", "tunnel_trips", "extra_lives", "about_faces",
	},
	"brickfall": {
		"pieces_locked", "lines_cleared", "quads", "hard_drops", "soft_cells",
		"holds_used", "top_outs", "levels_reached",
	},
	"chess": {
		"moves_played", "captures_made", "pieces_lost", "checks_given",
		"pawns_promoted", "knight_promotions", "machine_beaten", "beaten_by_machine",
		"draws", "wins_online", "losses_online", "resignations", "hotseat_rounds",
		"fischer_deals", "puzzles_tested",
	},
	"go": {
		"stones_placed", "stones_captured", "stones_lost", "passes", "takebacks",
		"resignations", "machine_beaten", "beaten_by_machine", "wins_online",
		"losses_online", "hotseat_rounds", "dead_stones_marked",
	},
	"powder-keg": {
		"bombs_laid", "kills", "deaths", "self_demolitions", "crates_smashed",
		"perks_grabbed", "kegs_kicked", "wall_crushes", "steps_walked",
		"cellar_wins", "settle_draws",
	},
	"hexfection": {
		"clones", "jumps", "blobs_converted", "blobs_lost", "times_consumed",
		"dish_wins", "seats_skipped",
	},
	"interns": {
		"interns_saved", "interns_lost", "gravity_lessons", "quits_ordered",
		"nukes_ordered", "climbers_hired", "chutes_issued", "supervisors_promoted",
		"bridges_ordered", "bashers_unleashed", "diggers_deployed", "floor_wins",
	},
	"texas-holdem": {
		"hands_played", "hands_won", "chips_won", "folds", "raises", "all_ins",
		"bust_outs", "tables_swept", "royal_flushes", "straight_flushes",
		"quads_made", "full_houses", "flushes_shown", "straights_shown",
	},
}

var validArcadeStat = func() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(repo.ArcadeGames))
	for _, g := range repo.ArcadeGames {
		set := map[string]bool{}
		for _, s := range arcadeStatNames[""] {
			set[s] = true
		}
		for _, s := range arcadeStatNames[g] {
			set[s] = true
		}
		out[g] = set
	}
	return out
}()

// A single report may not bump any counter by more than this: one round's
// honest output fits easily, a firehose does not.
const maxStatDelta = 200_000

// ArcadeHandler serves the Top Secret arcade: credit insertions (play tokens,
// not money — deliberately disjoint from the ledger) and high-score tables.
type ArcadeHandler struct {
	repo arcadeRepo
}

// NewArcadeHandler constructs the handler.
func NewArcadeHandler(r arcadeRepo) *ArcadeHandler {
	return &ArcadeHandler{repo: r}
}

// ArcadeGate returns middleware that turns every arcade route away while an
// admin has switched the arcade off (Settings → arcade_visible). visible()
// runs on every request, so callers pass something cached, not a DB hit.
// 404 (not 403) on purpose: an off switch means the room does not exist.
func ArcadeGate(visible func() bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !visible() {
				writeError(w, http.StatusNotFound, "the arcade is switched off", "not_found")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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

// ReportStats accumulates one round's counters into the caller's lifetime
// service record (member+). Unknown or absurd entries are dropped, not
// rejected — the round already happened, we keep what's plausible.
func (h *ArcadeHandler) ReportStats(w http.ResponseWriter, r *http.Request) {
	game, ok := arcadeGame(w, r)
	if !ok {
		return
	}
	var body struct {
		Stats map[string]int64 `json:"stats"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.Stats) == 0 {
		writeError(w, http.StatusBadRequest, "a non-empty {stats: {name: count}} object is required", "bad_request")
		return
	}
	allowed := validArcadeStat[game]
	clean := make(map[string]int64, len(body.Stats))
	for name, v := range body.Stats {
		if allowed[name] && v > 0 && v <= maxStatDelta {
			clean[name] = v
		}
	}
	if len(clean) > 0 {
		if err := h.repo.BumpStats(r.Context(), userIDFromCtx(r), game, clean); err != nil {
			writeError(w, http.StatusInternalServerError, "stats error", "internal_error")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Players lists everyone with an arcade footprint — the service-record
// browser's roster (member+; the arcade is a shared basement, stats are
// deliberately public within the org, like the leaderboards).
func (h *ArcadeHandler) Players(w http.ResponseWriter, r *http.Request) {
	players, err := h.repo.Players(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, players)
}

// PlayerStats returns one member's full service record, grouped by cabinet
// (member+). ?user=<id> reads anyone's; default is your own.
func (h *ArcadeHandler) PlayerStats(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user")
	if userID == "" {
		userID = userIDFromCtx(r)
	} else if !isValidUUID(userID) {
		writeError(w, http.StatusBadRequest, "user must be a UUID", "bad_request")
		return
	}
	stats, err := h.repo.PlayerStats(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "games": stats})
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
