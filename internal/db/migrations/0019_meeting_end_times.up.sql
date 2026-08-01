-- Meetings gain an optional end time. Existing rows keep NULL (calendar
-- exports fall back to a one-hour block); when present, the end must follow
-- the start — enforced here so no code path can write an inverted range.
ALTER TABLE meetings ADD COLUMN ends_at TIMESTAMPTZ;
ALTER TABLE meetings ADD CONSTRAINT meetings_ends_after_start
    CHECK (ends_at IS NULL OR ends_at > scheduled_at);
