-- Recording-secretary support (Robert's Rules of Order).
--
-- The secretary keeps a chronological journal during the meeting — call to
-- order, approval of previous minutes, reports, old business, new business,
-- discussion (optionally tied to a motion), points of order, recess,
-- adjournment. Motions/seconds/votes/results already live in the motions
-- tables; the journal plus those tables generate the minutes document.
--
-- Once minutes are FINALIZED (approved), they are the official record: the
-- journal becomes immutable at the database level, the same discipline as the
-- audit chain and the payments ledger. Finalization itself cannot be undone.

CREATE TABLE meeting_minutes_entries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id  UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    -- seq gives a stable chronological order even within one second.
    seq         BIGSERIAL,
    kind        TEXT NOT NULL CHECK (kind IN (
                    'call_to_order', 'previous_minutes', 'report',
                    'old_business', 'new_business', 'discussion',
                    'point_of_order', 'recess', 'adjournment', 'note')),
    body        TEXT NOT NULL CHECK (length(body) <= 8000),
    -- Discussion (and any entry) may reference the motion being debated.
    motion_id   UUID REFERENCES motions(id) ON DELETE SET NULL,
    recorded_by UUID REFERENCES users(id) ON DELETE RESTRICT,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_minutes_meeting ON meeting_minutes_entries (meeting_id, seq);

ALTER TABLE meetings
    ADD COLUMN minutes_finalized_at TIMESTAMPTZ,
    ADD COLUMN minutes_finalized_by UUID REFERENCES users(id) ON DELETE RESTRICT;

-- Old business vs new business — the Robert's Rules agenda split — on motions.
ALTER TABLE motions
    ADD COLUMN business TEXT NOT NULL DEFAULT 'new'
        CHECK (business IN ('new', 'old'));

-- Immutability: once a meeting's minutes are finalized, its journal is locked
-- (no INSERT/UPDATE/DELETE), enforced in the database so it holds against
-- direct SQL, not just the API.
CREATE FUNCTION minutes_guard() RETURNS trigger
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

CREATE TRIGGER trg_minutes_guard
    BEFORE INSERT OR UPDATE OR DELETE ON meeting_minutes_entries
    FOR EACH ROW EXECUTE FUNCTION minutes_guard();

-- Finalization is one-way: approved minutes cannot quietly become drafts again.
CREATE FUNCTION minutes_finalize_oneway() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.minutes_finalized_at IS NOT NULL
       AND NEW.minutes_finalized_at IS DISTINCT FROM OLD.minutes_finalized_at THEN
        RAISE EXCEPTION 'minutes finalization cannot be undone or altered';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER trg_minutes_finalize_oneway
    BEFORE UPDATE ON meetings
    FOR EACH ROW EXECUTE FUNCTION minutes_finalize_oneway();
