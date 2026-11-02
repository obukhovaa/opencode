## Why
Every posted heartbeat report, failure line, skip notice, `/heartbeat` status and setup reminder starts with `💓`. The agent cannot change it, so a daemon whose human asked it to open each report with one emoji that fits the beat (🚢 merged, 🧭 decision needed, 🔍 finding, ⏳ waiting) still shows a heart first.

## What Changes
- The beat header becomes plain text, `Heartbeat HH:MMZ`, with no emoji. The failure line (`… failed: <reason>`) and the skip notice (`… skipped: <why>`) reuse it.
- `/heartbeat` status (`Heartbeat is on.` / `off.` / `not set up.`) and the setup reminder drop the `💓` prefix.

## Capabilities
### New Capabilities
None.
### Modified Capabilities
- bridge-heartbeat: the report header and skip notice carry no emoji.

## Impact
`internal/heartbeat` strings, bridge heartbeat tests, `docs/cron-and-heartbeat.md` and `docs/bridge.md`. No config, schema, migration or stored-data change. Agent prompts that mention a `💓 Heartbeat` header (for example `AGENTS.neo.md` in piano-developer) should be updated once this ships.
