-- Continuity Phase E2: the secret-custody registry. METADATA ONLY - where
-- each critical credential lives and who holds it; secret values never
-- enter this system. Attestations ("I verified this copy exists") are
-- recorded acts, like purchase approvals.
CREATE TABLE secret_custody (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    location         TEXT NOT NULL CHECK (length(location) BETWEEN 1 AND 300),
    holder           TEXT NOT NULL CHECK (length(holder) BETWEEN 1 AND 120),
    last_verified_at TIMESTAMPTZ,
    last_verified_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_secret_custody_name ON secret_custody (lower(name));
