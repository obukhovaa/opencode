# tui-service-management Specification

## Purpose
Provide discoverable live service controls while preserving transcript space and session responsiveness.
## Requirements
### Requirement: Service pages replace sidebar
The TUI SHALL provide /mcp, /lsp and /modified-files commands and use the former sidebar space for chat.
#### Scenario: Inspect services
- **WHEN** a user opens a service command
- **THEN** a keyboard navigable view shows service status or modified files and Escape returns to chat.
### Requirement: Live MCP management
The MCP view SHALL show servers and tool names and allow reconnect, enabling/disabling and deferral for the selected agent, persisting tool policy without restarting the process.
#### Scenario: Disable server
- **WHEN** the user disables an MCP server for an idle selected agent
- **THEN** its tools are unavailable on subsequent turns and the agent setting is saved.
#### Scenario: Busy agent
- **WHEN** service policy is edited while an agent is busy
- **THEN** the operation reports that it must be retried when idle and makes no change.
### Requirement: Live LSP activation
The LSP view SHALL list built-in and configured servers and allow explicit install/activation using supported installers and deactivation without restarting the process.
#### Scenario: Activate available server
- **WHEN** a user activates an available LSP server
- **THEN** its client starts and the view reports the result.
#### Scenario: Unavailable server
- **WHEN** activation fails or no supported installation method exists
- **THEN** the failure is visible and chat remains responsive.

#### Scenario: Deactivate server
- **WHEN** the user deactivates a language server
- **THEN** its client and watcher stop and the saved configuration disables it.
