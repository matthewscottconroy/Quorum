-- INTERNS cabinet: the walkers-and-jobs game joins the arcade, and members
-- can build and share their own levels (the level editor's storage).

-- The game allowlists live in CHECK constraints; extend both.
ALTER TABLE arcade_plays DROP CONSTRAINT arcade_plays_game_check;
ALTER TABLE arcade_plays ADD CONSTRAINT arcade_plays_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns'));
ALTER TABLE arcade_scores DROP CONSTRAINT arcade_scores_game_check;
ALTER TABLE arcade_scores ADD CONSTRAINT arcade_scores_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns'));

-- Community levels: JSON documents the cartridge understands. Size-capped so
-- nobody stores their music collection in the arcade.
CREATE TABLE arcade_levels (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    game       TEXT NOT NULL CHECK (game IN
        ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns')),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    author     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    data       JSONB NOT NULL CHECK (octet_length(data::text) <= 49152),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (game, name)
);
CREATE INDEX idx_arcade_levels_game ON arcade_levels (game, created_at DESC);
