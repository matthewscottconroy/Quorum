-- Budget scenario planning. A scenario is a named draft budget (e.g. "2027 Base
-- Case", "Austerity"). Each has income/expense line items modeled as
-- quantity × unit amount, so a planner can brainstorm "what if we had 60 members
-- at $50" by editing a quantity. Scenarios can be cloned to explore variations
-- and compared side by side.
CREATE TABLE budget_scenarios (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    description  TEXT,
    period_label TEXT,
    status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    currency     TEXT NOT NULL DEFAULT 'USD',
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A line's contribution is quantity × unit_amount_minor; kind determines whether
-- it adds to income or expense. unit_amount_minor is in the currency's minor
-- units (see money.go) and may be negative (e.g. a discount line).
CREATE TABLE budget_lines (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scenario_id       UUID NOT NULL REFERENCES budget_scenarios(id) ON DELETE CASCADE,
    kind              TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
    category          TEXT,
    label             TEXT NOT NULL,
    quantity          BIGINT NOT NULL DEFAULT 1 CHECK (quantity >= 0),
    unit_amount_minor BIGINT NOT NULL DEFAULT 0,
    note              TEXT,
    sort_order        INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_budget_lines_scenario ON budget_lines (scenario_id);
