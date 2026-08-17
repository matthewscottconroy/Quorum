-- Token hashing is one-way (the plaintext is gone); rotating re-mints.
DROP TRIGGER IF EXISTS trg_corrections_append_only ON meeting_corrections;
DROP FUNCTION IF EXISTS corrections_append_only();
DROP TABLE IF EXISTS meeting_corrections;
CREATE OR REPLACE FUNCTION gl_invoice_remaining(p_invoice UUID) RETURNS BIGINT
LANGUAGE sql STABLE AS $$
    SELECT i.amount - coalesce((SELECT sum(t.amount) FROM transactions t
                                WHERE t.invoice_id = i.id AND t.currency = i.currency), 0)
    FROM dues_invoices i WHERE i.id = p_invoice $$;
