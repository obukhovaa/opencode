## ADDED Requirements

### Requirement: Dual-provider migrations for bridge tables

The bridge SHALL ship parallel goose migrations for SQLite and MySQL creating `bridge_sessions` and `bridge_allowlist` tables. The migration files MUST live at `internal/db/migrations/sqlite/<ts>_add_bridge_tables.sql` and `internal/db/migrations/mysql/<ts>_add_bridge_tables.sql`. The two-table definitions in MySQL syntax MUST also be appended to `internal/db/schema/mysql.sql` (sqlc's MySQL schema source per `sqlc.yaml:16`); without this, `make generate` cannot produce MySQL bindings.

#### Scenario: Both migrations apply cleanly

- **WHEN** opencode boots against either a fresh SQLite file or a fresh MySQL schema
- **THEN** goose applies the matching `add_bridge_tables` migration and `bridge_sessions` and `bridge_allowlist` exist with the expected columns

#### Scenario: sqlc regenerates both packages

- **WHEN** `make generate` runs after the migrations and query files are added
- **THEN** the `db.Queries` (SQLite) and `mysqldb.Queries` (MySQL) packages both expose the new bridge queries

### Requirement: Bridge tables include project_id in every primary key

The `bridge_sessions` PK MUST be `(project_id, channel, identity_id, peer_id)`. The `bridge_allowlist` PK MUST be `(project_id, channel, identity_id, peer_id)`. The `project_id` value MUST be `db.GetProjectID(config.WorkingDirectory())` — the same source opencode uses for `sessions.project_id`. This allows a shared MySQL to host multiple opencode workspaces without collision on `(channel, identity_id, peer_id)`.

#### Scenario: Two workspaces against the same MySQL

- **WHEN** two opencode instances run against the same MySQL with different `config.WorkingDirectory()` paths and both have Slack `default` identity configured
- **THEN** their `bridge_sessions` rows do not collide because `project_id` differs

### Requirement: bridge_sessions is many-to-one (many peers per session)

A single `session_id` MUST be allowed to appear in multiple `bridge_sessions` rows. Uniqueness is enforced only by the PK `(project_id, channel, identity_id, peer_id)` — the schema MUST NOT add a `UNIQUE (session_id)` constraint. This supports multi-reviewer interactive flow steps, where one step's session is bound to multiple peers concurrently.

#### Scenario: Three peers bound to one session

- **WHEN** the flow engine binds session `S` to three peers (Slack DM Alice, Slack DM Bob, Telegram chat `12345`)
- **THEN** three `bridge_sessions` rows exist, all with `session_id == "S"`, no PK violations

#### Scenario: Lookup by session_id returns all bound peers

- **WHEN** querying `SELECT * FROM bridge_sessions WHERE project_id = ? AND session_id = ?` for a multi-bound session
- **THEN** all bound peers are returned, suitable for outbound fan-out

### Requirement: bridge_sessions.mention_handle column

The `bridge_sessions` table MUST include a `mention_handle` column carrying the per-peer ping handle used as the first-message attribution prefix.

| Provider | Type |
|---|---|
| SQLite | `TEXT NULL` |
| MySQL | `VARCHAR(255) NULL` |

The column is nullable — bindings created without an explicit mention use the row's `peer_id` for attribution.

#### Scenario: Mention handle populated by bind

- **WHEN** `POST /router/bind` includes `{peers: [{..., mention: "<@U01ABC>"}]}`
- **THEN** the inserted row's `mention_handle` is `"<@U01ABC>"`

#### Scenario: Mention handle absent

- **WHEN** bind is called without a mention for a peer
- **THEN** the row's `mention_handle` is `NULL`; outbound attribution falls back to `peer_id`

### Requirement: bridge_sessions.mention_consumed_at column

The `bridge_sessions` table MUST include a `mention_consumed_at` column (nullable timestamp). It tracks whether the per-binding mention prefix has been delivered yet — set to current time on the first successful outbound for that binding so subsequent outbounds skip the prefix.

| Provider | Type |
|---|---|
| SQLite | `INTEGER NULL` (unix seconds) |
| MySQL | `BIGINT NULL` (unix seconds) |

#### Scenario: First successful outbound sets the timestamp

- **WHEN** a binding with `mention_handle != NULL` and `mention_consumed_at == NULL` receives a successful first outbound
- **THEN** `mention_consumed_at` is updated to the current unix time within the same transaction as any binding-form mutation (Slack channel→thread, Mattermost root-post capture); if no form mutation occurs, the update is its own transaction

#### Scenario: Re-bind resets the timestamp

- **WHEN** a peer is unbound and re-bound (DELETE then INSERT, or upsert REPLACE)
- **THEN** the new row's `mention_consumed_at` is NULL; the next outbound uses the prefix again

### Requirement: Foreign key with ON DELETE SET NULL on session_id

The `bridge_sessions.session_id` column MUST be a foreign key referencing `sessions(id)` with `ON DELETE SET NULL`. When opencode garbage-collects a session, the bridge's pointer MUST become `NULL` rather than the bridge row being cascade-deleted. The orchestrator MUST detect `session_id IS NULL` on the next inbound message from that peer and create a fresh opencode session, updating the pointer.

#### Scenario: Parent session deleted

- **WHEN** an opencode session is deleted via `internal/session.Service.Delete`
- **THEN** the corresponding `bridge_sessions.session_id` rows are set to `NULL` (not cascade-deleted)

#### Scenario: NULL pointer triggers fresh session creation

- **WHEN** an inbound message arrives for a peer whose `bridge_sessions.session_id IS NULL`
- **THEN** the orchestrator creates a new opencode session and `UPDATE`s the row's `session_id`

### Requirement: Required index for ON DELETE SET NULL performance

The migrations MUST create `idx_bridge_sessions_session_id (session_id)`. Without this index, deleting an opencode session triggers a full-scan of `bridge_sessions` on both providers because the `ON DELETE SET NULL` action requires scanning child rows.

#### Scenario: Index present after migration

- **WHEN** the `add_bridge_tables` migration has run on either provider
- **THEN** `idx_bridge_sessions_session_id` exists on `bridge_sessions(session_id)`

### Requirement: sqlc query files for both providers

The bridge SHALL ship sqlc query files at `internal/db/sql/bridge_sessions.sql` and `internal/db/sql/bridge_allowlist.sql` (SQLite, using `:one` with `RETURNING *` for inserts) and at `internal/db/sql/mysql/bridge_sessions.sql` and `internal/db/sql/mysql/bridge_allowlist.sql` (MySQL, using `:execresult` for INSERT then a GET-after-INSERT round-trip, matching the existing pattern at `internal/db/sql/mysql/sessions.sql:1-33`).

#### Scenario: Generated code compiles after make generate

- **WHEN** `make generate` runs against the new query files
- **THEN** both `db` and `mysqldb` packages compile cleanly and expose the bridge methods

### Requirement: Store wrapper multiplexes between providers

The bridge SHALL ship an `internal/bridge/store.go` wrapper that multiplexes between `db.Queries` (SQLite) and `mysqldb.Queries` (MySQL) for the two bridge tables. The wrapper MUST follow the pattern established by `internal/db/querier_factory.go`. Bridge code MUST NOT directly import either generated package — it goes through the wrapper.

#### Scenario: Store works against SQLite

- **WHEN** the bridge runs with `config.SessionProvider.Type == ProviderSQLite`
- **THEN** `bridge.Store` operations route to `db.Queries` and succeed

#### Scenario: Store works against MySQL

- **WHEN** the bridge runs with `config.SessionProvider.Type == ProviderMySQL`
- **THEN** `bridge.Store` operations route to `mysqldb.Queries` and succeed

### Requirement: Type mapping consistent across providers

The migration DDL MUST use the following type mapping per provider. MySQL column widths are sized so the **compound primary key fits within InnoDB's 3072-byte key-length limit** under utf8mb4 (4 bytes/char): `(255 + 32 + 64 + 128) × 4 = 1916 bytes < 3072`. Without this constraint the migration fails at apply time with `Error 1071 (42000): Specified key was too long`. SQLite has no equivalent limit but the same logical upper bounds apply for cross-provider struct consistency in the sqlc-generated Go code.

| Logical | SQLite | MySQL |
|---|---|---|
| `project_id` | `TEXT NOT NULL` | `VARCHAR(255) NOT NULL` |
| `channel` | `TEXT NOT NULL` | `VARCHAR(32) NOT NULL` (values: `"slack"`, `"telegram"`, `"mattermost"`) |
| `identity_id` | `TEXT NOT NULL` | `VARCHAR(64) NOT NULL` (operator-chosen, typically `"default"`) |
| `peer_id` | `TEXT NOT NULL` | `VARCHAR(128) NOT NULL` (longest real form: Mattermost `<channelId>\|<rootPostId>` ≈ 53 chars) |
| `session_id` | `TEXT REFERENCES sessions(id) ON DELETE SET NULL` | `VARCHAR(255) NULL` + named FK constraint |
| `mention_handle` | `TEXT NULL` | `VARCHAR(255) NULL` |
| `mention_consumed_at` | `INTEGER NULL` | `BIGINT NULL` |
| `created_at`, `updated_at` | `INTEGER NOT NULL DEFAULT (strftime('%s','now'))` | `BIGINT NOT NULL DEFAULT (UNIX_TIMESTAMP())` |

For MySQL the `bridge_sessions` table MUST include `ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci` and an inline named FK constraint `CONSTRAINT fk_bridge_sessions_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE SET NULL`. SQLite uses a `CREATE TRIGGER ... AFTER UPDATE` for `bridge_sessions.updated_at` maintenance; MySQL uses a `BEFORE UPDATE` trigger.

#### Scenario: Both providers store the same row shape

- **WHEN** the bridge inserts a `bridge_sessions` row on either provider
- **THEN** the row carries `project_id`, `channel`, `identity_id`, `peer_id`, `session_id`, `created_at`, and `updated_at` with semantically equivalent values

### Requirement: SQLite single-writer file lock

For SQLite deployments the bridge SHALL acquire an OS file lock on `<config.Data.Directory>/bridge.lock` at startup via a new `internal/fileutil/lock.go`. The implementation MUST be hand-rolled: `lock_unix.go` with build tag `//go:build unix` wrapping `syscall.Flock(LOCK_EX|LOCK_NB)`, `lock_windows.go` with build tag `//go:build windows` wrapping `LockFileEx` from `golang.org/x/sys/windows` with `LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY`. If the lock cannot be acquired, the bridge MUST refuse to start and report the contention in `/health`. The lock MUST be released automatically on process exit.

#### Scenario: Two opencode processes against the same SQLite

- **WHEN** a second `opencode serve` starts pointing at the same `Data.Directory`
- **THEN** the second process's bridge fails to acquire `bridge.lock`, refuses to start the bridge, and reports the contention in `/health`; the second process's API server still starts normally

### Requirement: MySQL per-identity GET_LOCK

For MySQL deployments the bridge SHALL acquire one MySQL named lock per enabled identity via `GET_LOCK(?, 0)`. The lock name MUST be `'opencode_bridge:' + SHA1_HEX(project_id + ':' + channel + ':' + identity_id)`. The implementation MUST satisfy two constraints: (1) each lock is held on a dedicated `*sql.Conn` obtained via `db.Conn(ctx)` and never returned to the pool for the adapter's lifetime, because `GET_LOCK` is per-connection; (2) on connection drop the bridge MUST detect via ping failure, reconnect, and re-acquire the lock before resuming — while unreacquired the adapter MUST be marked `degraded` in `/health`.

#### Scenario: Two opencode processes contend for one identity

- **WHEN** two opencode processes with the same `project_id` both try to start Slack identity `default`
- **THEN** the first acquires the `GET_LOCK`, the second's acquisition returns 0 (not available), the second's Slack `default` adapter is marked disabled with "another opencode instance owns this identity", and the second process's other identities come up normally

#### Scenario: Lock-holder connection drops

- **WHEN** the dedicated `*sql.Conn` holding a `GET_LOCK` is dropped by a network blip or MySQL restart
- **THEN** the bridge detects the drop via ping failure, marks the adapter `degraded` in `/health`, reconnects, re-acquires the lock, and resumes the adapter

#### Scenario: Different schemas don't collide due to project_id

- **WHEN** two opencode deployments against the same MySQL server use different schemas but identical workspace paths
- **THEN** they DO collide on the per-identity `GET_LOCK` because `project_id` is identical; this is the intended single-writer enforcement

#### Scenario: Different workspaces against the same MySQL don't collide

- **WHEN** two opencode deployments against the same MySQL server have different `config.WorkingDirectory()` paths and therefore different `project_id` hashes
- **THEN** their per-identity `GET_LOCK` names differ and both can hold their respective identities concurrently

### Requirement: No data migration from TS bridge

This change MUST NOT ship a data-migration tool. There is no importer from `~/.openwork/opencode-router/opencode-router.db`, no row-copy command, no Telegram pairing-hash migrator. Fresh deployments start with empty `bridge_sessions` and `bridge_allowlist` tables.

#### Scenario: Cutover from TS bridge

- **WHEN** an operator stops the TS bridge and starts the Go bridge for the first time
- **THEN** `bridge_sessions` and `bridge_allowlist` are empty; existing Telegram pairing-code redemptions are lost and users must re-pair
