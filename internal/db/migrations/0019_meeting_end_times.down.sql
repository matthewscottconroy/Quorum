ALTER TABLE meetings DROP CONSTRAINT IF EXISTS meetings_ends_after_start;
ALTER TABLE meetings DROP COLUMN IF EXISTS ends_at;
