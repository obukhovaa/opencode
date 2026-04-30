-- +goose Up

-- Repair: ensure message triggers exist.
-- The initial migration may have silently skipped these on some databases.

-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_messages_updated_at;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_messages_updated_at BEFORE
UPDATE ON messages FOR EACH ROW
SET
  NEW.updated_at = UNIX_TIMESTAMP ();
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_insert AFTER INSERT ON messages FOR EACH ROW
UPDATE sessions
SET
  message_count = message_count + 1
WHERE
  id = NEW.session_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_delete AFTER DELETE ON messages FOR EACH ROW
UPDATE sessions
SET
  message_count = message_count - 1
WHERE
  id = OLD.session_id;
-- +goose StatementEnd

-- Fix messages table collation to match sessions table only if it differs.
-- The initial migration creates the table with utf8mb4/utf8mb4_unicode_ci
-- explicitly, so this should be a no-op on virtually all installs. We guard
-- with INFORMATION_SCHEMA so we never trigger an expensive table rewrite
-- when it is unnecessary.
-- +goose StatementBegin
SET @needs_convert := (
  SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'messages'
    AND TABLE_COLLATION <> 'utf8mb4_unicode_ci'
);
-- +goose StatementEnd

-- +goose StatementBegin
SET @sql := IF(@needs_convert > 0,
  'ALTER TABLE messages CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci',
  'DO 0');
-- +goose StatementEnd

-- +goose StatementBegin
PREPARE stmt FROM @sql;
-- +goose StatementEnd

-- +goose StatementBegin
EXECUTE stmt;
-- +goose StatementEnd

-- +goose StatementBegin
DEALLOCATE PREPARE stmt;
-- +goose StatementEnd

-- Backfill message_count for any sessions that have a stale value.
-- Uses LEFT JOIN to also correct sessions with zero messages.
-- +goose StatementBegin
UPDATE sessions s
LEFT JOIN (
  SELECT session_id, COUNT(*) AS cnt FROM messages GROUP BY session_id
) m ON s.id = m.session_id COLLATE utf8mb4_unicode_ci
SET s.message_count = COALESCE(m.cnt, 0)
WHERE s.message_count != COALESCE(m.cnt, 0);
-- +goose StatementEnd

-- +goose Down
-- No-op: we don't want to remove these triggers on rollback.
