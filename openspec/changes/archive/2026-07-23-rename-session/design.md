## Context

A session's title lives on the `sessions` row (`internal/db` `Session.Title`, domain `session.Session.Title`) and is the sole user-facing name. It is written in three situations today:

1. **Creation** — `Sessions.Create/CreateWithID` with a placeholder: `"New Session"` (TUI, `internal/tui/page/chat.go:352`), `"chat-bridge"` (bridge, `internal/bridge/service/bind.go:107`), the request value (API), or `""` (ACP).
2. **Automatic generation** — `agent.generateTitle` (`internal/llm/agent/agent.go:451`) asks the `descriptor` model for a ≤50-char title and calls `Sessions.Save`. It is fired **asynchronously** from `processGeneration` (`agent.go:642`) **only when `len(msgs) == 0`** (first turn of a session), with **no check** for an already-set title.
3. **Generic save** — `Sessions.Save` (`internal/session/session.go:201`) writes title + token counts + summary + cost via the single `UpdateSession` query and publishes `pubsub.UpdatedEvent`.

Persistence is dual-provider via sqlc: SQLite (`db` package, schema = `internal/db/migrations/sqlite/`, queries = `internal/db/sql/`) and MySQL (`mysqldb` package, schema = `internal/db/schema/mysql.sql`, queries = `internal/db/sql/mysql/`). A hand-written adapter `MySQLQuerier` (`internal/db/mysql_querier.go`) makes `mysqldb` satisfy the unified `db.Querier` interface; because MySQL has no `RETURNING`, its adapter methods run the write as `:execresult` then `GetSessionByID` to return the row. sqlc is regenerated manually (`sqlc generate`, v1.30.0), not via `go generate`.

Consumers of the title and how they stay current: TUI top bar / sidebar / `appModel.selectedSession` subscribe to `pubsub.Event[session.Session]` and update on `UpdatedEvent` (`internal/tui/tui.go:642`); the API SSE stream subscribes to the session broker and emits `session.updated` (`internal/api/handler_event.go`); the bridge `/sessions` & `/session` and API `GET` read on demand. So a title write that publishes `UpdatedEvent` already fans out to every consumer.

## Goals / Non-Goals

**Goals:**
- A deliberate rename that sets the user-facing title and *sticks* — never silently overwritten by automatic title generation, even under the async race.
- Rename reachable from the TUI (`/rename`) and the bridge (`/rename <new name>`), consistent with the existing API title update.
- New title visible across all consumers immediately (live) or on next read (on-demand), reusing existing broadcast machinery.
- Additive, backward-compatible schema change; no config/`opencode-schema.json` surface.

**Non-Goals:**
- Renaming anything other than the user-facing `title` (no id/slug/summary changes; the summary and token/cost fields are untouched).
- Changing when or how automatic titles are generated for un-renamed sessions.
- Bulk rename, rename history/undo, per-title validation beyond non-empty/trim, or length enforcement beyond the existing DB column limits (SQLite unlimited `TEXT`; MySQL `VARCHAR(512)`).
- Renaming child/flow/task sessions specially — the operation targets whatever session id it is given.

## Decisions

### 1. Persisted `user_set_title` boolean, not a title-content heuristic
New sessions start with a **non-empty** placeholder (`"New Session"`, `"chat-bridge"`), so "auto-generate only when title is empty" would break generation for the common TUI and bridge flows. Instead add `sessions.user_set_title BOOLEAN NOT NULL DEFAULT FALSE` (SQLite) / `TINYINT(1) NOT NULL DEFAULT 0` (MySQL), mirroring the existing `messages.synthetic` bool precedent (sqlc maps both to Go `bool`). The flag is an explicit, durable record of user intent, independent of the title string. Domain `session.Session` gains `UserSetTitle bool`, mapped in `fromDBItem`.

*Alternative considered:* an in-memory guard — rejected: does not survive restart and cannot close the DB-level race.

