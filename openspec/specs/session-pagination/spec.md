# Session pagination

## Purpose

Keep shared session stores responsive without materializing their entire history, while preserving connector privacy and reliable traversal of older conversations.

## Requirements

### Requirement: Bounded, project-scoped session pages

Interactive session listings SHALL use SQL cursor pages, with a default of 50 and
a maximum of 200 rows, ordered by creation time and ID descending. Reads SHALL
have a five-second deadline. Search, root/tree filters and visibility SHALL be
applied before LIMIT. A cursor SHALL carry only position and filter scope, never
authorization. Invalid or differently scoped cursors SHALL be rejected.

#### Scenario: Large shared history

- **GIVEN** thousands of session rows with tied creation timestamps
- **WHEN** a caller follows the continuation tokens
- **THEN** pages remain bounded and no row is skipped or repeated due to ties
- **AND** deleting the row represented by the cursor does not invalidate traversal

### Requirement: Responsive switch and prune dialogs

The TUI SHALL show its dialog before database work, load 50 roots asynchronously,
debounce search and cancel obsolete reads. Late replies SHALL not replace newer
results or reopen a closed dialog. Queued controls from a closed picker or an
older search revision SHALL be ignored. Navigation during an in-flight page load
SHALL NOT duplicate cursor history. Page changes SHALL replace displayed rows.
Flow badges SHALL use lightweight metadata without restoring full flow snapshots.

#### Scenario: Search during a slow request

- **WHEN** the user types a new query or closes the picker
- **THEN** the old request is cancelled and its eventual result is ignored

### Requirement: Bridge continuation preserves isolation

All bridge connectors SHALL list ten sessions per page and expose a usable
continuation command in both text and rich tables. Default lists SHALL show
conversation roots, with search or `--all` including flow steps. Current bindings
SHALL be pinned once on the first unfiltered page. External visibility SHALL be
filtered in SQL before pagination. External callers SHALL see only their own
binding regardless of search, cursor or isolation settings. Visibility failures
SHALL fail closed to that binding. Prefix resolution and cron visibility SHALL
not read the full session history.

#### Scenario: Hidden rows are newest

- **GIVEN** hundreds of newer external conversations and older visible roots
- **WHEN** Slack, Telegram or Mattermost requests its first page
- **THEN** visible roots fill that page and a rendered continuation command reaches
  the remaining visible roots without naming hidden sessions

### Requirement: API and ACP continuation contracts

HTTP list and children endpoints SHALL preserve their array body and provide
`Link` and `X-Next-Cursor` headers, exposed to browsers. ACP SHALL honor its existing
cursor and nextCursor fields. Session status SHALL retain its map shape and read
only the process-wide active ledger, scoped to the current project; absence means
idle.

#### Scenario: Clients follow session continuation

- **WHEN** an HTTP client follows the next-page Link or an ACP client resends nextCursor
- **THEN** older sessions are returned with the existing response body shape
- **AND** invalid or differently scoped cursors are rejected

#### Scenario: Status lookup with a large idle history

- **GIVEN** many historical sessions and one active session in the current project
- **WHEN** the caller reads session status
- **THEN** only the active session is reported as busy and absent IDs mean idle

### Requirement: Bounded cleanup and full-history walkers

Age cleanup SHALL count eligible trees in SQL and stream candidates in batches of
200 through the existing coordinated deletion handler. It SHALL protect the active
conversation and its flow runs. Global operations such as auto-approve startup
SHALL use bounded walkers. Legacy List SHALL return at most 200 rows.

#### Scenario: Cleanup crosses a batch boundary

- **GIVEN** more than 200 eligible trees and an active flow conversation
- **WHEN** cleanup runs
- **THEN** all eligible trees are processed through the deletion handler
- **AND** the active conversation and associated runs remain
