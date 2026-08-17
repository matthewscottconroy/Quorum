DROP TRIGGER IF EXISTS trg_meeting_core_guard ON meetings;
DROP FUNCTION IF EXISTS meeting_core_guard();
DROP TRIGGER IF EXISTS trg_decisions_guard ON meeting_decisions;
DROP TRIGGER IF EXISTS trg_attendees_guard ON meeting_attendees;
DROP FUNCTION IF EXISTS meeting_satellite_guard();
ALTER TABLE meetings DROP COLUMN IF EXISTS minutes_snapshot;
