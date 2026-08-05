-- Agile card metadata: story points, card types with a containment
-- hierarchy, and typed relationships between cards.

ALTER TABLE action_items
    ADD COLUMN story_points INT CHECK (story_points IS NULL OR (story_points >= 0 AND story_points <= 100)),
    ADD COLUMN card_type TEXT NOT NULL DEFAULT 'task'
        CHECK (card_type IN ('epic', 'story', 'task', 'sub_task', 'spike')),
    ADD COLUMN parent_id UUID REFERENCES action_items(id) ON DELETE SET NULL;
CREATE INDEX idx_action_items_parent ON action_items (parent_id);

-- Containment rules, enforced in the database so they hold against direct
-- SQL, not just the API:
--   sub_task  -> parent must be a task, story, or spike
--   story/task/spike -> parent (optional) must be an epic
--   epic      -> no parent
-- Depth is therefore at most epic -> story/task/spike -> sub_task, which
-- also makes parent cycles impossible (no type may parent its own kind).
CREATE FUNCTION card_hierarchy_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_type TEXT;
    bad_children INT;
BEGIN
    IF NEW.parent_id IS NOT NULL THEN
        IF NEW.parent_id = NEW.id THEN
            RAISE EXCEPTION 'a card cannot be its own parent';
        END IF;
        SELECT card_type INTO parent_type FROM action_items WHERE id = NEW.parent_id;
        IF parent_type IS NULL THEN
            RAISE EXCEPTION 'parent card not found';
        END IF;
        IF NEW.card_type = 'sub_task' AND parent_type NOT IN ('task', 'story', 'spike') THEN
            RAISE EXCEPTION 'a sub-task must belong to a task, story, or spike (not a %)', parent_type;
        END IF;
        IF NEW.card_type IN ('story', 'task', 'spike') AND parent_type <> 'epic' THEN
            RAISE EXCEPTION 'a % can only belong to an epic (not a %)', NEW.card_type, parent_type;
        END IF;
        IF NEW.card_type = 'epic' THEN
            RAISE EXCEPTION 'an epic cannot have a parent';
        END IF;
    END IF;
    -- Type changes must not strand existing children.
    IF TG_OP = 'UPDATE' AND NEW.card_type <> OLD.card_type THEN
        SELECT count(*) INTO bad_children FROM action_items c
        WHERE c.parent_id = NEW.id
          AND NOT (
            (c.card_type = 'sub_task' AND NEW.card_type IN ('task', 'story', 'spike')) OR
            (c.card_type IN ('story', 'task', 'spike') AND NEW.card_type = 'epic')
          );
        IF bad_children > 0 THEN
            RAISE EXCEPTION 'cannot change type: % child card(s) would no longer fit the hierarchy', bad_children;
        END IF;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER trg_card_hierarchy
    BEFORE INSERT OR UPDATE OF card_type, parent_id ON action_items
    FOR EACH ROW EXECUTE FUNCTION card_hierarchy_guard();

-- Typed, directed relationships. related_to is stored once and displayed
-- from both sides; depends_on/blocked_by read naturally from the "from"
-- side and inverted from the "to" side ("blocks", "is dependency of").
CREATE TABLE card_links (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_id    UUID NOT NULL REFERENCES action_items(id) ON DELETE CASCADE,
    to_id      UUID NOT NULL REFERENCES action_items(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('depends_on', 'blocked_by', 'related_to')),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_id <> to_id),
    UNIQUE (from_id, to_id, kind)
);
CREATE INDEX idx_card_links_from ON card_links (from_id);
CREATE INDEX idx_card_links_to ON card_links (to_id);
