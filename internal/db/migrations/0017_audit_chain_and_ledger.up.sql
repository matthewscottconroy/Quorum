-- Evidence-grade integrity for the audit log and the financial ledger.
--
-- Goal: what the application reports must be trustworthy to a third party
-- (accountant, lawyer, auditor) even against a malicious insider with
-- application-level access. Two mechanisms, layered:
--
--  1. TAMPER-EVIDENT: every audit_log row carries a SHA-256 hash of its own
--     content plus the previous row's hash (a hash chain). Any edit, deletion,
--     or insertion in the middle breaks every verification from that point on.
--     `quorum -verify-audit` (and GET /api/v1/audit/verify) recompute the chain.
--  2. TAMPER-RESISTANT: triggers make audit_log append-only (no UPDATE ever;
--     DELETE only for rows past the 90-day retention floor, so the nightly
--     prune still works), make the transactions table a strict append-only
--     ledger, and freeze the identity of an invoice (amount, currency, member,
--     period) after creation while still allowing status/notes/reminder updates.
--
-- Threat model: an insider using the application, a leaked credential, or SQL
-- injection cannot silently rewrite history — triggers refuse, and anything
-- that bypasses them (a database superuser can always disable triggers) breaks
-- the hash chain and is detectable. What this cannot stop: a superuser who
-- rewrites the ENTIRE chain from the tampered row forward. Defense for that is
-- operational: off-box backups (BACKUP.md) capture chain heads over time, and
-- COMPLIANCE.md tells auditors to record the head hash at each engagement.
--
-- Note for future data migrations: legitimate bulk rewrites of these tables
-- must explicitly `ALTER TABLE ... DISABLE TRIGGER ...` and re-enable after —
-- a deliberate, visible act in the migration file.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Deterministic digest of one audit entry. Timestamp is rendered in UTC with
-- microseconds so the digest is independent of the session time zone. Kept in
-- ONE place so the insert trigger and the verifier can never disagree.
CREATE FUNCTION audit_entry_digest(
    p_seq bigint, p_user uuid, p_action text, p_etype text, p_eid text,
    p_at timestamptz, p_prev text
) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT encode(digest(
        p_seq::text || '|' ||
        coalesce(p_user::text, '') || '|' ||
        p_action || '|' ||
        coalesce(p_etype, '') || '|' ||
        coalesce(p_eid, '') || '|' ||
        to_char(p_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '|' ||
        coalesce(p_prev, ''),
    'sha256'), 'hex')
$$;

-- Chain columns. seq is the total order of the chain (id is a random UUID and
-- created_at can collide at microsecond resolution).
ALTER TABLE audit_log
    ADD COLUMN seq        BIGINT,
    ADD COLUMN prev_hash  TEXT,
    ADD COLUMN entry_hash TEXT;

CREATE SEQUENCE audit_log_seq_seq;

-- Backfill existing rows in chronological order and compute their chain, so
-- history recorded before this migration is covered too.
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn FROM audit_log
)
UPDATE audit_log a SET seq = o.rn FROM ordered o WHERE a.id = o.id;

SELECT setval('audit_log_seq_seq', coalesce(max(seq), 0) + 1, false) FROM audit_log;

DO $$
DECLARE
    r    RECORD;
    prev TEXT := NULL;
    h    TEXT;
BEGIN
    FOR r IN SELECT id, seq, user_id, action, entity_type, entity_id, created_at
             FROM audit_log ORDER BY seq LOOP
        h := audit_entry_digest(r.seq, r.user_id, r.action, r.entity_type, r.entity_id, r.created_at, prev);
        UPDATE audit_log SET prev_hash = prev, entry_hash = h WHERE id = r.id;
        prev := h;
    END LOOP;
END $$;

ALTER TABLE audit_log
    ALTER COLUMN seq        SET NOT NULL,
    ALTER COLUMN entry_hash SET NOT NULL;
CREATE UNIQUE INDEX idx_audit_log_seq ON audit_log (seq);

-- INSERT: serialize appends (the chain must be linear), pin created_at to the
-- database clock (no backdating), and compute the link. Runs for EVERY insert,
-- including direct SQL — an insider can't append an unchained row.
CREATE FUNCTION audit_log_chain() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(2820115557); -- audit-chain append lock
    NEW.created_at := now();
    NEW.seq        := nextval('audit_log_seq_seq');
    SELECT entry_hash INTO NEW.prev_hash FROM audit_log ORDER BY seq DESC LIMIT 1;
    NEW.entry_hash := audit_entry_digest(NEW.seq, NEW.user_id, NEW.action,
                                         NEW.entity_type, NEW.entity_id,
                                         NEW.created_at, NEW.prev_hash);
    RETURN NEW;
END $$;

CREATE TRIGGER trg_audit_log_chain
    BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_chain();

-- UPDATE: never. The audit log is append-only, full stop.
CREATE FUNCTION audit_log_block_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: rows cannot be modified';
END $$;

CREATE TRIGGER trg_audit_log_no_update
    BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_block_update();

-- DELETE: only rows older than the 90-day evidence floor may go (the nightly
-- retention prune deletes rows older than QUORUM_AUDIT_RETENTION_DAYS >= 90).
-- Fresh evidence cannot be destroyed through any SQL the app can run.
CREATE FUNCTION audit_log_guard_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.created_at > now() - interval '90 days' THEN
        RAISE EXCEPTION 'audit_log rows younger than 90 days cannot be deleted (evidence retention floor)';
    END IF;
    RETURN OLD;
END $$;

CREATE TRIGGER trg_audit_log_guard_delete
    BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_guard_delete();

-- The payments ledger is strictly append-only: a recorded payment can never be
-- altered or removed. Corrections are new, offsetting entries — exactly like a
-- paper ledger an accountant would accept.
CREATE FUNCTION ledger_block_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'transactions is an append-only ledger: % is not allowed; record a correcting entry instead', TG_OP;
END $$;

CREATE TRIGGER trg_transactions_append_only
    BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();

-- An invoice's identity — who owes how much, in what currency, for which
-- period — is frozen at creation. Status, notes, and reminder bookkeeping stay
-- mutable (that is their job). Deleting an invoice would leave a hole in the
-- receivables record, so it is refused outright.
CREATE FUNCTION invoice_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'dues_invoices cannot be deleted: cancel the invoice by status instead';
    END IF;
    IF NEW.amount       IS DISTINCT FROM OLD.amount
       OR NEW.currency     IS DISTINCT FROM OLD.currency
       OR NEW.member_id    IS DISTINCT FROM OLD.member_id
       OR NEW.period_label IS DISTINCT FROM OLD.period_label THEN
        RAISE EXCEPTION 'invoice identity (amount/currency/member/period) is frozen after creation';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER trg_invoice_guard
    BEFORE UPDATE OR DELETE ON dues_invoices
    FOR EACH ROW EXECUTE FUNCTION invoice_guard();

-- Actor attribution must survive account deletion. The old ON DELETE SET NULL
-- behavior would rewrite audit rows (breaking both the append-only rule and
-- the hash chain) and blank the ledger's recorded_by — i.e. deleting an
-- account would anonymize everything that account ever did. RESTRICT instead:
-- an account with recorded history cannot be deleted, only retired (demoted,
-- credentials reset). The API returns 409 with that guidance.
ALTER TABLE audit_log
    DROP CONSTRAINT IF EXISTS audit_log_user_id_fkey,
    ADD CONSTRAINT audit_log_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_recorded_by_fkey,
    ADD CONSTRAINT transactions_recorded_by_fkey
        FOREIGN KEY (recorded_by) REFERENCES users(id) ON DELETE RESTRICT;
