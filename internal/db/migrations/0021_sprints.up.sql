-- Sprints: time-boxed iterations for scoping and tracking work. Action items
-- are the work units — they gain an optional sprint assignment (the kanban
-- board groups them by status; the sprint selector scopes the board).
CREATE TABLE sprints (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    goal       TEXT CHECK (length(goal) <= 2000),
    starts_on  DATE NOT NULL,
    ends_on    DATE NOT NULL,
    status     TEXT NOT NULL DEFAULT 'planned'
               CHECK (status IN ('planned', 'active', 'completed')),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ends_on >= starts_on)
);

ALTER TABLE action_items
    ADD COLUMN sprint_id UUID REFERENCES sprints(id) ON DELETE SET NULL;

CREATE INDEX idx_action_items_sprint ON action_items (sprint_id);
