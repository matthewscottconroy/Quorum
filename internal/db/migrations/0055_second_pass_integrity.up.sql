-- Second-pass integrity round.

-- 1) gl_invoice_remaining now excludes FAILED transactions, matching the
--    application's paid math (recompute, PaidSum). No writer inserts failed
--    rows today, so this is latent — but the first one recorded would have
--    desynced waive write-offs, AR aging, and gl_reconcile_ar from the app.
CREATE OR REPLACE FUNCTION gl_invoice_remaining(p_invoice UUID) RETURNS BIGINT
LANGUAGE sql STABLE AS $$
    SELECT i.amount - coalesce((SELECT sum(t.amount) FROM transactions t
                                WHERE t.invoice_id = i.id AND t.currency = i.currency
                                  AND t.provider_status IS DISTINCT FROM 'failed'), 0)
    FROM dues_invoices i WHERE i.id = p_invoice $$;

-- 2) Corrections to finalized minutes. Robert's Rules allows amending
--    something previously adopted; Quorum's finality made SQL surgery the
--    only recourse. Corrections are APPEND-ONLY errata: the original
--    snapshot stays verbatim, corrections render beneath it with author and
--    timestamp. No UPDATE/DELETE path exists by design — a wrong correction
--    gets its own correction.
CREATE TABLE meeting_corrections (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    body       TEXT NOT NULL CHECK (char_length(body) BETWEEN 1 AND 4000),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_meeting_corrections ON meeting_corrections (meeting_id, created_at);

CREATE FUNCTION corrections_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'minutes corrections are append-only: issue a further correction instead';
END $$;
CREATE TRIGGER trg_corrections_append_only
    BEFORE UPDATE OR DELETE ON meeting_corrections
    FOR EACH ROW EXECUTE FUNCTION corrections_append_only();

-- 3) Calendar feed tokens are hashed at rest from now on (ballot and reset
--    tokens already were): a leaked backup must not yield working feed URLs.
--    Existing plaintext tokens are hashed IN PLACE, so every feed URL users
--    already subscribed with keeps working — lookups hash the presented
--    token before comparing.
UPDATE users
SET calendar_token = encode(sha256(calendar_token::bytea), 'hex')
WHERE calendar_token IS NOT NULL;
