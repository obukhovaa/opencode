-- +goose Up
-- +goose StatementBegin

-- Repair: ensure message triggers exist.
-- The initial migration may have silently skipped these on some databases.
CREATE TRIGGER IF NOT EXISTS update_messages_updated_at
AFTER UPDATE ON messages
BEGIN
UPDATE messages SET updated_at = strftime('%s', 'now')
WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS update_session_message_count_on_insert
AFTER INSERT ON messages
BEGIN
UPDATE sessions SET
    message_count = message_count + 1
WHERE id = new.session_id;
END;

CREATE TRIGGER IF NOT EXISTS update_session_message_count_on_delete
AFTER DELETE ON messages
BEGIN
UPDATE sessions SET
    message_count = message_count - 1
WHERE id = old.session_id;
END;

-- Backfill message_count for any sessions that have a stale value.
-- Only update rows where the count differs to avoid firing the updated_at trigger unnecessarily.
UPDATE sessions SET message_count = (
    SELECT COUNT(*) FROM messages WHERE messages.session_id = sessions.id
)
WHERE message_count != (
    SELECT COUNT(*) FROM messages WHERE messages.session_id = sessions.id
);

-- +goose StatementEnd

-- +goose Down
-- No-op: we don't want to remove these triggers on rollback.
