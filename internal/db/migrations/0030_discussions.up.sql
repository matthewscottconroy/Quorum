-- Discussions: Slack-style channels with membership and one-level threads.
-- Deliberately NO file uploads here — messages may instead reference a
-- resource-library document by id, which keeps every document under the
-- library's visibility, ledger, and audit machinery.

CREATE TABLE channels (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    topic      TEXT CHECK (topic IS NULL OR length(topic) <= 300),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_channels_name ON channels (lower(name));

-- Membership is by user account; any channel member may add others.
CREATE TABLE channel_members (
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    added_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    joined_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX idx_channel_members_user ON channel_members (user_id);

-- Messages: parent_id NULL = channel root; set = a reply in that root's
-- thread (one level, like Slack). resource_id references a library document;
-- the client resolves it per-viewer so restricted titles never leak.
CREATE TABLE channel_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id  UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    parent_id   UUID REFERENCES channel_messages(id) ON DELETE CASCADE,
    author_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    body        TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    resource_id UUID REFERENCES resources(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_channel_messages_channel ON channel_messages (channel_id, parent_id, created_at);

-- Replies may not nest (Slack model): a parent must itself be a root.
CREATE FUNCTION channel_thread_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE parent_parent UUID; parent_channel UUID;
BEGIN
    IF NEW.parent_id IS NOT NULL THEN
        SELECT parent_id, channel_id INTO parent_parent, parent_channel
        FROM channel_messages WHERE id = NEW.parent_id;
        IF parent_channel IS NULL THEN
            RAISE EXCEPTION 'thread parent not found';
        END IF;
        IF parent_parent IS NOT NULL THEN
            RAISE EXCEPTION 'replies cannot nest: reply to the thread''s first message';
        END IF;
        IF parent_channel <> NEW.channel_id THEN
            RAISE EXCEPTION 'reply must stay in its parent''s channel';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_channel_thread
    BEFORE INSERT OR UPDATE OF parent_id, channel_id ON channel_messages
    FOR EACH ROW EXECUTE FUNCTION channel_thread_guard();
