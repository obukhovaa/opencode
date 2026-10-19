## ADDED Requirements

### Requirement: Only attended sessions generate a title

The system SHALL generate a session title on a session's first turn only when the session is not marked unattended, or is marked interactive. Flow steps, headless `-p` runs, cron jobs fired while neither the TUI nor a chat binding watches their session (the scheduler's unattended mark), and the subagents linked under any of them SHALL NOT make the title model call. Interactive flow steps, the TUI, the chat bridge, the API, and cron jobs fired in a watched session SHALL keep generating titles.

#### Scenario: Flow step

- **WHEN** a flow step's session runs its first turn
- **THEN** no title model call is made and the session keeps the title it was created with

#### Scenario: Subagent of a flow step

- **WHEN** a task subagent spawned inside a flow step runs its first turn
- **THEN** no title model call is made

#### Scenario: Interactive flow step

- **WHEN** an `interactive: true` step's session runs its first turn
- **THEN** a title is generated

#### Scenario: Cron job in a watched session

- **WHEN** a cron job fires in the session the TUI has selected, or in one a chat binding holds
- **THEN** its subagent's first turn generates a title
