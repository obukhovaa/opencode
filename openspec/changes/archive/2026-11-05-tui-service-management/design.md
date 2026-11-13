## Context
MCP clients are shared and pooled; agent toolsets are cached. LSP initialization currently closes its startup channel. Existing configuration writeback preserves environment placeholders.
## Goals / Non-Goals
Provide live controls and preserve shared-session safety. Do not change the public config shape or install arbitrary user-supplied packages.
## Decisions
Use dedicated keyboard-driven service pages, asynchronous commands for IO, existing tools/deferredTools entries scoped to the selected agent. Replace the selected idle agent toolset in place after persistence so cached tool schemas reflect changes while its provider and subscriptions remain stable. Reject edits while agents are busy to avoid races. Reconnect retires pooled clients without interrupting active calls. LSP activation uses built-in installer metadata and a long-lived app context; startup-channel notification must tolerate completion.
## Risks / Trade-offs
Shared MCP clients → disabling controls selected-agent exposure rather than shutting down transport used elsewhere. Installing servers can take time → show busy/error state while command runs off the UI thread. Existing busy agents → require retry after work completes.
## Migration Plan
No migration. Existing settings remain valid; rollback removes the new commands.
