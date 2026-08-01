-- Digest v2: bring the `detail` JSONB column under the hash chain.
--
-- 0017 chained who/what/which/when. The application now records WHAT CHANGED
-- for sensitive mutations (governance decisions and motions, invoice status
-- transitions, role changes, quorum settings) in audit_log.detail — and a
-- chain that doesn't cover detail would let an insider rewrite that context
-- undetected. So the digest now includes detail, rendered via jsonb::text
-- (PostgreSQL normalizes jsonb deterministically, so the text form is stable).
--
-- The whole retained chain is recomputed under the v2 formula. THIS CHANGES
-- EVERY entry_hash INCLUDING THE HEAD: a head hash recorded before this
-- migration will no longer match. That is expected; this migration file is
-- itself the auditable record of the formula change, and auditors re-anchor on
-- the post-migration head (see COMPLIANCE.md).

DROP TRIGGER trg_audit_log_chain ON audit_log;
DROP FUNCTION audit_log_chain();
DROP FUNCTION audit_entry_digest(bigint, uuid, text, text, text, timestamptz, text);

CREATE FUNCTION audit_entry_digest(
    p_seq bigint, p_user uuid, p_action text, p_etype text, p_eid text,
    p_detail jsonb, p_at timestamptz, p_prev text
) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT encode(digest(
        p_seq::text || '|' ||
        coalesce(p_user::text, '') || '|' ||
        p_action || '|' ||
        coalesce(p_etype, '') || '|' ||
        coalesce(p_eid, '') || '|' ||
        coalesce(p_detail::text, '') || '|' ||
        to_char(p_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '|' ||
        coalesce(p_prev, ''),
    'sha256'), 'hex')
$$;

-- Recomputing the chain requires UPDATEs that the append-only guard (rightly)
-- refuses. Disabling it here is the sanctioned pattern from 0017's header: a
-- deliberate, visible act inside a reviewed migration file, re-enabled
-- immediately after. The recompute rewrites only prev_hash/entry_hash — the
-- recorded facts (actor, action, entity, time, detail) are untouched.
ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update;

DO $$
DECLARE
    r    RECORD;
    prev TEXT := NULL;
    h    TEXT;
BEGIN
    FOR r IN SELECT id, seq, user_id, action, entity_type, entity_id, detail, created_at
             FROM audit_log ORDER BY seq LOOP
        h := audit_entry_digest(r.seq, r.user_id, r.action, r.entity_type, r.entity_id, r.detail, r.created_at, prev);
        UPDATE audit_log SET prev_hash = prev, entry_hash = h WHERE id = r.id;
        prev := h;
    END LOOP;
END $$;

ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update;

CREATE FUNCTION audit_log_chain() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(2820115557);
    NEW.created_at := now();
    NEW.seq        := nextval('audit_log_seq_seq');
    SELECT entry_hash INTO NEW.prev_hash FROM audit_log ORDER BY seq DESC LIMIT 1;
    NEW.entry_hash := audit_entry_digest(NEW.seq, NEW.user_id, NEW.action,
                                         NEW.entity_type, NEW.entity_id,
                                         NEW.detail, NEW.created_at, NEW.prev_hash);
    RETURN NEW;
END $$;

CREATE TRIGGER trg_audit_log_chain
    BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_chain();
