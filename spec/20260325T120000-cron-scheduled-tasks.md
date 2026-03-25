# Cron Scheduled Tasks

**Date**: 2026-03-25
**Status**: Draft
**Author**: AI-assisted

## Overview

Add cron-based scheduled task tools (`croncreate`, `crondelete`, `cronlist`) that let primary agents schedule recurring or one-shot prompts. Each cron job acts as a proxy to the existing task tool — executing on schedule with full subagent context preservation via persistent `task_id` reuse. Jobs are stored in a dedicated DB table, cascade-deleted with their session, and automatically resume when OpenCode restarts.

## Motivation

### Current State

Agents can spawn subagent tasks on demand via the task tool:

```go
var managerToolNames = []string{TaskToolName}

// In agent-tool.go
type TaskParams struct {
    Prompt       string `json:"prompt"`
    SubagentType string `json:"subagent_type"`
    TaskID       string `json:"task_id,omitempty"`
    TaskTitle    string `json:"task_title,omitempty"`
}
```

This is strictly reactive — an agent or user must explicitly invoke the task each time.

### Problems

1. **No automated polling**: Users cannot tell the agent to periodically check deployment status, CI results, or other external state without manually re-prompting.
2. **No reminders**: No way to schedule a one-shot future action (e.g., "in 30 minutes, check if tests passed").
3. **No background workflows**: Long-running monitoring tasks require the user to stay in the loop, repeatedly asking the agent to check.

### Desired State

```
/loop 5m check if the deployment finished and tell me what happened

> Scheduled cron job `cron_a1b2c3` every 5 minutes using explorer agent.
> Next run at 14:35.
```

Agents can also schedule tasks programmatically via `croncreate`, manage them via `cronlist`/`crondelete`, and results flow back into the parent session's conversation.

## Research Findings

### Claude Code's Scheduled Tasks Implementation

Claude Code (v2.1.72+) implements session-scoped cron scheduling with three tools: `CronCreate`, `CronList`, `CronDelete`.

| Aspect | Claude Code | OpenCode (proposed) |
|---|---|---|
| Persistence | Session-only, lost on exit | DB-persisted, survives restart |
| Execution model | Fires between user turns when idle | Same — waits for agent idle, then invokes task tool |
| Cron format | Standard 5-field cron | Same |
| Interval shorthand | `/loop 5m` slash command | `/loop 5m` slash command (Go duration syntax) |
| Jitter | 10% of period, capped at 15min | Not adopted — deterministic timing preferred |
| Expiry | 3-day auto-expiry | No auto-expiry (explicit delete or session delete) |
| Task limit | 50 per session | Configurable, default 50 |
| Subagent isolation | Runs inline in session | Runs via task tool in child session |
| Context preservation | No task_id reuse | task_id reuse across runs for context continuity |

**Key findings from Claude's approach:**
- `/loop` is a "bundled skill" that parses natural-language intervals and delegates to `CronCreate`
- One-shot reminders use cron expressions pinned to a specific minute/hour
- Scheduler checks every second, enqueues at low priority
- Scheduled prompts fire between turns, never mid-response
- No catch-up for missed fires — fires once when idle

**What we adopt:**
- Same three-tool pattern (`croncreate`, `crondelete`, `cronlist`)
- Same `/loop` slash command UX with interval parsing
- Same "wait for idle, then fire" execution model
- Same 5-field cron expression format

**What we differ on:**
- Persistent storage (resume on restart) instead of ephemeral
- Task tool delegation with `task_id` reuse for context continuity across runs
- No jitter (unnecessary for single-user CLI)
- No auto-expiry (user controls lifecycle explicitly)
- `/crons` TUI page for visual management

### Existing Codebase Patterns

**Manager tool gating** (`internal/llm/agent/tools.go`):

```go
for _, name := range managerToolNames {
    if reg.IsToolEnabled(agentID, name) {
        if info.Mode == config.AgentModeAgent {
            result <- createTool(name)
        } else {
            logging.Warn("Subagent can't have manager tools enabled")
        }
    }
}
```

Cron tools follow this same pattern — added to `managerToolNames`, only available to primary agents (`AgentModeAgent`).

**DB migration pattern** (goose format, dual SQLite/MySQL migrations, sqlc for query generation).

