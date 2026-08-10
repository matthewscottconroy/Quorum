-- HOLD 'EM joins the arcade: extend every game allowlist CHECK. The game
-- (Texas hold 'em) is public domain; chips are play tokens, never money.
ALTER TABLE arcade_plays DROP CONSTRAINT arcade_plays_game_check;
ALTER TABLE arcade_plays ADD CONSTRAINT arcade_plays_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem'));
ALTER TABLE arcade_scores DROP CONSTRAINT arcade_scores_game_check;
ALTER TABLE arcade_scores ADD CONSTRAINT arcade_scores_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem'));
ALTER TABLE arcade_levels DROP CONSTRAINT arcade_levels_game_check;
ALTER TABLE arcade_levels ADD CONSTRAINT arcade_levels_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem'));
ALTER TABLE arcade_player_stats DROP CONSTRAINT arcade_player_stats_game_check;
ALTER TABLE arcade_player_stats ADD CONSTRAINT arcade_player_stats_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem'));
