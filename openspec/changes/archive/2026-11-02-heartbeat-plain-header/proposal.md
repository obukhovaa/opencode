## Why
Every posted heartbeat report, failure line, skip notice, `/heartbeat` status and setup reminder starts with `💓`. The agent cannot change it, so a daemon whose human asked it to open each report with one emoji that fits the beat (🚢 merged, 🧭 decision needed, 🔍 finding, ⏳ waiting) still shows a heart first.

## What Changes
- The beat header becomes a plain-text note that the beat started and ran: `Heartbeat started at HH:MMZ and ran`, with no emoji.
- A failed beat posts `Heartbeat started at HH:MMZ and failed: <reason>`.
- A skipped beat never started, so its notice reads `Heartbeat due at HH:MMZ skipped: <why>. …`.
- `/heartbeat` status (`Heartbeat is on.` / `off.` / `not set up.`) and the setup reminder drop the `💓` prefix.

## Capabilities
### New Capabilities
None.
### Modified Capabilities
- bridge-heartbeat: the report header, failure line and skip notice are plain-text notes with no emoji.

## Impact
`internal/heartbeat` strings, bridge heartbeat tests, `docs/cron-and-heartbeat.md` and `docs/bridge.md`. No config, schema, migration or stored-data change. Agent prompts that mention a `💓 Heartbeat` header (for example `AGENTS.neo.md` in piano-developer) should be updated once this ships.
