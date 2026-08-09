-- Cards (action items) gain additional contributors beyond the single
-- assignee: a many-to-many link to members. The assignee stays the
-- accountable owner; contributors are everyone else working on it.
CREATE TABLE action_item_contributors (
    action_item_id UUID NOT NULL REFERENCES action_items(id) ON DELETE CASCADE,
    member_id      UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    added_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (action_item_id, member_id)
);
CREATE INDEX idx_card_contributors_member ON action_item_contributors (member_id);