**PubSub for TUI updates** (`pubsub.NewBroker[T]()` embedded in services).

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Tool names | `croncreate`, `crondelete`, `cronlist` (lowercase) | Consistent with existing tool naming (`bash`, `edit`, `task`) |
| Storage | Dedicated `cron_jobs` table with `ON DELETE CASCADE` from sessions | Automatic cleanup on session deletion; survives process restart |
| Execution | Proxy to task tool `Run()` method | Reuses all subagent machinery (session creation, cost rollup, permissions) |
| Context preservation | Reuse `task_id` across runs of same cron | Subagent retains conversation history from prior runs |
| Scheduler | In-process goroutine with 1-second tick | Simple, no external dependencies; matches Claude's approach |
| Agent busy handling | Queue and wait until `IsSessionBusy()` returns false | Prevents conflicts with user-initiated work |
| Permission model | Permission keyed on cron job ID; supports "allow all" (persistent) and "allow once" | First run asks, subsequent runs skip if granted persistently |
| Default agent access | Only `hivemind` has cron tools enabled | Coder already has many tools; hivemind is the coordinator |
| `/loop` implementation | Built-in slash command (not a skill) | Needs direct access to cron service; skills can't create cron jobs |
| `/crons` implementation | TUI page (like agents/logs) | Consistent with existing table-detail pattern |
| Cron format | Standard 5-field (minute hour dom month dow) | Industry standard, same as Claude Code |
| One-shot support | `is_recurring` boolean on cron job; one-shot auto-deletes after execution | Clean lifecycle management |
| Max cron jobs per session | 50 (configurable) | Prevents runaway scheduling; matches Claude's limit |

## Architecture

### Data Model

```
┌──────────────────────────────────────────────────────────────┐
│ cron_jobs                                                    │
│ ├── id              TEXT PK          (generated, 8-char)     │
│ ├── session_id      TEXT FK → sessions(id) ON DELETE CASCADE │
│ ├── schedule        TEXT             (5-field cron expr)     │
│ ├── prompt          TEXT             (task prompt)           │
│ ├── subagent_type   TEXT             (e.g. "explorer")      │
│ ├── task_title      TEXT             (short description)    │
│ ├── task_id         TEXT             (reused across runs)   │
│ ├── is_recurring    BOOLEAN          (false = one-shot)     │
│ ├── source          TEXT             ("loop" or "agent")    │
│ ├── status          TEXT             (active/paused/done)   │
│ ├── last_run_at     INTEGER nullable (unix timestamp)       │
│ ├── next_run_at     INTEGER          (unix timestamp)       │
│ ├── run_count       INTEGER          (total executions)     │
│ ├── last_result     TEXT nullable     (last task output)    │
│ ├── created_at      INTEGER                                 │
│ ├── updated_at      INTEGER                                 │
│ └── error           TEXT nullable     (last error if any)   │
└──────────────────────────────────────────────────────────────┘
          │ CASCADE DELETE
          ▼
┌──────────────────────────────────────────────────────────────┐
│ sessions                                                     │
└──────────────────────────────────────────────────────────────┘
```

### Component Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      CronService                            │
│  ├── scheduler goroutine (1s tick)                          │
│  ├── pubsub.Broker[CronJob] (TUI events)                   │
│  ├── DB queries (CRUD)                                      │
│  └── task tool execution (via agent loop)                   │
└────────────────┬────────────────────────────────────────────┘
                 │
    ┌────────────┼───────────────────┐
    ▼            ▼                   ▼
┌────────┐ ┌──────────┐   ┌──────────────────┐
│ Tools  │ │ TUI Page │   │ Slash Commands   │
│ croncreate │ │ /crons   │   │ /loop            │
│ crondelete │ │ (table)  │   │ (interval parse) │
│ cronlist   │ └──────────┘   └──────────────────┘
└────────┘
```

### Execution Flow

```
STEP 1: Cron Job Creation
─────────────────────────
Agent calls croncreate (or user types /loop)
  → Validate params (schedule, prompt, subagent_type)
  → Generate cron job ID (8-char random)
  → Generate task_id (for task tool reuse)
  → Compute next_run_at from cron expression
  → Insert into cron_jobs table
  → Publish CreatedEvent for TUI
  → Return confirmation with job ID and next fire time

STEP 2: Scheduler Tick (every 1 second)
────────────────────────────────────────
Scheduler goroutine wakes up
  → Query all active cron jobs where next_run_at <= now
  → For each due job:
      → Check if parent session's agent is busy
      → If busy: skip (will retry next tick)
      → If idle: enqueue execution

