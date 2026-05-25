-- name: CreateCronJob :one
INSERT INTO cron_jobs (
    id,
    session_id,
    schedule,
    prompt,
    subagent_type,
    task_title,
    task_id,
    is_recurring,
    source,
    status,
    firing,
    next_run_at,
    run_count,
    created_at,
    updated_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    FALSE,
    ?,
    0,
    strftime('%s', 'now'),
    strftime('%s', 'now')
) RETURNING *;

-- name: GetCronJob :one
SELECT * FROM cron_jobs WHERE id = ? LIMIT 1;

-- name: ListCronJobsBySession :many
SELECT * FROM cron_jobs WHERE session_id = ? ORDER BY created_at DESC;

-- name: ListActiveCronJobs :many
SELECT * FROM cron_jobs WHERE status = 'active' ORDER BY created_at ASC;

-- name: ListDueCronJobs :many
SELECT * FROM cron_jobs
WHERE status = 'active'
  AND firing = FALSE
  AND next_run_at IS NOT NULL
  AND next_run_at <= ?
ORDER BY next_run_at ASC;

-- name: ListMissedOneShots :many
SELECT * FROM cron_jobs
WHERE status = 'active'
  AND is_recurring = FALSE
  AND next_run_at IS NOT NULL
  AND next_run_at < ?
ORDER BY next_run_at ASC;

-- name: CountActiveCronJobsBySession :one
SELECT COUNT(*) FROM cron_jobs
WHERE session_id = ? AND status = 'active';

-- name: SetCronJobFiring :exec
UPDATE cron_jobs SET firing = ? WHERE id = ?;

-- name: ClaimCronJobForFiring :execrows
-- Atomically marks a cron job as firing only if it is still due. Returns the
-- number of rows affected; 0 means another worker already claimed it or the
-- row's next_run_at moved into the future.
UPDATE cron_jobs SET firing = TRUE
WHERE id = ?
  AND status = 'active'
  AND firing = FALSE
  AND next_run_at IS NOT NULL
  AND next_run_at <= ?;

-- name: ClearStaleFiring :exec
UPDATE cron_jobs SET firing = FALSE WHERE firing = TRUE;

-- name: UpdateCronJobAfterRun :one
UPDATE cron_jobs
SET last_run_at = ?,
    run_count = run_count + 1,
    last_result = ?,
    next_run_at = ?,
    status = ?,
    firing = FALSE,
    error = NULL
WHERE id = ?
RETURNING *;

-- name: UpdateCronJobNextRun :exec
UPDATE cron_jobs SET next_run_at = ? WHERE id = ?;

-- name: UpdateCronJobStatus :exec
UPDATE cron_jobs SET status = ? WHERE id = ?;

-- name: UpdateCronJobError :exec
UPDATE cron_jobs SET error = ?, firing = FALSE WHERE id = ?;

-- name: DeleteCronJob :exec
DELETE FROM cron_jobs WHERE id = ?;
