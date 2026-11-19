## Why

A shared production project has about 120,000 sessions. Opening switch/prune loaded all selectable rows synchronously and restored full flow snapshots per root; bridge commands also loaded everything before truncating.

This is the completion record for the implementation and review in PR #134. The main capability spec was added with the implementation before this archive record was assembled.

## What Changes

- Add indexed, bounded cursor reads and server-side search on SQLite and MySQL.
- Make TUI switch/prune asynchronous with cancellation, picker/query generations and lightweight flow badges.
- Page bridge lists and prefix lookup without weakening external-session privacy; render continuation commands in native tables.
- Page HTTP and ACP lists, stream cleanup and global walkers, and read status from active work.

## Capabilities

### New Capabilities

- `session-pagination`: bounded history traversal across interfaces, cleanup and privacy.

### Modified Capabilities

None.

## Impact

Session storage/query adapters, TUI dialogs, bridge service and table renderers, HTTP/ACP handlers and serve startup. Adds parallel SQLite/MySQL index migrations. No new configuration fields or dependencies. HTTP array responses and ACP cursor fields retain their shapes; clients must follow continuation for older sessions. Legacy session List is bounded to 200 rows.
