-- Async ballot links: an officer emails a single-use, time-limited tokenized
-- link for an OPEN motion; the recipient casts their vote through a public page
-- without needing an app session. This lets rank-and-file / restricted members
-- vote remotely. One live token per member per motion (resendable).
CREATE TABLE ballot_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    motion_id  UUID NOT NULL REFERENCES motions(id) ON DELETE CASCADE,
    member_id  UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    used       BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_ballot_tokens_hash ON ballot_tokens (token_hash);
CREATE UNIQUE INDEX idx_ballot_tokens_motion_member ON ballot_tokens (motion_id, member_id);
