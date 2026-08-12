package repo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// Level-storage refusals the handler translates into 4xx responses.
var (
	ErrArcadeLevelQuota     = errors.New("level quota reached for this game")
	ErrArcadeLevelNameTaken = errors.New("another member owns a level with this name")
)

// ArcadeGames is the cabinet allowlist — the only values accepted anywhere
// (API, DB CHECKs). Order here is the display order on the arcade floor.
var ArcadeGames = []string{
	"chess", "go", "comet-buster", "penny-pincher", "brickfall", "powder-keg", "hexfection", "interns",
	"texas-holdem",
}

// ArcadeRepo records credit insertions and high scores for the Top Secret
// arcade. Credits are play tokens, not money — this never touches the GL.
type ArcadeRepo struct {
	db *pgxpool.Pool
}

// NewArcadeRepo constructs an ArcadeRepo.
func NewArcadeRepo(db *pgxpool.Pool) *ArcadeRepo {
	return &ArcadeRepo{db: db}
}

// InsertCredit records one credit for the user on a cabinet and returns the
// user's lifetime credit count for that cabinet.
func (r *ArcadeRepo) InsertCredit(ctx context.Context, userID, game string) (int, error) {
	var total int
	err := r.db.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO arcade_plays (user_id, game) VALUES ($1::uuid, $2)
		)
		SELECT count(*)::int + 1 FROM arcade_plays
		WHERE user_id = $1::uuid AND game = $2`, userID, game).Scan(&total)
	return total, err
}

// SubmitScore records a final score. It refuses (pgx.ErrNoRows) when the user
// has never inserted a credit for the cabinet — no play, no leaderboard.
func (r *ArcadeRepo) SubmitScore(ctx context.Context, userID, game string, score int64) error {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO arcade_scores (user_id, game, score)
		SELECT $1::uuid, $2, $3
		WHERE EXISTS (SELECT 1 FROM arcade_plays WHERE user_id = $1::uuid AND game = $2)`,
		userID, game, score)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// TopScores returns the leaderboard for a cabinet: each user's best score,
