-- Password recovery (admin reset + self-service email token) and TOTP two-factor
-- authentication.

-- Self-service password reset tokens. The plain token is emailed to the user;
-- only its SHA-256 hash is stored. Tokens are single-use and time-limited.
CREATE TABLE password_reset_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_password_reset_hash ON password_reset_tokens (token_hash);
CREATE INDEX idx_password_reset_user ON password_reset_tokens (user_id);

-- TOTP two-factor auth. totp_secret holds the base32 shared secret once set up
-- (whether or not it has been confirmed); totp_enabled is true only after the
-- user verifies a code, at which point 2FA is required at login.
ALTER TABLE users
    ADD COLUMN totp_secret  TEXT,
    ADD COLUMN totp_enabled BOOLEAN NOT NULL DEFAULT FALSE;

-- One-time recovery codes, usable in place of a TOTP code when the device is
-- lost. Stored as SHA-256 hashes; consumed on use.
CREATE TABLE mfa_recovery_codes (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    used      BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX idx_mfa_recovery_user ON mfa_recovery_codes (user_id);
