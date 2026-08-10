-- Per-player arcade statistics: lifetime counters per (user, game, stat),
-- accumulated from the cartridge's end-of-round report. Deliberately a
-- key/value ledger — each cabinet tracks its own vocabulary of deeds
-- (bombs laid, hyperdrive misfires, takebacks begged) without schema churn.
-- Values are self-reported by the wasm client, like scores: friendly
-- numbers, not forensic ones. The API allowlists stat names per game.
CREATE TABLE arcade_player_stats (
    user_id UUID   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game    TEXT   NOT NULL CHECK (game IN
        ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns')),
    stat    TEXT   NOT NULL CHECK (stat ~ '^[a-z0-9_]{1,40}$'),
    value   BIGINT NOT NULL DEFAULT 0 CHECK (value >= 0),
    PRIMARY KEY (user_id, game, stat)
);
CREATE INDEX idx_arcade_player_stats_game ON arcade_player_stats (game, stat);
