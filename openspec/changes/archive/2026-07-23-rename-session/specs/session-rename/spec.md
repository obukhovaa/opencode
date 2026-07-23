## ADDED Requirements

### Requirement: Users can rename a session's title
The system SHALL provide an operation that sets a session's user-facing title to a caller-supplied value. The operation SHALL trim leading and trailing whitespace, SHALL reject an empty or whitespace-only title without modifying the session, and on success SHALL persist the new title and durably mark the session as user-titled. The mark SHALL survive process restarts and SHALL NOT be cleared by unrelated session updates (token counts, cost, summary).

#### Scenario: Successful rename
- **WHEN** the rename operation is invoked on an existing session with a non-empty title
- **THEN** the session's persisted title becomes that value and the session is marked user-titled

#### Scenario: Empty title rejected
- **WHEN** the rename operation is invoked with an empty or whitespace-only title
- **THEN** the operation returns an error and the session's title and user-titled mark are unchanged

#### Scenario: Surrounding whitespace trimmed
- **WHEN** the rename operation is invoked with a title that has leading/trailing whitespace
- **THEN** the persisted title is the trimmed value

#### Scenario: Unrelated updates preserve the mark
- **WHEN** a session that was renamed by a user later has its token counts, cost, or summary updated through the normal save path
- **THEN** the title and the user-titled mark are preserved

### Requirement: A user-set title is never overwritten by automatic title generation
Automatic title generation SHALL NOT modify the title of a session that has been marked user-titled. Automatic title generation SHALL continue to apply to sessions that have not been renamed. The guard SHALL be enforced such that a rename that commits at any point up to the generator's write wins over the generated title (no lost-update race).

#### Scenario: Generator skips a user-titled session
- **WHEN** a session has been renamed by a user and a turn triggers automatic title generation
- **THEN** the generated title is not written and the user's title is preserved

#### Scenario: Generator still titles un-renamed sessions
- **WHEN** a session that has never been renamed sends its first message
- **THEN** a title is generated and persisted as before, and the session is not marked user-titled

#### Scenario: Rename wins over an in-flight generation
- **WHEN** a user renames a session while automatic title generation for that session is in flight
- **THEN** the final persisted title is the user's title regardless of which write completes last

### Requirement: The TUI exposes a rename command
The TUI SHALL provide a `/rename` slash command that renames the active session. Invoking it SHALL prompt for the new title; submitting a non-empty value SHALL apply the rename to the active session. When there is no active session or the submitted value is empty, the command SHALL report a warning and make no change.

#### Scenario: Rename the active session from the TUI
- **WHEN** the user runs `/rename` and submits a non-empty title while a session is active
- **THEN** the active session is renamed and the TUI top bar shows the new title

#### Scenario: No active session
- **WHEN** the user runs `/rename` while no session is active
- **THEN** a warning is shown and no session is modified

### Requirement: The chat bridge exposes a rename command
The chat bridge SHALL handle a `/rename <new title>` command that renames the session currently bound to the requesting peer, using the command argument as the new title. With no argument the command SHALL reply with usage guidance and make no change. On success it SHALL reply confirming the new title. The command SHALL be handled by the bridge itself and SHALL NOT be forwarded to the agent as a prompt.

#### Scenario: Rename the bound session from chat
- **WHEN** a peer bound to a session sends `/rename Release triage`
- **THEN** that session's title becomes `Release triage` and the bridge replies confirming the new title

#### Scenario: Missing argument
- **WHEN** a peer sends `/rename` with no argument
- **THEN** the bridge replies with usage guidance and the session title is unchanged

### Requirement: Explicit title updates across entry points are consistent
Every explicit user-initiated title update — TUI command, bridge command, and the session update API — SHALL go through the same rename path so that each one marks the session user-titled and is therefore protected from automatic title generation.

#### Scenario: API title update is protected
- **WHEN** a client sets a session's title via the session update API and that session later triggers automatic title generation
- **THEN** the title set via the API is preserved

### Requirement: A renamed title is reflected consistently across all consumers
On a successful rename the system SHALL broadcast a session-updated event carrying the new title so that live consumers refresh without a manual reload, and consumers that read sessions on demand SHALL observe the new title on their next read.

#### Scenario: Live event stream reflects the rename
- **WHEN** a session is renamed
- **THEN** a `session.updated` event carrying the new title is emitted on the API event stream

#### Scenario: TUI live consumers refresh
- **WHEN** the active session is renamed
- **THEN** the TUI top bar and the active-session state reflect the new title without a manual reload

#### Scenario: On-demand consumers read the new title
- **WHEN** a session is renamed
- **THEN** a subsequent bridge `/sessions` or `/session` listing and a session GET via the API return the new title
