-- +goose Up
ALTER TABLE cron_jobs ADD COLUMN requester VARCHAR(320) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE cron_jobs DROP COLUMN requester;
