# tui-activities Specification

## Purpose
Expose running work within terminal chat sessions and keep editing responsive as sessions grow.

## Requirements
### Requirement: Responsive editing
Typing in a long session SHALL avoid re-rendering unchanged historical messages and quadratic tool/result matching.
#### Scenario: Long tool history
- **WHEN** the user types while a session contains thousands of completed tool calls
- **THEN** historical tool matching SHALL use linear work and unchanged message rendering SHALL be reused

### Requirement: Activity inspection and cancellation
The TUI SHALL provide /tasks for shell and monitor activities and /subagents for subagent activities in the current session and its descendant sessions, with status, original task details, output and cancellation.
#### Scenario: Inspect and stop task
- **WHEN** a user selects an activity and requests cancellation
- **THEN** the corresponding task SHALL be cancelled through its existing lifecycle and its updated state shown
#### Scenario: Session isolation
- **WHEN** unrelated sessions have running work
- **THEN** the active session activity page SHALL exclude it

### Requirement: Activity navigation
The TUI SHALL show running activity counts above the editor and allow keyboard navigation into the activity views without losing draft text.
#### Scenario: Open activities
- **WHEN** the user focuses the activity strip with Up from an empty editor or k in vim NORMAL and presses Enter
- **THEN** the selected activity view SHALL open
