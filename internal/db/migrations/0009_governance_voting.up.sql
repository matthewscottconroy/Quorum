-- Governance & voting: live quorum rules, motions (formal proposals put to a
-- vote), per-member ballots, and meeting proxies. Upgrades the app from merely
-- recording decision tallies after the fact to conducting the vote.

-- Single-row org-wide governance settings. The id CHECK keeps it a singleton.
CREATE TABLE governance_settings (
    id                          INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    -- quorum_mode: how the quorum threshold is derived from the active roster.
    --   'majority' → floor(active/2)+1
    --   'percent'  → ceil(active * quorum_value / 100)
    --   'fixed'    → quorum_value (an absolute member count)
    quorum_mode                 TEXT    NOT NULL DEFAULT 'majority'
                                CHECK (quorum_mode IN ('majority', 'percent', 'fixed')),
    quorum_value                INTEGER NOT NULL DEFAULT 0 CHECK (quorum_value >= 0),
    -- When true, a member represented by a present proxy holder counts toward quorum.
    proxies_count_toward_quorum BOOLEAN NOT NULL DEFAULT TRUE,
    -- Default passing threshold applied to new motions.
    default_threshold           TEXT    NOT NULL DEFAULT 'majority'
                                CHECK (default_threshold IN ('majority', 'two_thirds', 'unanimous')),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO governance_settings (id) VALUES (1) ON CONFLICT DO NOTHING;

-- A motion is a formal proposal put to a vote within a meeting. Its lifecycle:
--   draft → seconded → open → (carried | failed | tabled | withdrawn)
-- The tally is computed from motion_votes; outcome is set when the motion closes.
CREATE TABLE motions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id  UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    detail      TEXT,
    mover_id    UUID REFERENCES members(id) ON DELETE SET NULL,
    seconder_id UUID REFERENCES members(id) ON DELETE SET NULL,
    threshold   TEXT NOT NULL DEFAULT 'majority'
                CHECK (threshold IN ('majority', 'two_thirds', 'unanimous')),
    status      TEXT NOT NULL DEFAULT 'draft'
                CHECK (status IN ('draft', 'seconded', 'open', 'carried', 'failed', 'tabled', 'withdrawn')),
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    opened_at   TIMESTAMPTZ,
    closed_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_motions_meeting ON motions (meeting_id);

-- One ballot per member per motion. A proxy vote is recorded as the grantor's
-- ballot with is_proxy = true and cast_by set to the holder's user account.
CREATE TABLE motion_votes (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    motion_id UUID NOT NULL REFERENCES motions(id) ON DELETE CASCADE,
    member_id UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    choice    TEXT NOT NULL CHECK (choice IN ('for', 'against', 'abstain')),
    is_proxy  BOOLEAN NOT NULL DEFAULT FALSE,
    cast_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    cast_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_motion_votes_unique ON motion_votes (motion_id, member_id);

-- Proxy assignments scoped to a meeting: holder may cast the grantor's ballot.
CREATE TABLE meeting_proxies (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    grantor_id UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    holder_id  UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT meeting_proxies_distinct CHECK (grantor_id <> holder_id)
);
CREATE UNIQUE INDEX idx_meeting_proxies_grantor ON meeting_proxies (meeting_id, grantor_id);
CREATE INDEX idx_meeting_proxies_meeting ON meeting_proxies (meeting_id);
