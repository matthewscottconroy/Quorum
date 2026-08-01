DROP INDEX IF EXISTS idx_action_items_sprint;
ALTER TABLE action_items DROP COLUMN IF EXISTS sprint_id;
DROP TABLE IF EXISTS sprints;
