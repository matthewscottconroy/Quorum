-- Custom kanban columns. Cards (action items) keep `status` as the canonical
-- reporting field (dashboards, "open items", sprint progress); columns are
-- workflow lanes layered on top. A column may map to a status, in which case
-- dropping a card into it also advances the status; unmapped columns (e.g.
-- "Blocked", "Reviewing") move the card without touching status.
CREATE TABLE board_columns (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    position       INT  NOT NULL DEFAULT 0,
    maps_to_status TEXT CHECK (maps_to_status IN ('open', 'in_progress', 'done', 'cancelled')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_board_columns_name ON board_columns (lower(name));

-- A card with no explicit column renders in the column mapping its status,
-- so existing boards look identical after this migration.
ALTER TABLE action_items
    ADD COLUMN column_id UUID REFERENCES board_columns(id) ON DELETE SET NULL;
CREATE INDEX idx_action_items_column ON action_items (column_id);

INSERT INTO board_columns (name, position, maps_to_status) VALUES
    ('Open',        10, 'open'),
    ('In progress', 20, 'in_progress'),
    ('Done',        30, 'done');
