-- name: GetFile :one
SELECT *
FROM files
WHERE id = ? LIMIT 1;

-- name: GetFileByPathAndSession :one
SELECT *
FROM files
WHERE path = ? AND session_id = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: ListFilesBySession :many
SELECT *
FROM files
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: ListFilesByPath :many
SELECT *
FROM files
WHERE path = ?
ORDER BY created_at DESC;

-- name: CreateFile :execresult
INSERT INTO files (
    id,
    session_id,
    path,
    content,
    version,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, UNIX_TIMESTAMP(), UNIX_TIMESTAMP()
);

-- name: UpdateFile :execresult
UPDATE files
SET
    content = ?,
    version = ?,
    updated_at = UNIX_TIMESTAMP()
WHERE id = ?;

-- name: DeleteFile :exec
DELETE FROM files
WHERE id = ?;

-- name: DeleteSessionFiles :exec
DELETE FROM files
WHERE session_id = ?;

-- name: ListLatestSessionFiles :many
SELECT f.*
FROM files f
INNER JOIN (
    SELECT session_id, path, MAX(created_at) as max_created_at
    FROM files
    WHERE files.session_id = ?
    GROUP BY session_id, path
) latest ON f.session_id = latest.session_id AND f.path = latest.path AND f.created_at = latest.max_created_at
ORDER BY f.path;

-- name: ListFilesBySessionTree :many
SELECT f.*
FROM files f
INNER JOIN sessions s ON f.session_id = s.id
WHERE s.root_session_id = ?
ORDER BY f.created_at ASC;

-- name: ListLatestSessionTreeFiles :many
SELECT f.*
FROM files f
INNER JOIN sessions s ON f.session_id = s.id
INNER JOIN (
    SELECT si.root_session_id, fi.path, MAX(fi.created_at) as max_created_at
    FROM files fi
    INNER JOIN sessions si ON fi.session_id = si.id
    WHERE si.root_session_id = ?
    GROUP BY si.root_session_id, fi.path
) latest ON s.root_session_id = latest.root_session_id AND f.path = latest.path AND f.created_at = latest.max_created_at
ORDER BY f.path;
