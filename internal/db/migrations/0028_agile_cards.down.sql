DROP TABLE card_links;
DROP TRIGGER trg_card_hierarchy ON action_items;
DROP FUNCTION card_hierarchy_guard();
ALTER TABLE action_items
    DROP COLUMN story_points,
    DROP COLUMN card_type,
    DROP COLUMN parent_id;
