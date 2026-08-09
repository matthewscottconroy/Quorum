-- Org-maturity features: office terms (who holds which office, with history),
-- committees, conflict-of-interest recusals, membership applications,
-- and invoice installment plans. All additive.

-- Office terms: an informational office title held by a member over a period.
-- Distinct from the permission role (which stays the security boundary) — this
-- captures "Treasurer 2024–2025" for the org chart and continuity, not access.
CREATE TABLE office_terms (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id  UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    title      TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
    started_on DATE NOT NULL DEFAULT current_date,
    ended_on   DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ended_on IS NULL OR ended_on >= started_on)
);
CREATE INDEX idx_office_terms_member ON office_terms (member_id);
-- One member holds a given title once at a time (no overlapping open terms).
CREATE UNIQUE INDEX idx_office_terms_current ON office_terms (member_id, title)
    WHERE ended_on IS NULL;

-- Committees: a named working group with an optional chair and purpose. An
-- organizational construct (reporting/roster), not a permission scope.
CREATE TABLE committees (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 80),
    purpose    TEXT CHECK (purpose IS NULL OR length(purpose) <= 1000),
    chair_id   UUID REFERENCES members(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE committee_members (
    committee_id UUID NOT NULL REFERENCES committees(id) ON DELETE CASCADE,
    member_id    UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    PRIMARY KEY (committee_id, member_id)
);

-- Conflict-of-interest recusals: a member records that they are recusing from
-- a specific motion or purchase. Advisory + auditable; recorded in minutes.
CREATE TABLE recusals (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type TEXT NOT NULL CHECK (subject_type IN ('motion', 'purchase')),
    subject_id   UUID NOT NULL,
    member_id    UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    reason       TEXT CHECK (reason IS NULL OR length(reason) <= 500),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (subject_type, subject_id, member_id)
);
CREATE INDEX idx_recusals_subject ON recusals (subject_type, subject_id);

-- Membership applications: a public join request feeding an admin queue.
CREATE TABLE join_requests (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    email       TEXT NOT NULL CHECK (length(email) BETWEEN 3 AND 200),
    message     TEXT CHECK (message IS NULL OR length(message) <= 1000),
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    resolved_by UUID REFERENCES users(id) ON DELETE SET NULL,
    member_id   UUID REFERENCES members(id) ON DELETE SET NULL  -- set when approved
);
CREATE INDEX idx_join_requests_status ON join_requests (status, created_at);

-- Invoice installment plans: split one invoice into scheduled partial payments.
-- Purely a schedule/tracking aid; payments still post through transactions and
-- the invoice's own paid/partial status is authoritative.
CREATE TABLE invoice_installments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES dues_invoices(id) ON DELETE CASCADE,
    amount     BIGINT NOT NULL CHECK (amount > 0),
    due_date   DATE NOT NULL,
    seq        INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (invoice_id, seq)
);
CREATE INDEX idx_installments_invoice ON invoice_installments (invoice_id);