// highest first, limited to n rows.
func (r *ArcadeRepo) TopScores(ctx context.Context, game string, n int) ([]model.ArcadeScore, error) {
	if n <= 0 || n > 100 {
		n = 10
	}
	rows, err := r.db.Query(ctx, `
		SELECT coalesce(m.display_name, 'MYSTERY PLAYER'), best.score, best.achieved_at
		FROM (
			SELECT DISTINCT ON (s.user_id) s.user_id, s.score, s.created_at AS achieved_at
			FROM arcade_scores s
			WHERE s.game = $1
			ORDER BY s.user_id, s.score DESC, s.created_at
		) best
		JOIN users u ON u.id = best.user_id
		LEFT JOIN members m ON m.id = u.member_id
		ORDER BY best.score DESC, best.achieved_at
		LIMIT $2`, game, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ArcadeScore{}
	for rows.Next() {
		var s model.ArcadeScore
		if err := rows.Scan(&s.PlayerName, &s.Score, &s.AchievedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TopScoresSince is TopScores restricted to scores achieved on or after
// `since` — the weekly challenge ladder, which therefore resets itself.
func (r *ArcadeRepo) TopScoresSince(ctx context.Context, game string, since time.Time, n int) ([]model.ArcadeScore, error) {
	if n <= 0 || n > 100 {
		n = 10
	}
	rows, err := r.db.Query(ctx, `
		SELECT coalesce(m.display_name, 'MYSTERY PLAYER'), best.score, best.achieved_at
		FROM (
			SELECT DISTINCT ON (s.user_id) s.user_id, s.score, s.created_at AS achieved_at
			FROM arcade_scores s
			WHERE s.game = $1 AND s.created_at >= $3
			ORDER BY s.user_id, s.score DESC, s.created_at
		) best
		JOIN users u ON u.id = best.user_id
		LEFT JOIN members m ON m.id = u.member_id
		ORDER BY best.score DESC, best.achieved_at
		LIMIT $2`, game, n, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ArcadeScore{}
	for rows.Next() {
		var s model.ArcadeScore
		if err := rows.Scan(&s.PlayerName, &s.Score, &s.AchievedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Stats returns one summary row per cabinet (including cabinets never played),
// with the caller's own numbers alongside the house records.
func (r *ArcadeRepo) Stats(ctx context.Context, userID string) ([]model.ArcadeGameStats, error) {
	byGame := map[string]*model.ArcadeGameStats{}
	out := make([]model.ArcadeGameStats, 0, len(ArcadeGames))
	for _, g := range ArcadeGames {
		out = append(out, model.ArcadeGameStats{Game: g})
	}
	for i := range out {
		byGame[out[i].Game] = &out[i]
	}

	rows, err := r.db.Query(ctx, `
		SELECT game, count(*)::int,
		       count(*) FILTER (WHERE user_id = $1::uuid)::int
		FROM arcade_plays GROUP BY game`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var game string
		var plays, mine int
		if err := rows.Scan(&game, &plays, &mine); err != nil {
			return nil, err
		}
		if s := byGame[game]; s != nil {
			s.TotalPlays = plays
			s.YourPlays = mine
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	srows, err := r.db.Query(ctx, `
		SELECT top.game, top.score, coalesce(m.display_name, 'MYSTERY PLAYER'),
		       coalesce(mine.best, 0)
		FROM (
			SELECT DISTINCT ON (game) game, score, user_id
			FROM arcade_scores ORDER BY game, score DESC, created_at
		) top
		JOIN users u ON u.id = top.user_id
		LEFT JOIN members m ON m.id = u.member_id
		LEFT JOIN (
			SELECT game, max(score) AS best FROM arcade_scores
			WHERE user_id = $1::uuid GROUP BY game
		) mine ON mine.game = top.game`, userID)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var game, holder string
		var high, mine int64
		if err := srows.Scan(&game, &high, &holder, &mine); err != nil {
			return nil, err
		}
		if s := byGame[game]; s != nil {
			s.HighScore = high
			s.HighScorer = holder
			s.YourBest = mine
		}
	}
	return out, srows.Err()
}

// ---- per-player statistics (the service records) ----

// BumpStats accumulates a round's counters into the player's lifetime
// numbers. Keys are pre-validated by the handler.
func (r *ArcadeRepo) BumpStats(ctx context.Context, userID, game string, stats map[string]int64) error {
	if len(stats) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for stat, delta := range stats {
		batch.Queue(`
			INSERT INTO arcade_player_stats (user_id, game, stat, value)
			VALUES ($1::uuid, $2, $3, $4)
			ON CONFLICT (user_id, game, stat) DO UPDATE
				SET value = arcade_player_stats.value + EXCLUDED.value`,
			userID, game, stat, delta)
	}
	return r.db.SendBatch(ctx, batch).Close()
}

// PlayerStats returns every counter one player has accrued, grouped by game.
func (r *ArcadeRepo) PlayerStats(ctx context.Context, userID string) (map[string]map[string]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT game, stat, value FROM arcade_player_stats
		WHERE user_id = $1::uuid ORDER BY game, stat`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var game, stat string
		var value int64
		if err := rows.Scan(&game, &stat, &value); err != nil {
			return nil, err
		}
		if out[game] == nil {
			out[game] = map[string]int64{}
		}
		out[game][stat] = value
	}
	return out, rows.Err()
}

// Players lists everyone with an arcade footprint (for the service-record
// browser), busiest first.
func (r *ArcadeRepo) Players(ctx context.Context) ([]model.ArcadePlayer, error) {
	rows, err := r.db.Query(ctx, `
		SELECT u.id::text, coalesce(m.display_name, 'MYSTERY PLAYER'), count(p.id)::int
		FROM arcade_plays p
		JOIN users u ON u.id = p.user_id
		LEFT JOIN members m ON m.id = u.member_id
		GROUP BY u.id, m.display_name
		ORDER BY count(p.id) DESC, coalesce(m.display_name, 'MYSTERY PLAYER')
		LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ArcadePlayer{}
	for rows.Next() {
		var p model.ArcadePlayer
		if err := rows.Scan(&p.UserID, &p.Name, &p.TotalPlays); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- community levels (the level editor's storage) ----

// maxLevelsPerGame keeps the community list arcade-sized.
const maxLevelsPerGame = 500

// SaveLevel stores a level document. Same-name levels by the SAME author are
// replaced (iterate-and-save from the editor); a name taken by someone else
// is a conflict surfaced via the unique constraint.
func (r *ArcadeRepo) SaveLevel(ctx context.Context, game, name, author, data string) (string, error) {
	var count int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*)::int FROM arcade_levels WHERE game = $1`, game).Scan(&count); err != nil {
		return "", err
	}
	if count >= maxLevelsPerGame {
		return "", ErrArcadeLevelQuota
	}
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO arcade_levels (game, name, author, data)
		VALUES ($1, $2, $3::uuid, $4::jsonb)
		ON CONFLICT (game, name) DO UPDATE
			SET data = EXCLUDED.data, created_at = now()
			WHERE arcade_levels.author = EXCLUDED.author
		RETURNING id::text`, game, name, author, data).Scan(&id)
	if err == pgx.ErrNoRows {
		// The ON CONFLICT WHERE clause rejected the update: someone else owns
		// this name.
		return "", ErrArcadeLevelNameTaken
	}
	return id, err
}

// ListLevels returns a game's community levels, newest first.
func (r *ArcadeRepo) ListLevels(ctx context.Context, game string) ([]model.ArcadeLevel, error) {
	rows, err := r.db.Query(ctx, `
		SELECT l.id::text, l.name, coalesce(m.display_name, 'MYSTERY PLAYER'), l.author::text, l.created_at
		FROM arcade_levels l
		JOIN users u ON u.id = l.author
		LEFT JOIN members m ON m.id = u.member_id
		WHERE l.game = $1
		ORDER BY l.created_at DESC
		LIMIT 200`, game)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ArcadeLevel{}
	for rows.Next() {
		var l model.ArcadeLevel
		if err := rows.Scan(&l.ID, &l.Name, &l.AuthorName, &l.AuthorID, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLevel returns one level with its full data document.
func (r *ArcadeRepo) GetLevel(ctx context.Context, id string) (*model.ArcadeLevel, error) {
	var l model.ArcadeLevel
	err := r.db.QueryRow(ctx, `
		SELECT l.id::text, l.game, l.name, coalesce(m.display_name, 'MYSTERY PLAYER'),
		       l.author::text, l.data::text, l.created_at
		FROM arcade_levels l
		JOIN users u ON u.id = l.author
		LEFT JOIN members m ON m.id = u.member_id
		WHERE l.id = $1::uuid`, id).
		Scan(&l.ID, &l.Game, &l.Name, &l.AuthorName, &l.AuthorID, &l.Data, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// DeleteLevel removes a level; only its author (or callers the handler has
// already vetted as admin) may do so — the authorOnly id enforces the former.
func (r *ArcadeRepo) DeleteLevel(ctx context.Context, id, authorOnly string) error {
	var tag pgconn.CommandTag
	var err error
	if authorOnly == "" {
		tag, err = r.db.Exec(ctx, `DELETE FROM arcade_levels WHERE id = $1::uuid`, id)
	} else {
		tag, err = r.db.Exec(ctx,
			`DELETE FROM arcade_levels WHERE id = $1::uuid AND author = $2::uuid`, id, authorOnly)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
