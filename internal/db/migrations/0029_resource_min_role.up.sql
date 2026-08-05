-- Role-based resource visibility, alongside the existing group mechanism.
-- NULL keeps today's behavior (all members). A set value hides the resource
-- (and its document) from anyone below that role — including officers and
-- admins when the bar is above them. Groups and role combine as AND:
-- role gates first, then groups refine among members.
ALTER TABLE resources
    ADD COLUMN visible_min_role TEXT
        CHECK (visible_min_role IS NULL OR visible_min_role IN ('member', 'officer', 'admin'));
