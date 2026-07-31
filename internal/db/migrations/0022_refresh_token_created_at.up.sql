-- Sliding idle-session enforcement needs to know when each refresh token was
-- minted. Rotation issues a fresh token on every refresh, so created_at is
-- effectively "last activity": a token older than the idle window means the
-- session sat unused that long and must re-authenticate.
ALTER TABLE refresh_tokens
    ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
