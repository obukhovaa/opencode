## ADDED Requirements

### Requirement: Only attended sessions generate a title

The system SHALL generate a session title on a session's first turn only when the session is not marked unattended, or is marked interactive. Flow steps, headless `-p` runs, cron jobs, and the subagents linked under any of them SHALL NOT make the title model call. Interactive flow steps, the TUI, the chat bridge and the API SHALL keep generating titles.

#### Scenario: Flow step

- **WHEN** a flow step's session runs its first turn
- **THEN** no title model call is made and the session keeps the title it was created with

#### Scenario: Subagent of a flow step

- **WHEN** a task subagent spawned inside a flow step runs its first turn
- **THEN** no title model call is made

#### Scenario: Interactive flow step

- **WHEN** an `interactive: true` step's session runs its first turn
- **THEN** a title is generated
