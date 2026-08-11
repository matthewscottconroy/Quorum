-- Governance provenance: a motion may reference the plan it affects, and a
-- decided motion writes the meeting decision log with a link back to itself
-- (so the log entry is traceable and never duplicated).
ALTER TABLE motions
    ADD COLUMN plan_id UUID REFERENCES plans(id) ON DELETE SET NULL;
CREATE INDEX idx_motions_plan ON motions (plan_id) WHERE plan_id IS NOT NULL;

ALTER TABLE meeting_decisions
    ADD COLUMN motion_id UUID REFERENCES motions(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_meeting_decisions_motion
    ON meeting_decisions (motion_id) WHERE motion_id IS NOT NULL;