STEP 3: Cron Job Execution
──────────────────────────
  → Check permission using cron job ID as key
      → If "allow all" was granted before: proceed
      → If "allow once" or first run: ask permission
      → If denied: skip this run, keep job active
  → Invoke agent loop with prompt that triggers task tool:
      → task_id = cron_job.task_id (same across runs)
      → prompt = cron_job.prompt
      → subagent_type = cron_job.subagent_type
      → task_title = cron_job.task_title
  → Wait for task completion
  → Update cron_job: last_run_at, run_count++, last_result, next_run_at
  → If one-shot (is_recurring=false): set status="done", no next_run_at
  → Publish UpdatedEvent for TUI

STEP 4: Process Restart Resume
──────────────────────────────
On startup, CronService.Init():
  → Load all cron jobs with status="active" from DB
  → For each: recompute next_run_at from schedule (if next_run_at is in the past, set to next future occurrence)
  → Start scheduler goroutine
  → No catch-up for missed fires (same as Claude)
```

### Cron Tool Parameters

**croncreate** — superset of task tool params plus scheduling:

```go
type CronCreateParams struct {
    Prompt       string `json:"prompt"`           // required: task prompt
    SubagentType string `json:"subagent_type"`    // required: e.g. "explorer"
    TaskTitle    string `json:"task_title"`        // required: short description
    Schedule     string `json:"schedule"`          // required: 5-field cron expression
    IsRecurring  bool   `json:"is_recurring"`      // optional: default true
}
```

**crondelete**:

```go
type CronDeleteParams struct {
    ID string `json:"id"` // required: cron job ID
}
```

**cronlist**: no parameters. Returns all cron jobs for the current session.

### Permission Flow

```
FIRST RUN of cron job "cron_a1b2c3":
────────────────────────────────────
  → permissions.Request(ctx, {
        SessionID: parentSessionID,
        ToolName:  "cron",
        Action:    "execute",
        Params:    {"cron_id": "cron_a1b2c3", "prompt": "check deploy..."},
    })
  → TUI shows permission dialog:
      "Cron job cron_a1b2c3 wants to run: check deploy status
       Schedule: */5 * * * *
       [Allow Once] [Allow All] [Deny]"
  → If "Allow All" → GrantPersistant() → future runs skip permission
  → If "Allow Once" → Grant() → next run asks again
  → If "Deny" → skip this run

AUTO-APPROVE INHERITANCE:
  → If parent session has auto-approve enabled,
    cron task sessions inherit it (same as task tool)
```

### `/loop` Slash Command

```
Syntax: /loop [interval] <prompt>
        /loop <prompt> [every <interval>]

Interval format: Go duration syntax
  10s → rounded to 1m (cron minimum granularity)
  5m  → */5 * * * *
  1h  → 0 * * * *
  2h  → 0 */2 * * *
  1d  → 0 0 * * *

Default interval: 10 minutes (if omitted)

Examples:
  /loop 5m check if the deployment finished
  /loop check the build every 2 hours
  /loop 30s run tests            → rounded to 1m
