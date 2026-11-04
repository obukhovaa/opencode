## Why
The permanent service sidebar consumes transcript space and cannot repair disconnected services. Users need live service controls without restarting their session.

## What Changes
- Replace the sidebar with dedicated MCP, LSP and modified-file views.
- Persist per-agent MCP enabled/deferred tool choices and support reconnect.
- Start or install supported LSP servers on demand.

## Capabilities
### New Capabilities
- `tui-service-management`: Discover and manage services in the TUI.
### Modified Capabilities
None.

## Impact
TUI pages and command routing, MCP registry, configuration writeback, LSP lifecycle. Existing configuration fields suffice.
