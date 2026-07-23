# Tasks — Rename Session

## 1. Database schema & migrations

- [x] 1.1 Add SQLite migration `internal/db/migrations/sqlite/<ts>_add_user_set_title.sql`: `-- +goose Up` `ALTER TABLE sessions ADD COLUMN user_set_title BOOLEAN NOT NULL DEFAULT FALSE;` and `-- +goose Down` dropping the column (mirror `20260627120000_add_message_synthetic.sql`)
- [x] 1.2 Add MySQL migration `internal/db/migrations/mysql/<ts>_add_user_set_title.sql`: `ALTER TABLE sessions ADD COLUMN user_set_title TINYINT(1) NOT NULL DEFAULT 0;` up / drop down (use the SAME timestamp as 1.1)
- [x] 1.3 Add `user_set_title TINYINT(1) NOT NULL DEFAULT 0,` to the `sessions` table in the sqlc schema source `internal/db/schema/mysql.sql`

## 2. Queries & sqlc regeneration

- [x] 2.1 SQLite queries `internal/db/sql/sessions.sql`: add `-- name: RenameSession :one` `UPDATE sessions SET title = ?, user_set_title = TRUE WHERE id = ? RETURNING *;` and `-- name: SetGeneratedTitle :execrows` `UPDATE sessions SET title = ? WHERE id = ? AND user_set_title = FALSE;` (leave `UpdateSession` untouched)
- [x] 2.2 MySQL queries `internal/db/sql/mysql/sessions.sql`: add `-- name: RenameSession :execresult` (row returned via adapter, like `UpdateSession`) and `-- name: SetGeneratedTitle :execrows` (MySQL sqlc supports `:execrows` → `(int64, error)`, see `ClaimCronJobForFiring`), same SQL using `1`/`0` for the boolean literals
- [x] 2.3 Run `sqlc generate` (v1.30.0); confirm `git diff` on `internal/db/models.go` (adds `Session.UserSetTitle bool` in both `db` and `mysqldb`), `internal/db/querier.go` (adds `RenameSession`, `SetGeneratedTitle`), `internal/db/sessions.sql.go`, `internal/db/mysql/sessions.sql.go` — and nothing unrelated
- [x] 2.4 Hand-write the MySQL adapter methods in `internal/db/mysql_querier.go`: `RenameSession(ctx, arg) (Session, error)` — run the `:execresult` query then `GetSessionByID`, mirroring `UpdateSession`; `SetGeneratedTitle(ctx, arg) (int64, error)` — trivial direct delegation to `q.queries.SetGeneratedTitle(...)`, mirroring `ClaimCronJobForFiring`
- [x] 2.5 `go build ./internal/db/...` compiles (both wrappers still satisfy `QuerierWithTx`)

## 3. Session service

- [x] 3.1 Add `UserSetTitle bool` to `session.Session` (`internal/session/session.go`) and map it in `fromDBItem`
- [x] 3.2 Add `Rename(ctx, id, title string) (Session, error)`: trim title, return an error on empty/whitespace-only, call `q.RenameSession`, `Publish(pubsub.UpdatedEvent, session)`, return the session
- [x] 3.3 Add `SetGeneratedTitle(ctx, id, title string) (Session, error)`: call `q.SetGeneratedTitle`; if rows-affected > 0 → `GetSessionByID` + `Publish(pubsub.UpdatedEvent, ...)` and return it; if 0 → return the current session (via `Get`) without publishing (user title already won)
- [x] 3.4 Add `Rename` and `SetGeneratedTitle` to the `Service` interface

## 4. Guard automatic title generation

- [x] 4.1 In `agent.generateTitle` (`internal/llm/agent/agent.go`), after `a.sessions.Get`, early-return `nil` when `sess.UserSetTitle` is true (skip the descriptor call)
- [x] 4.2 Replace the `sess.Title = title` + `a.sessions.Save(ctx, sess)` write with `a.sessions.SetGeneratedTitle(ctx, sessionID, title)` so the write is guarded by `user_set_title = FALSE`