```

The `/loop` command:
1. Parses interval and prompt from input text
2. Converts Go duration to nearest cron expression
3. Calls `CronService.Create()` directly (not via agent)
4. Uses the session's active agent's default subagent type (explorer by default)
5. Sets `source: "loop"` to distinguish from agent-initiated crons

### `/crons` TUI Page

```
┌─ Cron Jobs ─────────────────────────────────────────────────┐
│ ID        │ Schedule     │ Source │ Status │ Runs │ Next Run │
│───────────┼──────────────┼────────┼────────┼──────┼──────────│
│ cron_a1b2 │ */5 * * * *  │ /loop  │ active │   12 │ 14:35    │
│ cron_c3d4 │ 0 */2 * * *  │ agent  │ active │    3 │ 16:00    │
│ cron_e5f6 │ 30 14 * * *  │ /loop  │ done   │    1 │ —        │
├─────────────────────────────────────────────────────────────┤
│ Details: cron_a1b2                                          │
│ Schedule: */5 * * * * (every 5 minutes)                     │
│ Prompt: check if the deployment finished                    │
│ Subagent: explorer                                          │
│ Created: 2026-03-25 14:20                                   │
│ Last run: 2026-03-25 14:30                                  │
│                                                             │
│ Latest output:                                              │
│ > Deployment is still in progress. 3/5 pods ready.          │
│ > ETA: ~2 minutes remaining.                                │
│                                                             │
│ [d] Delete  [enter] View details                            │
└─────────────────────────────────────────────────────────────┘
```

Layout follows the same pattern as `agents` and `logs` pages: two `layout.Container`s stacked vertically (table + details), split 50/50. The table subscribes to `pubsub.Event[CronJob]` for live updates.

Key bindings:
- `d` or `delete` — delete selected cron job
- `enter` — select row, show details in bottom panel
- `esc` — return to chat

### TUI Rendering of Cron Tool Calls

Cron tool calls in the chat message list render similarly to task tool calls but with a cron indicator:

```
┌─ 🔄 Cron Task (*/5 * * * *) ──────────────────────────────┐
│ cron_a1b2 → explorer: check if the deployment finished     │
│                                                             │
│ > Deployment complete. All 5/5 pods running.               │
│ > Build sha: a1b2c3d4                                      │
└─────────────────────────────────────────────────────────────┘
```

The visual difference from a regular task call is the cron icon and schedule display in the header.

### Agent Access Control

```go
// In registry.go registerBuiltins():
{
    ID:   "hivemind",
    Mode: config.AgentModeAgent,
    Tools: map[string]bool{
        // existing disabled tools...
        "bash":      false,
        "edit":      false,
        // cron tools enabled (not in disabled list)
    },
},
{
    ID:   "coder",
    Mode: config.AgentModeAgent,
    Tools: map[string]bool{
        // cron tools disabled by default
        "croncreate": false,
        "crondelete": false,
        "cronlist":   false,
    },
},
```

Cron tool names are added to `managerToolNames`:

```go
var managerToolNames = []string{
    TaskToolName,
    CronCreateToolName,
    CronDeleteToolName,
    CronListToolName,
}
```

This ensures the existing `AgentModeAgent` check in `NewToolSet` prevents subagents from accessing cron tools. Additionally, `coder` has them disabled by default — only `hivemind` gets them out of the box. Users can override this in `.opencode.json`.

### CronService Interface

```go
type CronJob struct {
    ID            string
    SessionID     string
    Schedule      string
    Prompt        string
    SubagentType  string
    TaskTitle     string
    TaskID        string
    IsRecurring   bool
    Source        string  // "loop" or "agent"
    Status        string  // "active", "paused", "done"
    LastRunAt     int64
    NextRunAt     int64
    RunCount      int64
    LastResult    string
    Error         string
    CreatedAt     int64
    UpdatedAt     int64
}

