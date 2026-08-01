-- Multi-currency reporting. Analytics and budgets previously summed amounts
-- across rows of different currencies at par, which is only correct for a
-- single-currency org. These add an org reporting currency and an
-- effective-dated exchange-rate table so aggregates can convert into one
-- currency (and report anything they can't convert).

-- The single currency that analytics and budget rollups are expressed in.
ALTER TABLE governance_settings
    ADD COLUMN reporting_currency TEXT NOT NULL DEFAULT 'USD';

-- Effective-dated rates. On/after effective_at, 1 major unit of from_currency
-- is worth `rate` major units of to_currency. Newer rows for the same pair
-- supersede older ones (the aggregation picks the latest effective_at <= today).
CREATE TABLE fx_rates (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_currency TEXT NOT NULL CHECK (from_currency <> ''),
    to_currency   TEXT NOT NULL CHECK (to_currency <> ''),
    rate          NUMERIC(24, 10) NOT NULL CHECK (rate > 0),
    effective_at  DATE NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    CHECK (from_currency <> to_currency),
    UNIQUE (from_currency, to_currency, effective_at)
);

-- The aggregation looks up "latest rate for (from -> reporting) as of today",
-- i.e. filters by to_currency + effective_at and orders by effective_at desc.
CREATE INDEX idx_fx_rates_lookup ON fx_rates (to_currency, from_currency, effective_at DESC);
