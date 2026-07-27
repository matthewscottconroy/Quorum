DROP TABLE IF EXISTS mfa_recovery_codes;
ALTER TABLE users
    DROP COLUMN IF EXISTS totp_enabled,
    DROP COLUMN IF EXISTS totp_secret;
DROP TABLE IF EXISTS password_reset_tokens;
