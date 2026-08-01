DROP TRIGGER IF EXISTS trg_minutes_finalize_oneway ON meetings;
DROP FUNCTION IF EXISTS minutes_finalize_oneway();
DROP TRIGGER IF EXISTS trg_minutes_guard ON meeting_minutes_entries;
DROP FUNCTION IF EXISTS minutes_guard();
ALTER TABLE motions DROP COLUMN IF EXISTS business;
ALTER TABLE meetings
    DROP COLUMN IF EXISTS minutes_finalized_by,
    DROP COLUMN IF EXISTS minutes_finalized_at;
DROP TABLE IF EXISTS meeting_minutes_entries;
