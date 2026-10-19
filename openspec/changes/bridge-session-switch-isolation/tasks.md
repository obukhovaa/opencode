## 1. Setting and lookup

- [x] 1.1 `router.isolateExternalSessions` on `bridge.Config`, nil-safe `ExternalSessionsIsolated()`, schema generator entry and regenerated `opencode-schema.json`
- [x] 1.2 `Store.ListSessionIDsByChannel` over a new sqlc query for SQLite and MySQL (distinct non-NULL session ids, project-scoped)

## 2. Commands

- [x] 2.1 `/sessions` leaves out external-bound sessions, keeping the current one
- [x] 2.2 `/session <id-prefix>` excludes them from matching and from the ambiguous listing, keeping the current one
- [x] 2.3 `/crons` leaves out their jobs, keeping the current session's
- [x] 2.4 A failed lookup fails closed to the current session and logs a WARN; with the setting off the store is not queried

## 3. Tests and docs

- [x] 3.1 Commands: off lists and switches as before; on hides, keeps the current session, refuses the switch with the no-match reply, leaves it out of the ambiguous listing, fails closed on a lookup error
- [x] 3.2 Store query on SQLite (and the MySQL integration suite); viper round-trip of the field
- [x] 3.3 `docs/bridge.md`: router field, chat commands, external conversations
