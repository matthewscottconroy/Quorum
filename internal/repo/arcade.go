package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ArcadeGames is the cabinet allowlist — the only values accepted anywhere
// (API, DB CHECKs). Order here is the display order on the arcade floor.
var ArcadeGames = []string{
	"chess", "go", "comet-buster", "penny-pincher", "brickfall", "powder-keg", "hexfection",
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