### 2. Three distinct write paths; the generic `UpdateSession` never touches the flag
- **Generic save** (`UpdateSession`): unchanged — it does not read or write `user_set_title`, so token/cost/summary saves preserve whatever flag value is in the row. (`RETURNING *` / the MySQL adapter's `GetSessionByID` pick up the new column automatically after regen.)
- **Rename**: a new query sets `title = ?, user_set_title = TRUE WHERE id = ?`.
- **Automatic generation**: a new *guarded* query sets `title = ? WHERE id = ? AND user_set_title = FALSE`.

Keeping the flag out of `UpdateSession` is what makes a stale `Save` from the title generator unable to reset the flag; only the two dedicated queries move it. The other `Save` callers — compaction/summary writes at `agent.go:1799/2010/2182` — never set `.Title` and never touch `user_set_title`, so they preserve both a user title and its mark.

### 3. Race-free guard via a conditional UPDATE, not just an in-code check
`generateTitle` will (a) early-return if the freshly-read session is already `UserSetTitle` (avoids a wasted descriptor LLM call), and (b) write through the guarded query `... WHERE user_set_title = FALSE`. Even if a rename commits *after* the generator's initial read but *before* its write, the guarded UPDATE matches zero rows and the user's title stands. This satisfies the spec's "rename wins over an in-flight generation" scenario without locking.

### 4. sqlc return-shape handling for the guarded query
A conditional UPDATE can match zero rows, so it must **not** be modeled as SQLite `:one` + `RETURNING *` (sqlc `:one` treats zero rows as `sql.ErrNoRows`). Model it as SQLite `:execrows` / MySQL `:execresult` returning rows-affected. The service's guarded setter (e.g. `SetGeneratedTitle`) then: if affected > 0, `GetSessionByID` and publish `UpdatedEvent`; if affected == 0, no-op (the user title already won). The rename query targets a single id with no predicate, so it can stay `:one` + `RETURNING *` (SQLite) / `:execresult` + `GetSessionByID` (MySQL adapter), mirroring `UpdateSession`.

### 5. Unify all explicit renames through one service method
Add `Rename(ctx, id, title) (Session, error)` to `session.Service` (trim, reject empty, run the rename query, publish `UpdatedEvent`). The TUI handler, the bridge `cmdRename`, and the API `handleSessionUpdate` title branch all call it. This is what guarantees the spec's "explicit title updates across entry points are consistent" — every one of them marks the session user-titled. The API currently calls `Save` for the title branch; switching it to `Rename` additionally sets the flag. One intentional contract change: `Rename` rejects empty/whitespace titles, so `PATCH {title:""}` — which today silently sets an empty title — now returns `400 Bad Request`. This is a deliberate tightening (an empty user-facing name is not useful) and is the only behavioral difference for the API's title branch; non-empty updates are unchanged aside from the added flag.

### 6. Cross-consumer consistency is free via the existing broadcast
`Rename` and the guarded setter publish `pubsub.UpdatedEvent` with the fresh session, exactly as `Save` does. That single event already drives the TUI subscriptions (top bar, sidebar, `selectedSession`) and the SSE `session.updated` frame; on-demand consumers (bridge/API reads) always fetch fresh. No new event type or wiring is needed.

### 7. TUI: rely on the pubsub refresh, not local model mutation
`appModel.Update` uses a value receiver, so a handler must not mutate `a.selectedSession` inside a returned `tea.Cmd` closure (the mutation would be lost). The `/rename` handler only calls `Sessions.Rename` and reports a toast; the `pubsub.UpdatedEvent` path at `tui.go:642` updates `selectedSession`/top bar within `Update`. The command mirrors `/loop`: a `CommandInfo` in `internal/slashcmd/builtin.go` (`TUIOnly: true`, `ArgumentHint`) + a handler returning `dialog.ShowMultiArgumentsDialogMsg{CommandID:"rename", ArgNames:["title"]}`, dispatched from the `dialog.CommandRunCustomMsg` case to a new `handleRenameCommand`.

## Risks / Trade-offs

- **Concurrent generic `Save` racing a rename on the title field** → A `Save` carrying a stale `Title` could overwrite a rename (both write `title`). This is a pre-existing property of `Save` (it already races auto-generated titles the same way) and is bounded — `Save` callers `Get`-then-`Save`, and the only frequent title-bearing writer (title generation) is now guarded. Not solved here; noted so a future change can make `UpdateSession` stop writing `title` if needed.
- **sqlc regeneration drift** → Regenerate with the pinned `sqlc v1.30.0` and commit `internal/db/*.sql.go`, `internal/db/mysql/*.sql.go`, `models.go`, `querier.go` together with the migration; verify `git diff` shows only the new column/queries. The MySQL adapter method(s) in `mysql_querier.go` are hand-written and must be added to match the new `db.Querier` methods or the build breaks.
- **MySQL migration vs consolidated schema** → MySQL has both a runtime goose migration (`internal/db/migrations/mysql/`) and a sqlc schema source (`internal/db/schema/mysql.sql`); both must gain the column or codegen and runtime diverge.
- **Empty/very long titles** → Non-empty is enforced in `Rename`; length is left to the DB column (MySQL truncates/errors at 512). Acceptable; matches current auto-title behavior (≤50 chars) not being enforced elsewhere.

## Migration Plan

Additive column with a `FALSE`/`0` default: existing rows read as not-user-titled (so auto-generation keeps working for them), and older binaries ignore the column. Deploy = run the new goose migration (SQLite auto-applies from embedded migrations; MySQL via the existing migration path). Rollback = revert the code; the extra column is harmless to an older binary, or drop it via the migration's `-- +goose Down`.

## Open Questions

- None blocking. Length capping (e.g. trim to 512 before the MySQL write) can be added if we see truncation errors in practice; deferred since the API title update already accepts arbitrary lengths today.
