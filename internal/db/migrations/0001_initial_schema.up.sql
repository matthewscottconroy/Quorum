-- members must come first; users references it.
CREATE TABLE members (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name  TEXT NOT NULL,
    email         TEXT,
    phone         TEXT,
    address       TEXT,
    tier          TEXT NOT NULL DEFAULT 'standard',
    status        TEXT NOT NULL DEFAULT 'active',
    joined_at     DATE NOT NULL DEFAULT CURRENT_DATE,
    notes         TEXT,
    metadata      JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'member',
    member_id     UUID REFERENCES members(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE refresh_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE dues_invoices (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id    UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    amount       NUMERIC(10,2) NOT NULL,
    currency     TEXT NOT NULL DEFAULT 'USD',
    period_label TEXT NOT NULL,
    due_date     DATE NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    notes        TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE transactions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id            UUID REFERENCES dues_invoices(id),
    member_id             UUID REFERENCES members(id),
    amount                NUMERIC(10,2) NOT NULL,
    currency              TEXT NOT NULL DEFAULT 'USD',
    provider              TEXT NOT NULL,
    provider_reference_id TEXT,
    provider_status       TEXT,
    payment_method_type   TEXT,
    recorded_by           UUID REFERENCES users(id),
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    notes                 TEXT,
    raw_payload           JSONB
);

CREATE TABLE meetings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title        TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    location     TEXT,
    agenda       TEXT,
    notes        TEXT,
    status       TEXT NOT NULL DEFAULT 'scheduled',
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE meeting_attendees (
    meeting_id UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    member_id  UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    present    BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (meeting_id, member_id)
);

CREATE TABLE meeting_decisions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id   UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    summary      TEXT NOT NULL,
    detail       TEXT,
    vote_for     INT,
    vote_against INT,
    vote_abstain INT,
    outcome      TEXT NOT NULL DEFAULT 'passed',
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- plans must be defined before action_items.
CREATE TABLE plans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL DEFAULT 'draft',
    owner_id    UUID REFERENCES members(id),
    target_date DATE,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE action_items (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    description TEXT,
    assignee_id UUID REFERENCES members(id),
    meeting_id  UUID REFERENCES meetings(id),
    plan_id     UUID REFERENCES plans(id),
    due_date    DATE,
    status      TEXT NOT NULL DEFAULT 'open',
    priority    TEXT NOT NULL DEFAULT 'normal',
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE plan_decisions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id    UUID NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    summary    TEXT NOT NULL,
    rationale  TEXT,
    decided_by UUID REFERENCES users(id),
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE contacts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    organization TEXT,
    email        TEXT,
    phone        TEXT,
    address      TEXT,
    category     TEXT,
    tags         TEXT[],
    notes        TEXT,
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE resources (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    description TEXT,
    url         TEXT,
    category    TEXT,
    tags        TEXT[],
    added_by    UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID REFERENCES users(id),
    action     TEXT NOT NULL,
    entity_id  TEXT,
    detail     JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency for processed webhook events.
CREATE TABLE processed_events (
    provider_event_id TEXT PRIMARY KEY,
    processed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Full-text search indexes.
CREATE INDEX idx_members_fts   ON members   USING gin(to_tsvector('english', display_name || ' ' || coalesce(email, '')));
CREATE INDEX idx_meetings_fts  ON meetings  USING gin(to_tsvector('english', title || ' ' || coalesce(notes, '')));
CREATE INDEX idx_contacts_fts  ON contacts  USING gin(to_tsvector('english', name || ' ' || coalesce(organization, '')));
CREATE INDEX idx_resources_fts ON resources USING gin(to_tsvector('english', title || ' ' || coalesce(description, '')));

-- Useful lookup indexes.
CREATE INDEX idx_dues_invoices_member   ON dues_invoices (member_id);
CREATE INDEX idx_dues_invoices_status   ON dues_invoices (status);
CREATE INDEX idx_transactions_invoice   ON transactions (invoice_id);
CREATE INDEX idx_action_items_assignee  ON action_items (assignee_id);
CREATE INDEX idx_action_items_status    ON action_items (status);
CREATE INDEX idx_meetings_scheduled_at  ON meetings (scheduled_at);
