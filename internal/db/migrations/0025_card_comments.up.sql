-- Conversation threads on work-board cards (action items). The author is a
-- user account; display resolves to their linked member's name when present.
-- ON DELETE SET NULL keeps the conversation readable after an author's
-- account is removed ("former user" client-side).
CREATE TABLE action_item_comments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action_item_id UUID NOT NULL REFERENCES action_items(id) ON DELETE CASCADE,
    author_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    body           TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ai_comments_item ON action_item_comments (action_item_id, created_at);
