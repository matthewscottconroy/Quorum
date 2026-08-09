-- Engagement features: meeting RSVPs, member-reported payments (an officer
-- confirmation queue), per-user calendar-subscription tokens, and report
-- subscriptions. All additive; nothing here alters existing tables' data.

-- RSVPs: a member's intent for an upcoming meeting, distinct from recorded
-- attendance (which an officer sets after the fact). 'yes'/'no'/'maybe'.
CREATE TABLE meeting_rsvps (
    meeting_id UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    member_id  UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    response   TEXT NOT NULL CHECK (response IN ('yes', 'no', 'maybe')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (meeting_id, member_id)
);

-- Member-reported payments: a self-service "I've sent payment via Zelle/check"
-- signal for an invoice, giving officers a confirmation queue instead of an
-- inbox. Confirming records the real transaction; dismissing clears it.
CREATE TABLE payment_reports (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id   UUID NOT NULL REFERENCES dues_invoices(id) ON DELETE CASCADE,
    member_id    UUID REFERENCES members(id) ON DELETE SET NULL,
    method       TEXT NOT NULL,               -- free text: zelle, check, venmo...
    reference    TEXT,                         -- optional confirmation number
    note         TEXT,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed', 'dismissed')),
    reported_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at  TIMESTAMPTZ,
    resolved_by  UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_payment_reports_status ON payment_reports (status, reported_at);
-- At most one open report per invoice (a member can't spam the queue).
CREATE UNIQUE INDEX idx_payment_reports_one_open ON payment_reports (invoice_id)
    WHERE status = 'pending';

-- Calendar-subscription token: a per-user opaque secret embedded in a
-- read-only ICS feed URL so meetings appear in the user's calendar app.
-- Rotatable; stored hashed would preclude the URL, so it's a random token
-- treated like a scoped read credential (meetings are member-visible anyway).
ALTER TABLE users ADD COLUMN calendar_token TEXT UNIQUE;

-- Report subscriptions: which scheduled digests each user wants. The nightly
-- job reads these and emails matching reports on their cadence.
CREATE TABLE report_subscriptions (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    report      TEXT NOT NULL,   -- ar_aging | ap_aging | income_statement
    cadence     TEXT NOT NULL CHECK (cadence IN ('weekly', 'monthly')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, report)
);
