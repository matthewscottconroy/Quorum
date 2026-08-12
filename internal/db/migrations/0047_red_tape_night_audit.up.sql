-- Two more cabinets: RED TAPE (paddle-and-brick, original theme) and
-- NIGHT AUDIT (an original after-hours infiltration shooter). Extend every
-- game allowlist CHECK, same drill as 0045.
ALTER TABLE arcade_plays DROP CONSTRAINT arcade_plays_game_check;
ALTER TABLE arcade_plays ADD CONSTRAINT arcade_plays_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem','red-tape','night-audit'));
ALTER TABLE arcade_scores DROP CONSTRAINT arcade_scores_game_check;
ALTER TABLE arcade_scores ADD CONSTRAINT arcade_scores_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem','red-tape','night-audit'));
ALTER TABLE arcade_levels DROP CONSTRAINT arcade_levels_game_check;
ALTER TABLE arcade_levels ADD CONSTRAINT arcade_levels_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem','red-tape','night-audit'));
ALTER TABLE arcade_player_stats DROP CONSTRAINT arcade_player_stats_game_check;
ALTER TABLE arcade_player_stats ADD CONSTRAINT arcade_player_stats_game_check CHECK (game IN
    ('chess','go','comet-buster','penny-pincher','brickfall','powder-keg','hexfection','interns','texas-holdem','red-tape','night-audit'));
