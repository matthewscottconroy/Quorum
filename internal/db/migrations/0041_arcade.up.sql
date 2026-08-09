-- Top Secret arcade: credit insertions and high scores.
--
-- Credits are play tokens, not money: inserting one records that the user
-- started a game (arcade bookkeeping on the account), nothing else. No
-- balance, no ledger interaction — deliberately disjoint from the GL.

CREATE TABLE arcade_plays (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game       TEXT NOT NULL CHECK (game IN
        ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_arcade_plays_user ON arcade_plays (user_id, game);
CREATE INDEX idx_arcade_plays_game ON arcade_plays (game);

CREATE TABLE arcade_scores (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game       TEXT NOT NULL CHECK (game IN
        ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection')),
    score      BIGINT NOT NULL CHECK (score >= 0 AND score <= 100000000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_arcade_scores_top ON arcade_scores (game, score DESC);
CREATE INDEX idx_arcade_scores_user ON arcade_scores (user_id, game);
