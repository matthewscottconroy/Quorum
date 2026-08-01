-- Visibility groups: named sets of members that constrain who can see a
-- resource (document / link / media in the Resources library).
--
-- Semantics, enforced in the repo's List/Get:
--   * a resource with NO groups is visible to every member (the old behavior,
--     so existing resources are unaffected);
--   * a resource with groups is visible only to users whose linked member
--     belongs to at least one of them;
--   * officers and above always see everything (they curate the library, and
--     a hidden-from-the-librarian shelf is how documents get lost).
CREATE TABLE groups (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 80),
    description TEXT CHECK (length(description) <= 500),
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE group_members (
    group_id  UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    member_id UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, member_id)
);
CREATE INDEX idx_group_members_member ON group_members (member_id);

CREATE TABLE resource_groups (
    resource_id UUID NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    group_id    UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (resource_id, group_id)
);
CREATE INDEX idx_resource_groups_group ON resource_groups (group_id);