type Service interface {
    pubsub.Suscriber[CronJob]
    Create(ctx context.Context, params CreateParams) (CronJob, error)
    Delete(ctx context.Context, id string) error
    List(ctx context.Context, sessionID string) ([]CronJob, error)
    Get(ctx context.Context, id string) (CronJob, error)
    Start(ctx context.Context) error   // starts scheduler, resumes active jobs
    Stop()                             // stops scheduler
}
```

## Implementation Plan

### Phase 1: Database Layer

- [ ] **1.1** Create SQLite migration `internal/db/migrations/sqlite/20260325120000_add_cron_jobs.sql` with `cron_jobs` table, `ON DELETE CASCADE` from sessions, indexes on `session_id` and `status`, `updated_at` trigger
- [ ] **1.2** Create MySQL migration `internal/db/migrations/mysql/20260325120000_add_cron_jobs.sql` (same schema, MySQL syntax)
- [ ] **1.3** Create sqlc query files `internal/db/sql/cron_jobs.sql` and `internal/db/sql/mysql/cron_jobs.sql` with CRUD operations: `CreateCronJob`, `GetCronJob`, `ListCronJobsBySession`, `ListActiveCronJobs`, `UpdateCronJob`, `DeleteCronJob`
- [ ] **1.4** Add `CronJob` model to `internal/db/models.go`
- [ ] **1.5** Run sqlc generation, update `Querier` interface
- [ ] **1.6** Update `internal/db/schema/mysql.sql` with the new table

### Phase 2: Cron Service

- [ ] **2.1** Create `internal/cron/service.go` with `Service` interface, `CronJob` struct, and `CreateParams`
- [ ] **2.2** Implement cron expression parser (use existing Go library like `github.com/robfig/cron/v3` or a lightweight parser — check `go.mod` for existing deps first)
- [ ] **2.3** Implement scheduler goroutine: 1-second tick, query due jobs, check agent busy state, enqueue execution
- [ ] **2.4** Implement execution logic: permission check, task tool invocation via agent loop, result capture, job state update
- [ ] **2.5** Implement `Start()` for resume-on-restart: load active jobs, recompute `next_run_at`
- [ ] **2.6** Implement Go duration to cron expression conversion for `/loop` interval syntax
- [ ] **2.7** Add pubsub broker for TUI updates

### Phase 3: Tools

- [ ] **3.1** Create `internal/llm/tools/cron.go` with `croncreate`, `crondelete`, `cronlist` tool implementations
- [ ] **3.2** Add cron tool names to `managerToolNames` in `internal/llm/agent/tools.go`
- [ ] **3.3** Update built-in agent registrations in `internal/agent/registry.go`: disable cron tools for `coder`, keep enabled for `hivemind`, already blocked for subagents by manager tool check
- [ ] **3.4** Add tool descriptions with clear documentation of parameters and cron expression format
- [ ] **3.5** Implement permission checking: use cron job ID as permission key, support persistent grant for recurring jobs

### Phase 4: Slash Commands & TUI

- [ ] **4.1** Add `/loop` slash command in `internal/tui/tui.go` `buildCommands`: parse interval + prompt, call `CronService.Create()`
- [ ] **4.2** Add `/crons` slash command that navigates to the crons TUI page
- [ ] **4.3** Create `internal/tui/components/crons/table.go` — cron jobs table with columns: ID, Schedule, Source, Status, Runs, Next Run
- [ ] **4.4** Create `internal/tui/components/crons/details.go` — detail panel showing full job info + latest output
- [ ] **4.5** Create `internal/tui/page/crons.go` — page with table + details layout (same as agents/logs pattern)
- [ ] **4.6** Register crons page in `internal/tui/tui.go`, add keybinding for navigation
- [ ] **4.7** Add delete keybinding in crons table (`d` key)

### Phase 5: Integration & Chat Rendering

- [ ] **5.1** Wire `CronService` into `app.App` and initialize on startup (`Start()`), shutdown (`Stop()`)
- [ ] **5.2** Implement chat message rendering for cron tool calls — show cron icon, schedule, and result (similar to task tool rendering but with cron indicator)
- [ ] **5.3** Handle agent busy state: scheduler waits for `IsSessionBusy()` to return false before executing cron task
- [ ] **5.4** Result conflation: cron execution creates messages in the parent session's conversation via the agent loop

### Phase 6: Testing

- [ ] **6.1** Unit tests for cron expression parsing and Go duration conversion
- [ ] **6.2** Unit tests for cron service CRUD operations
- [ ] **6.3** Unit tests for scheduler logic (due job detection, busy waiting, one-shot cleanup)
- [ ] **6.4** Unit tests for tool parameter validation
- [ ] **6.5** Integration test: create cron → wait for execution → verify task tool was invoked with correct params

## Edge Cases

### Agent Busy When Cron Fires

1. Cron job `cron_a1b2` is due at 14:35
2. Agent is mid-response on a user request
3. Scheduler detects `IsSessionBusy(sessionID) == true`
4. Scheduler skips this tick, retries every second
5. Agent finishes at 14:37, scheduler detects idle state
6. Cron job fires at 14:37
7. No catch-up: only one execution, not two

### Session Deleted While Cron Active

1. User deletes session containing active cron jobs
2. `ON DELETE CASCADE` in DB removes all `cron_jobs` rows
3. Session service publishes `DeletedEvent`
4. CronService listens for session deletions, removes in-memory state for that session
5. Scheduler never fires those jobs again

### Process Restart

1. OpenCode exits (ctrl+c or crash)
2. Scheduler goroutine stops, in-memory state lost
3. OpenCode restarts, user opens same session
4. `CronService.Start()` loads all `status="active"` jobs from DB
5. For each job: if `next_run_at` is in the past, compute next future occurrence from `schedule`
6. Scheduler resumes normal operation

### Concurrent Cron Fires

1. Two cron jobs due at the same time for the same session
2. First job starts executing (agent becomes busy)
3. Second job sees agent busy, waits
4. First job completes, agent becomes idle
5. Second job fires on next tick

### One-Shot Completion

1. User schedules: "remind me at 3pm to push the release"
2. Cron expression: `0 15 * * *`, `is_recurring: false`
3. Job fires at 15:00, task executes
4. After completion: `status` set to `"done"`, no `next_run_at`
5. Job remains in DB for visibility but never fires again
6. `/crons` table shows it as "done"

### Permission Denied

1. Cron job fires, permission dialog shown
2. User selects "Deny"
3. This run is skipped, job stays active
4. Next scheduled time: permission asked again
5. Job is NOT deleted on denial — only skipped

### Max Jobs Per Session

1. Session already has 50 active cron jobs
2. Agent calls `croncreate` for a 51st job
3. Tool returns error: "Maximum of 50 cron jobs per session reached"
4. Agent can suggest deleting unused jobs

## Open Questions

1. **Should cron tools be available to `coder` by default, or only `hivemind`?**
   - Currently proposed: only `hivemind` by default, users can enable for `coder` via config
   - **Recommendation**: Start with `hivemind`-only, gather feedback. The coder agent already has many tools, and scheduling is a coordination concern that fits hivemind's role.

2. **Should there be an auto-expiry like Claude's 3-day limit?**
   - Claude auto-expires after 3 days to prevent forgotten loops
   - **Recommendation**: Skip for now. Since OpenCode persists cron jobs and shows them in `/crons`, users have visibility. Can add later if needed via a configurable `max_lifetime` field.

3. **How should the cron execution interact with the chat message list?**
   - Option A: Cron results appear as regular messages in the chat (visible immediately)
   - Option B: Cron results appear as collapsed/folded entries (less noisy)
   - **Recommendation**: Option A for now — results should be visible since the user scheduled them. The task tool rendering already handles subagent results well.

4. **Should `/loop` default to `explorer` or use a configurable default subagent?**
   - **Recommendation**: Default to `explorer` (read-only, safe for automated polling). Allow overriding via syntax like `/loop 5m --agent workhorse check and fix the build`.

5. **Should cron jobs from one session be visible in `/crons` when viewing a different session?**
   - **Recommendation**: Show only current session's cron jobs. A "show all" toggle could be added later.

6. **Environment variable to disable cron entirely?**
   - Claude uses `CLAUDE_CODE_DISABLE_CRON=1`
   - **Recommendation**: Add `OPENCODE_DISABLE_CRON=1` for parity. When set, cron tools are not registered and `/loop` is unavailable.

## Success Criteria

- [ ] `croncreate` tool creates a persistent cron job that survives process restart
- [ ] `crondelete` removes a cron job by ID
- [ ] `cronlist` returns all cron jobs for the current session
- [ ] Cron jobs execute on schedule via task tool with `task_id` reuse
- [ ] Deleting a session cascade-deletes its cron jobs
- [ ] `/loop 5m <prompt>` creates a recurring cron job from the TUI
- [ ] `/crons` opens a table page showing all session cron jobs with delete capability
- [ ] Cron tools are only available to primary agents (not subagents)
- [ ] Only `hivemind` has cron tools enabled by default
- [ ] Permission is asked on first cron execution; "allow all" skips future prompts
- [ ] Agent busy state is respected — cron waits for idle before firing
- [ ] Both SQLite and MySQL providers support the `cron_jobs` table
- [ ] `make test` passes

## References

- `internal/llm/agent/agent-tool.go` — Task tool implementation (cron proxies to this)
- `internal/llm/agent/tools.go` — `managerToolNames` and `NewToolSet` (cron tools added here)
- `internal/agent/registry.go` — Built-in agent registration (tool access control)
- `internal/db/migrations/sqlite/20260220120000_add_flow_states.sql` — Migration pattern to follow
- `internal/db/sql/flow_states.sql` — sqlc query pattern to follow
- `internal/session/session.go` — Session service (cascade delete, event publishing)
- `internal/permission/permission.go` — Permission service (request/grant/deny flow)
- `internal/tui/page/agents.go` — TUI page pattern (table + details layout)
- `internal/tui/tui.go` — Slash command registration in `buildCommands`
- `internal/tui/components/dialog/commands.go` — Command struct for slash commands
- `internal/llm/tools/skill.go` — Permission check pattern for tools
- [Claude Code scheduled tasks docs](https://code.claude.com/docs/en/scheduled-tasks) — Reference implementation
