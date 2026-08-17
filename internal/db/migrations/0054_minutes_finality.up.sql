-- Finalized minutes become genuinely final.
--
-- The 0020 guard locked only the journal (meeting_minutes_entries), but the
-- minutes DOCUMENT is generated from the meeting row, the attendance roster,
-- and the decision log too — all of which stayed editable after finalization,
-- so the "official record" could silently change. Three additions:
--
-- 1) A snapshot column: the rendered Markdown document is stored at finalize
--    time and served thereafter, whatever else happens to the source rows.
ALTER TABLE meetings ADD COLUMN minutes_snapshot TEXT;

-- 2) The satellite tables lock with the journal.
CREATE FUNCTION meeting_satellite_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    mid uuid;
    finalized timestamptz;
BEGIN
    IF TG_OP = 'INSERT' THEN
        mid := NEW.meeting_id;
    ELSE
        mid := OLD.meeting_id;
    END IF;
    SELECT minutes_finalized_at INTO finalized FROM meetings WHERE id = mid;
    IF finalized IS NOT NULL THEN
        RAISE EXCEPTION 'minutes are finalized and immutable (approved on %)', finalized;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER trg_attendees_guard
    BEFORE INSERT OR UPDATE OR DELETE ON meeting_attendees
    FOR EACH ROW EXECUTE FUNCTION meeting_satellite_guard();
CREATE TRIGGER trg_decisions_guard
    BEFORE INSERT OR UPDATE OR DELETE ON meeting_decisions
    FOR EACH ROW EXECUTE FUNCTION meeting_satellite_guard();

-- 3) The meeting's own identity fields — the header of the minutes — freeze.
--    Status and notes stay editable (cancelling or annotating a past meeting
--    is not rewriting its minutes), and the snapshot itself may be written.
CREATE FUNCTION meeting_core_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.minutes_finalized_at IS NOT NULL AND (
        NEW.title        IS DISTINCT FROM OLD.title OR
        NEW.scheduled_at IS DISTINCT FROM OLD.scheduled_at OR
        NEW.ends_at      IS DISTINCT FROM OLD.ends_at OR
        NEW.location     IS DISTINCT FROM OLD.location OR
        NEW.agenda       IS DISTINCT FROM OLD.agenda
    ) THEN
        RAISE EXCEPTION 'minutes are finalized: the meeting header is part of the official record and cannot change';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER trg_meeting_core_guard
    BEFORE UPDATE ON meetings
    FOR EACH ROW EXECUTE FUNCTION meeting_core_guard();
