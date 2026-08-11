DROP INDEX IF EXISTS idx_meeting_decisions_motion;
ALTER TABLE meeting_decisions DROP COLUMN IF EXISTS motion_id;
DROP INDEX IF EXISTS idx_motions_plan;
ALTER TABLE motions DROP COLUMN IF EXISTS plan_id;
