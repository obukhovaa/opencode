## Why

A session's title is the only user-facing name for a conversation, but today it can only be set by the async descriptor agent that auto-generates a title from the first user message. Users cannot correct or choose it. Worse, the auto-generator (`agent.generateTitle`) overwrites the title unconditionally when a session has no messages yet, so even if we let a user set a title, the next turn's title generation would silently clobber it. Users need a deliberate rename that sticks and shows up everywhere the session is displayed.

## What Changes

- Add a **rename** operation on the session service that sets the user-facing `title` and marks the session as user-titled so auto-generation never overwrites it again.
- Persist a new `user_set_title` boolean on the `sessions` table (SQLite + MySQL). The async title generator SHALL skip / no-op when this flag is set, closing the clobber race at the database level.
- Add a **TUI `/rename` slash command**: selecting it opens a single-field input (mirroring `/loop`), and submitting renames the active session.
- Add a **bridge `/rename <new name>` chat command** (Slack / Telegram / Mattermost): the argument is the new title, applied to the peer's currently bound session.
- Route the existing **API `PATCH /session/{id}` title update** through the same rename path so every explicit title set is consistent (flag-marked, guarded).
- Renames reuse the existing `pubsub.UpdatedEvent` broadcast so all live consumers (TUI top bar / sidebar / active session, SSE `session.updated`) refresh immediately, and on-demand consumers (bridge `/sessions` & `/session`, API `GET`) read the new title on next fetch.

## Capabilities

### New Capabilities
- `session-rename`: user-initiated renaming of a session's title, including the guarantee that a user-set title is never overwritten by automatic title generation and that the new title is consistently reflected across every consumer.

### Modified Capabilities
<!-- None. There is no existing spec covering session titles or auto-title generation; the auto-generation behavior change is captured as a requirement of the new session-rename capability. -->

## Impact

- **DB schema**: new `sessions.user_set_title` column; new migrations for SQLite (`internal/db/migrations/sqlite/`) and MySQL (`internal/db/migrations/mysql/` + consolidated `internal/db/schema/mysql.sql`); new queries in `internal/db/sql/sessions.sql` and `internal/db/sql/mysql/sessions.sql`; regenerated sqlc (`internal/db/*.sql.go`, `internal/db/mysql/*.sql.go`, `models.go`, `querier.go`); hand-written adapter methods in `internal/db/mysql_querier.go`.
- **Session service** (`internal/session/session.go`): new `Rename` method, a guarded generated-title setter, `UserSetTitle` on the domain struct, `Service` interface additions.
- **Agent** (`internal/llm/agent/agent.go`): `generateTitle` gains the user-set guard and uses the guarded setter instead of `Save`.
- **TUI** (`internal/slashcmd/builtin.go`, `internal/tui/tui.go`): new command definition + handler.
- **Bridge** (`internal/bridge/service/commands.go`): new `cmdRename` handler + registration.
- **API** (`internal/api/handler_session.go`): title update routed through `Rename`.
- No `.opencode.json` config surface changes (no `opencode-schema.json` regen needed). No breaking changes; the new column is additive with a `false` default, so existing rows and older binaries are unaffected.
