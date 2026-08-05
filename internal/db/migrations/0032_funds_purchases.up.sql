-- CPA-accounting Phase B: purpose-restricted funds and multi-signature
-- purchases (roadmap/cpa-accounting.md §2.3).
--
-- A fund is a named pot with its own GL cash account; its balance is always
-- DERIVED from the ledger, never stored. Spending happens through purchase
-- requests that collect individually-recorded approvals until the fund's
-- policy is satisfied (N approvals AND every named signer), then completion
-- posts DR Expenses / CR fund-cash in the same transaction that closes the
-- request — money cannot leave a fund without hitting the books, and the
-- request row freezes forever once terminal.

CREATE TABLE funds (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name               TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    purpose            TEXT CHECK (purpose IS NULL OR length(purpose) <= 500),
    cash_account_id    UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    approvals_required INT NOT NULL DEFAULT 2 CHECK (approvals_required BETWEEN 1 AND 10),
    active             BOOLEAN NOT NULL DEFAULT true,
    created_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_funds_name ON funds (lower(name));

-- Named signers whose approval is individually REQUIRED (on top of the
-- overall count).
CREATE TABLE fund_signers (
    fund_id UUID NOT NULL REFERENCES funds(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (fund_id, user_id)
);

CREATE TABLE purchase_requests (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fund_id          UUID NOT NULL REFERENCES funds(id) ON DELETE RESTRICT,
    requester_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    amount           BIGINT NOT NULL CHECK (amount > 0),
    currency         TEXT NOT NULL,
    payee            TEXT NOT NULL CHECK (length(payee) BETWEEN 1 AND 120),
    memo             TEXT CHECK (memo IS NULL OR length(memo) <= 500),
    resource_id      UUID REFERENCES resources(id) ON DELETE SET NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'completed', 'rejected', 'cancelled')),
    journal_entry_id UUID REFERENCES journal_entries(id),
    decided_at       TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_purchases_fund ON purchase_requests (fund_id, status, created_at DESC);

-- Each approval is one recorded act of a person: who, when, from where.
CREATE TABLE purchase_approvals (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id  UUID NOT NULL REFERENCES purchase_requests(id) ON DELETE CASCADE,
    approver_id UUID REFERENCES users(id) ON DELETE SET NULL,
    ip          TEXT NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (request_id, approver_id)
);

-- Requests are history: never deleted, frozen once terminal, and a
-- completion must carry its journal entry (the books-link is not optional).
CREATE FUNCTION purchase_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'purchase requests are permanent records: cancel or reject instead of deleting';
    END IF;
    IF OLD.status IN ('completed', 'rejected', 'cancelled') THEN
        RAISE EXCEPTION 'purchase request is % and frozen', OLD.status;
    END IF;
    IF NEW.status = 'completed' AND NEW.journal_entry_id IS NULL THEN
        RAISE EXCEPTION 'a completed purchase must reference its journal entry';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_purchase_guard BEFORE UPDATE OR DELETE ON purchase_requests
    FOR EACH ROW EXECUTE FUNCTION purchase_guard();

-- New posting sources for Phase B.
ALTER TABLE journal_entries DROP CONSTRAINT journal_entries_source_type_check;
ALTER TABLE journal_entries ADD CONSTRAINT journal_entries_source_type_check
    CHECK (source_type IN ('invoice', 'payment', 'writeoff', 'unwaive', 'manual', 'backfill',
                           'purchase', 'fund_transfer'));

-- Fund cash accounts live in 1500–1998, allocated sequentially.
CREATE FUNCTION gl_next_fund_code() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT lpad((coalesce(max(code::int), 1499) + 1)::text, 4, '0')
    FROM accounts WHERE code ~ '^1[5-9][0-9]{2}$' $$;

-- Derived balance of a GL account in one currency.
CREATE FUNCTION gl_balance(p_account UUID, p_currency TEXT) RETURNS BIGINT
LANGUAGE sql STABLE AS $$
    SELECT coalesce(sum(debit) - sum(credit), 0) FROM journal_lines
    WHERE account_id = p_account AND currency = p_currency $$;