## 5. TUI `/rename` command

- [x] 5.1 Add a `CommandInfo{ID:"rename", Title:"Rename Session", Description:"Rename the current session", ArgumentHint:"[new title]", TUIOnly:true}` to `BuiltinCommands()` in `internal/slashcmd/builtin.go`
- [x] 5.2 In `buildCommands()` handlers map (`internal/tui/tui.go`), add a `"rename"` handler returning `dialog.ShowMultiArgumentsDialogMsg{CommandID:"rename", ArgNames:[]string{"title"}, ArgHints:{"title":"New session title"}}` (mirror `"loop"`)
- [x] 5.3 Extend the `dialog.CommandRunCustomMsg` case (~`tui.go:501`) to dispatch `CommandID == "rename"` to a new `handleRenameCommand(msg.Args)`
- [x] 5.4 Implement `handleRenameCommand(args map[string]string) tea.Cmd`: warn if `selectedSession.ID == ""`; trim `args["title"]`, warn if empty; call `a.app.Sessions.Rename(context.Background(), id, title)`; report success/error via `util.ReportInfo`/`util.InfoMsg`. Do NOT mutate `a.selectedSession` in the closure — the `pubsub.UpdatedEvent` path (`tui.go:642`) refreshes it

## 6. Bridge `/rename` command

- [x] 6.1 Register `"rename": s.cmdRename` in `ChatCommands()` (`internal/bridge/service/commands.go`)
- [x] 6.2 Implement `cmdRename(ctx, in bridge.Inbound) *bridge.CommandReply`: `args := strings.TrimSpace(in.CommandArgs)`; if empty → `replyText("Usage: /rename <new title>")`; `resolveBinding(ctx, in.Peer)` → session id; if the binding resolves to no/empty `SessionID`, reply that there is no session to rename (do NOT create one); else `s.app.Sessions.Rename(ctx, binding.SessionID, args)`; on success `replyText("Session renamed to: " + args)`, on error a failure reply (mirror `cmdSession` error handling)
- [x] 6.3 If a bridge help/command listing enumerates commands, add `/rename` there for discoverability

## 7. API consistency

- [x] 7.1 In `handleSessionUpdate` (`internal/api/handler_session.go`), change the `req.Title != nil` branch to call `s.app.Sessions.Rename(ctx, sessionID, *req.Title)` instead of `Save`, preserving the permission-only-PATCH behavior (still skip when `req.Title == nil`). Note the intentional contract change: an empty/whitespace title now returns `400 Bad Request` (previously it silently set an empty title); map `Rename`'s validation error to `writeError(w, http.StatusBadRequest, ...)`. Update the function's doc comment accordingly

## 8. Tests

- [x] 8.1 Session service test: `Rename` trims and rejects empty; sets `UserSetTitle`; publishes `UpdatedEvent`
- [x] 8.2 Session service test: `SetGeneratedTitle` writes when not user-titled, no-ops (no title change, no publish) when `UserSetTitle` is true
- [x] 8.3 Agent test (or service-level): a renamed session's `generateTitle` does not overwrite the title (guard + guarded write); an un-renamed session still gets a generated title
- [x] 8.4 Bridge command test: `/rename Foo` renames the bound session and replies; `/rename` with no arg replies usage and does not change the title (follow existing `commands_test.go` patterns)
- [x] 8.5 API handler test: `PATCH /session/{id}` with a non-empty title sets `UserSetTitle`; an empty/whitespace title returns `400`; permission-only PATCH leaves title and flag unchanged
- [x] 8.6 Run `go test ./internal/db/... ./internal/session/... ./internal/llm/agent/... ./internal/bridge/... ./internal/api/...` and fix fallout; run `make test` for the final gate

## 9. Verification

- [x] 9.1 `openspec validate rename-session --strict` passes
- [x] 9.2 Manual/e2e sanity: rename a brand-new (0-message) session, then send the first message → the user title survives (auto-generation is suppressed)
