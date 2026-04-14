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

> ⏲ Scheduled cron job cron_a1b2c3 — every 5 minutes (explorer).
> Next run at 14:35.
```

Agents can also schedule tasks programmatically via `croncreate`, manage them via `cronlist`/`crondelete`, and results flow back into the parent session's conversation as synthetic tool messages.

## Research Findings

### Claude Code's Scheduled Tasks Implementation

Claude Code (v2.1.72+) implements session-scoped cron scheduling with three tools: `CronCreate`, `CronList`, `CronDelete`.

| Aspect | Claude Code | OpenCode (proposed) |
|---|---|---|
| Persistence | Session-only by default, optional durable via `.claude/scheduled_tasks.json` | DB-persisted always, survives restart |
| Execution model | Enqueues prompt as `UserMessage` (`isMeta: true`) into the main agent loop | Direct task tool invocation + synthetic messages in parent session |
| Cron format | Standard 5-field cron | Same |
| Interval shorthand | `/loop` bundled skill | `/loop` built-in slash command (Go duration syntax) |
| Jitter | Recurring: up to 10% of period (max 15min). One-shot on `:00`/`:30`: up to 90s early | Not adopted — single-user CLI, no fleet coordination needed |
| Expiry | 7-day auto-expiry for recurring jobs (`DEFAULT_CRON_JITTER_CONFIG.recurringMaxAgeMs`) | No auto-expiry (explicit delete or session delete) |
| Task limit | 50 per session | Same, configurable |
| Idle gating | Fires only while REPL is idle, queued at `priority: 'later'` so user input takes precedence | Same — waits for agent idle on active session; fires directly on inactive sessions |
| Missed fires — recurring | No catch-up: reschedule forward from `now` after each fire | Same — anchor next_run_at from `now` after each fire, not from prior `next_run_at` |
| Missed fires — one-shot | Surfaced at startup with user confirmation (`AskUserQuestion`-style dialog) — user chooses "run now" or "discard" | Same — at `Start()`, load one-shots whose `next_run_at < now` and surface to the user on their next session activation |
| Context preservation | No context across fires — each is a fresh prompt | `task_id` reuse gives subagent full history of prior runs |
| Runtime killswitch | GrowthBook flag polled every tick (`isKilled?()` in scheduler) | `OPENCODE_DISABLE_CRON=1` env var polled at each tick — flipping it disables firing mid-session |

**Key implementation details from Claude's source code:**

1. **Minute-mark avoidance**: Claude's `CronCreate` prompt instructs the model to avoid `:00` and `:30` minutes for cron expressions when the user's request is approximate ("every morning around 9" → `"57 8 * * *"` not `"0 9 * * *"`). This spreads API load across the fleet. We adopt this as a best practice in our tool prompt — it still helps with LLM API rate limits if multiple OpenCode instances run concurrently.

2. **Queue priority**: Claude enqueues cron fires at `priority: 'later'` so user input always takes precedence. Our equivalent: the scheduler skips firing when the active session's agent is busy, and retries on the next tick.

3. **`isMeta: true` pattern**: Claude injects the cron prompt as a user message hidden from the transcript UI but visible to the model. We don't adopt this — our approach uses task tool invocation which runs in an isolated child session, keeping the parent conversation clean. Results are written back as synthetic `tool_call` + `tool_result` messages.

4. **Status badge**: Claude injects a `scheduled_task_fire` system message rendered as a dimmed `⊹ Running scheduled task (Jan 5 9:00am)` line. We adopt a similar pattern: a status bar `InfoMsg` flash with `⏲` icon.

5. **Scheduler lock**: Claude uses an `O_EXCL` lock file so only one session drives the scheduler for durable tasks. We don't need this — our DB-backed approach handles concurrency naturally via row-level state checks.

6. **One-shot auto-cleanup**: Claude removes one-shot tasks from the store after firing. We mark them `status="done"` instead — they remain visible in `/crons` but never fire again.

7. **Cron validation**: Claude validates that the expression parses correctly AND matches at least one calendar date in the next year (`nextCronRunMs()` returns non-null). We adopt both checks.

8. **Human-readable schedule**: Claude's `cronToHuman()` converts cron expressions to human-readable strings (e.g., `"*/5 * * * *"` → `"every 5 minutes"`). The return value from `croncreate` includes this for the agent to relay to the user.

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

**Status bar messaging** (`internal/tui/util/util.go`): `InfoMsg{Type, Msg, TTL}` displayed in the status bar with auto-clear. The status component (`core/status.go`) handles `InfoMsg` via `clearMessageCmd` with configurable TTL.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Tool names | `croncreate`, `crondelete`, `cronlist` (lowercase) | Consistent with existing tool naming (`bash`, `edit`, `task`) |
| Storage | Dedicated `cron_jobs` table with `ON DELETE CASCADE` from sessions | Automatic cleanup on session deletion; survives process restart |
| Execution model | Direct task tool `Run()` + synthetic messages in parent session | No LLM cost for parent agent per cron fire; conversation history stays complete for agent context |
| Context preservation | Reuse `task_id` across runs of same cron | Subagent retains conversation history from prior runs |
| Inactive session handling | Task tool runs directly; synthetic messages written to DB; status bar notification | No agent loop needed for non-active sessions; user sees results when they return |
| Active session handling | Same as inactive + live chat update via pubsub | Results appear in chat immediately |
| Scheduler | In-process goroutine with 1-second tick | Simple, no external dependencies; matches Claude's approach |
| Agent busy handling | Skip tick and retry next second when active session agent is busy | User input always takes precedence (equivalent to Claude's `priority: 'later'`) |
| Permission model | Permission keyed on cron job ID; supports "allow all" (persistent) and "allow once" | First run asks, subsequent runs skip if granted persistently |
| Default agent access | Only `hivemind` has cron tools enabled | Coder already has many tools; hivemind is the coordinator |
| `/loop` implementation | Built-in slash command (not a skill) | Needs direct access to cron service; skills can't create cron jobs |
| `/crons` implementation | TUI page (like agents/logs) | Consistent with existing table-detail pattern |
| Cron format | Standard 5-field (minute hour dom month dow) | Industry standard, same as Claude Code |
| One-shot support | `is_recurring` boolean on cron job; one-shot set to `status="done"` after execution | Remains visible in `/crons` for reference but never fires again |
| Max cron jobs per session | 50 (configurable) | Prevents runaway scheduling; matches Claude's limit |
| Icon | `⏲` | Simple unicode, visually distinct from other icons in `styles/icons.go` |
| Cross-session visibility | Cron indicator in session list + status bar flash on fire | Users know which sessions have active crons without opening them |
| Synthetic message atomicity | Both `tool_call` + `tool_result` written in a single DB transaction with consecutive seq numbers, under a per-session write mutex shared with the agent loop, **after** the task has finished running | Prevents interleaving with agent or user messages — a `tool_call` is always immediately followed by its `tool_result`. The task tool runs in a child session during the intervening time, writing only to the task session; the parent session sees both messages appear atomically once the task returns |
| `/loop` subagent type | `explorer` (hardcoded default) | Most scheduled tasks are read-only monitoring; explorer is safe. Agent-initiated crons via `croncreate` allow explicit subagent selection. Slash commands (`/loop 5m /foo`) work by embedding a "execute this skill" instruction in the prompt — the subagent resolves and invokes the skill tool itself |
| `/loop` task title | Prompt truncated to 80 chars | Avoids an LLM call for a cosmetic field; users who want a custom title use the agent's `croncreate` tool directly |
| `/loop` fires immediately | On creation, fire once right away, then on schedule thereafter | Matches user mental model ("every 5 min check the deploy" → check NOW and every 5 min) and Claude's `/loop` skill behavior |
| Next-run anchor after fire | Always `computeNextFire(schedule, time.Now())` — never anchored off prior `next_run_at` | Prevents rapid catch-up firing if scheduler was blocked for multiple windows (same as Claude's "reschedule from now, not from next") |
| In-flight guard | Job row carries `firing BOOLEAN` column (or in-memory set in scheduler) — set true at fire start, cleared on completion; `ListDueCronJobs` excludes `firing=true` rows | Prevents double-fire during the window between execution start and `next_run_at` update |
| Missed one-shot at startup | On `Start()`, load one-shots with `next_run_at < now AND status='active'`, publish a `MissedTasks` event; TUI surfaces a confirmation dialog on next session activation | User-pinned one-shots must not be silently dropped. Recurring missed fires still get no catch-up (reschedule forward from now) |
| Task tool invocation context | Pass sentinel `MessageIDContextKey = "cron:<cron_job_id>:<run_count>"` — task tool validates non-empty but doesn't otherwise consume it | Task tool requires `messageID` in context; a sentinel satisfies validation without needing to pre-insert the synthetic assistant message. This restores the "single DB transaction for both synthetic messages" invariant |
| Permission key scope | Key: `cron:<cron_job_id>`. Grant persistence: per-cron-job, stored in session permission service. Deleting the cron clears the grant. Restarting OpenCode preserves grants for still-existing jobs via the same session-scoped mechanism used for other persistent grants | Scoping per-cron-job (not per-session) prevents one "allow all" leaking to an unrelated future cron. Prompt content is not part of the key — editing the prompt via a future API would re-use the grant (cron prompts aren't editable today so this is moot) |
| Cron parser | `github.com/robfig/cron/v3` — `cron.ParseStandard()` only (no scheduler). Handles DST (skips spring-forward gaps) and dom/dow OR semantics (when both are constrained, either match fires — standard vixie-cron semantics) | Battle-tested, zero extra code. Matches Claude's `computeNextCronRun` behavior exactly |

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
│ ├── firing          BOOLEAN          (in-flight guard)      │
│ ├── last_run_at     INTEGER nullable (unix timestamp)       │
│ ├── next_run_at     INTEGER nullable (unix ts; NULL when    │
│ │                                     status=done)          │
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
│  └── direct task tool Run() invocation                      │
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
  → Validate cron expression parses and matches a future date
  → Enforce max 50 jobs per session
  → Generate cron job ID (8-char random)
  → Generate task_id (for task tool session reuse)
  → Compute next_run_at from cron expression
  → Insert into cron_jobs table
  → Publish CreatedEvent for TUI
  → Return confirmation with job ID, human-readable schedule, next fire time

STEP 2: Scheduler Tick (every 1 second)
────────────────────────────────────────
Scheduler goroutine wakes up
  → If OPENCODE_DISABLE_CRON is set: return (runtime killswitch)
  → Query: SELECT * FROM cron_jobs
            WHERE status = 'active'
              AND firing = false          -- in-flight guard
              AND next_run_at <= now
  → For each due job:
      IF job's session is the active session:
        → Check if session agent is busy (IsSessionBusy)
        → If busy: skip this tick, retry next second
        → If idle: proceed to execution
      ELSE (inactive session):
        → Proceed to execution immediately (no agent to conflict with)
      For serialization within one session: acquire per-session scheduler
      mutex (in-memory sync.Mutex keyed by session_id) before firing.
      Different sessions fire in parallel.

STEP 3: Cron Job Execution (direct task tool invocation)
────────────────────────────────────────────────────────
  → Mark job firing=true (DB update) — prevents the next tick from
    re-selecting this row while task is running.
  → Check permission using key "cron:<cron_job_id>":
      → If "allow all" was granted before: proceed
      → If "allow once" or first run: ask permission (active session only)
      → For inactive sessions with no prior persistent grant: skip,
        clear firing=false, retry when session becomes active
      → If denied: skip this run, clear firing=false, keep job active
  → Generate a unique call_id (e.g. "toolu_" + 16 hex chars — matches
    the format the provider layer produces for real tool calls).
  → Build ctx with:
      → SessionIDContextKey  = cron_job.session_id
      → MessageIDContextKey  = "cron:<cron_job_id>:<run_count>"
        (sentinel — task tool validates non-empty but doesn't otherwise
        use it; downstream consumers that care can detect the "cron:"
        prefix)
  → Invoke taskTool.Run(ctx, tools.ToolCall{
        ID:    call_id,
        Input: {prompt, subagent_type, task_id, task_title},
    })
    This creates or resumes the child task session and runs the
    subagent to completion. Parent session is untouched during this
    time — no messages appear in the parent until the task returns.
  → Wait for task completion (seconds to minutes).

  → Write synthetic messages into parent session (ATOMIC, AFTER task returns):
      → Acquire per-session write mutex (shared with the agent loop —
        same lock the agent holds when writing its own tool_call/
        tool_result pair). While we hold it, the agent cannot start
        a new turn and insert interleaving messages.
      → In a single DB transaction:
          → Insert synthetic assistant message with parts[tool_call]
            (seq=N, tool_call.id=call_id)
          → Insert tool_result message (seq=N+1,
            tool_call_id=call_id, content=task output)
      → Commit transaction, release mutex.
      → Publish both messages via session pubsub (active session sees
        them in chat live). Pubsub publication MUST follow the commit
        so subscribers never observe a half-written pair.

  → Update cron_job row (single UPDATE under the same mutex or a
    separate transaction — it doesn't affect conversation ordering):
      → last_run_at = now
      → run_count   = run_count + 1
      → last_result = <task output, truncated to reasonable size>
      → next_run_at = computeNextFire(schedule, time.Now())
                      — ALWAYS anchor from NOW (not from prior
                        next_run_at) to avoid rapid catch-up if the
                        scheduler was blocked for multiple windows.
      → If one-shot (is_recurring=false):
          status = "done", next_run_at = NULL
      → firing = false
      → error  = NULL (cleared on successful run)
  → Publish UpdatedEvent for TUI (updates /crons table, session list
    indicator).

STEP 4: Status Bar Notification
────────────────────────────────
On every cron fire (regardless of active/inactive session):
  → Emit InfoMsg to status bar:
      "⏲ <truncated_session_title>: <truncated_task_title>"
      TTL: 5 seconds, Type: InfoTypeInfo
  → If the session has an active chat view, synthetic messages
    appear in the message list via pubsub

STEP 5: Process Restart Resume
──────────────────────────────
On startup, CronService.Start():
  → Load all cron jobs with status="active" from DB.
  → Clear any stale firing=true rows (a prior process crashed
    mid-execution — the row is no longer in flight).
  → For each active job, compute next_run_at:
      → If job IS recurring:
          → next_run_at := computeNextFire(schedule, time.Now())
          → No catch-up: missed recurring windows are dropped forward.
      → If job IS NOT recurring (one-shot):
          → IF stored next_run_at < now  (missed one-shot):
              → Collect into a "missed one-shots" list for surfacing.
              → Leave next_run_at as-is until user decides.
          → ELSE:
              → Keep stored next_run_at (future fire still valid).
  → If missed one-shots collected:
      → Publish MissedOneShotsEvent carrying the list.
      → TUI subscribes; on next session activation, it renders a
        confirmation dialog per job: [Run Now] [Discard] [Keep For Later].
      → User choice:
          → "Run Now" → set next_run_at = now, scheduler fires on next tick.
          → "Discard" → set status="done".
          → "Keep For Later" → leave untouched; dialog re-appears next startup.
      → The dialog content wraps the prompt in a code fence so a
        multi-line imperative prompt is NOT interpreted as immediate
        instructions (prevents self-inflicted prompt injection, per
        Claude's `buildMissedTaskNotification`).
  → Start scheduler goroutine.
```

### Synthetic Message Format

When a cron job fires, the scheduler writes two messages directly to the parent session's DB (bypassing the LLM):

**Message 1 — Synthetic assistant message with tool_call:**

```json
{
  "role": "assistant",
  "parts": [{
    "type": "tool_call",
    "name": "task",
    "id": "<unique_call_id>",
    "input": {
      "prompt": "<cron prompt>",
      "subagent_type": "<subagent_type>",
      "task_id": "<cron task_id>",
      "task_title": "⏲ <task_title>"
    }
  }]
}
```

**Message 2 — Tool result message:**

```json
{
  "role": "tool",
  "parts": [{
    "type": "tool_result",
    "tool_call_id": "<same_call_id>",
    "content": "<task output text>"
  }]
}
```

These are structurally identical to what the agent would produce if it called the task tool itself. The parent agent sees them as normal conversation history on its next turn — full context without any LLM cost during the cron fire.

**Atomicity requirement**: Both messages MUST be written in a single DB transaction with consecutive sequence numbers, under a per-session write mutex that the agent loop also holds when writing messages. The task tool runs to completion FIRST (it writes only to the child task session during execution, never to the parent); the synthetic pair is only inserted into the parent AFTER the task returns, which is why a single transaction is actually viable.

This prevents a race where a cron fires between an agent's `tool_call` and `tool_result` — which would corrupt the message pairing and break the conversation for Anthropic-style providers that require `tool_result` to immediately follow the assistant message containing the matching `tool_call`.

**`call_id` uniqueness**: generate a new `call_id` per fire (don't reuse across runs of the same cron). The `task_id` stays stable across fires (passed as input to the task tool, not as the call id) so the subagent session is resumed; the `call_id` is just the message-level identifier pairing the synthetic `tool_call` with its `tool_result`.

### Cross-Session Visibility

**Session list (ctrl+s sidebar):**

Sessions with active cron jobs show an indicator next to the title:

```
  My coding session
  Deploy monitoring          ⏲ 2
  Code review tracker        ⏲ 1
```

The indicator shows `⏲ N` where N is the count of active cron jobs. This is computed by querying `cron_jobs` where `session_id = ? AND status = 'active'` when rendering the session list, or cached via pubsub updates.

**Status bar flash on fire:**

Every cron fire triggers a status bar `InfoMsg`:

```
⏲ Deploy monitoring: check deploy status
```

Format: `⏲ <session_title truncated to 20 chars>: <task_title truncated to 30 chars>`

TTL: 5 seconds. This appears regardless of which session is active, giving the user awareness that background crons are running.

### Cron Tool Definitions

**croncreate** — superset of task tool params plus scheduling:

```go
type CronCreateParams struct {
    Schedule     string `json:"schedule"`          // required: 5-field cron expression
    Prompt       string `json:"prompt"`            // required: task prompt
    SubagentType string `json:"subagent_type"`     // required: e.g. "explorer"
    TaskTitle    string `json:"task_title"`         // required: short description
    IsRecurring  bool   `json:"is_recurring"`       // optional: default true
}
```

**crondelete**:

```go
type CronDeleteParams struct {
    ID string `json:"id"` // required: cron job ID returned by croncreate
}
```

**cronlist**: no parameters. Returns all cron jobs for the current session.

### Tool Prompts

These prompts are injected as the tool's `prompt` field, guiding the LLM on correct usage.

**croncreate prompt:**

```
Schedule a prompt to run at a future time via a subagent. The scheduled task runs
in an isolated child session using the task tool. Each run reuses the same task_id,
so the subagent retains full conversation history from prior runs.

Uses standard 5-field cron in the user's local timezone:
minute hour day-of-month month day-of-week.
"0 9 * * *" means 9am local — no timezone conversion needed.

## One-shot tasks (is_recurring: false)

For "remind me at X" or "at <time>, do Y" — fire once then mark as done.
Pin minute/hour/day-of-month/month to specific values:
  "remind me at 2:30pm today" → schedule: "30 14 <today_dom> <today_month> *", is_recurring: false
  "tomorrow morning, run the smoke test" → schedule: "57 8 <tomorrow_dom> <tomorrow_month> *", is_recurring: false

## Recurring jobs (is_recurring: true, the default)

For "every N minutes" / "every hour" / "weekdays at 9am":
  "*/5 * * * *" (every 5 min), "0 * * * *" (hourly), "0 9 * * 1-5" (weekdays at 9am local)

## Avoid the :00 and :30 minute marks when the task allows it

When the user's request is approximate ("every morning", "hourly", "in about an hour"),
pick a minute that is NOT 0 or 30:
  "every morning around 9" → "57 8 * * *" or "3 9 * * *" (not "0 9 * * *")
  "hourly" → "7 * * * *" (not "0 * * * *")
  "in an hour or so, remind me to..." → pick whatever minute you land on, don't round

Only use minute 0 or 30 when the user names that exact time and clearly means it
("at 9:00 sharp", "at half past"). This spreads LLM API load when multiple
OpenCode instances run concurrently.

## Subagent selection

Choose the subagent_type based on what the task needs:
  - "explorer": read-only tasks (monitoring, checking status, reviewing). Default for most scheduled tasks.
  - "workhorse": tasks that need to write files, run commands, or make changes.

## Runtime behavior

Jobs fire while the agent is idle — never mid-response. If the agent is busy when
a task comes due, it waits until the current turn finishes. Results are written as
synthetic messages into the parent session, so the conversation has full context of
what happened. The subagent retains history across runs via task_id reuse — it can
reference what it found in previous executions.

Returns a job ID you can pass to crondelete.
```

**crondelete prompt:**

```
Cancel a cron job previously scheduled with croncreate. Removes it from the
database. The job stops firing immediately. Pass the job ID returned by croncreate.
```

**cronlist prompt:**

```
List all cron jobs scheduled via croncreate in the current session. Shows each
job's ID, cron schedule, human-readable schedule description, prompt, subagent
type, status (active/done), and run count.
```

### Permission Flow

```
FIRST RUN of cron job "cron_a1b2c3":
────────────────────────────────────
  → permissions.Request(ctx, {
        SessionID: parentSessionID,
        ToolName:  "cron",
        Action:    "execute",
        Path:      "cron:cron_a1b2c3",       // scope key
        Description: "⏲ Cron job cron_a1b2c3: check deploy status (*/5 * * * *)",
        Params:    {"cron_id": "cron_a1b2c3", "prompt": "check deploy..."},
    })
  → TUI shows permission dialog:
      "⏲ Cron job cron_a1b2c3 wants to run:
       check deploy status
       Schedule: */5 * * * * (every 5 minutes)
       [Allow Once] [Allow All] [Deny]"
  → If "Allow All" → GrantPersistant(path="cron:cron_a1b2c3")
                  → future runs of THIS cron skip permission; other
                    crons still ask
  → If "Allow Once" → Grant() → next run asks again
  → If "Deny" → skip this run; job stays active; next fire asks again

PERMISSION KEY SCOPE (explicit):
  → Key: "cron:<cron_job_id>" — per-job, NOT per-session and NOT per-prompt.
  → Deleting the cron job also removes its grant (CronService.Delete
    calls permissions.RevokePath("cron:<id>")).
  → OpenCode restart: grants persist via the same mechanism that
    persists other tool permissions. Only jobs that still exist in
    the DB can consume them; grants for deleted cron IDs are orphans
    but harmless (they'll never match again).
  → A future cron with a different ID asks anew, even if the prompt
    is identical — the ID is the grant identity.

INACTIVE SESSION (no prior persistent grant):
  → Cannot show permission dialog (no active UI context for that session)
  → Skip execution, clear firing=false, retry when session becomes active.
  → When user switches to the session, pending crons can fire and
    ask permission.

AUTO-APPROVE INHERITANCE:
  → If parent session has auto-approve enabled,
    cron task sessions inherit it (same as task tool behavior)
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
4. Uses `explorer` as default subagent type (hardcoded — safe for monitoring/polling)
5. Uses prompt truncated to 80 chars as `task_title` (avoids LLM call for cosmetic field)
6. Sets `source: "loop"` to distinguish from agent-initiated crons
7. Shows confirmation via `InfoMsg` in status bar
8. **Fires once immediately** by setting `next_run_at = time.Now()` on insert — scheduler picks it up on its next tick. Subsequent runs follow the cron schedule normally. Matches user mental model: `/loop 5m check the deploy` checks now AND every 5 min thereafter.

### Slash commands inside `/loop`

Claude's `/loop 5m /babysit-prs` enqueues the raw `/babysit-prs` command into the main agent loop on each fire. OpenCode diverges — cron runs in subagents for isolation. To support the equivalent ("every 5 min, run this skill"), `/loop` with a prompt starting with `/` wraps it as an explicit instruction to the subagent:

```
/loop 5m /babysit-prs
```

is scheduled with:

```
prompt: "Invoke the skill tool with name=\"babysit-prs\" and no arguments. Report the skill's output verbatim as your final response."
```

The subagent sees the instruction, calls the `skill` tool, and surfaces the result. This works because subagents also have access to the skill tool. A `/loop 5m /foo arg1 arg2` is parsed as `skill=foo, args="arg1 arg2"` and forwarded the same way.

This gives users the "schedule a slash command" ergonomic while preserving the subagent isolation boundary.

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

### TUI Rendering of Cron Tool Calls in Chat

Cron-fired synthetic messages in the chat render like task tool calls but with a cron indicator. The `⏲` prefix on `task_title` is the signal — the message renderer detects it and styles accordingly:

```
┌─ ⏲ Task (*/5 * * * *) ────────────────────────────────────┐
│ cron_a1b2 → explorer: check if the deployment finished     │
│                                                             │
│ > Deployment complete. All 5/5 pods running.               │
│ > Build sha: a1b2c3d4                                      │
└─────────────────────────────────────────────────────────────┘
```

The `renderToolParams` function in `message.go` checks for the `⏲` prefix in the task title and renders with distinct styling (dimmed schedule in the header).

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

This ensures the existing `AgentModeAgent` check in `NewToolSet` prevents subagents from accessing cron tools. Additionally, `coder` has them disabled by default — only `hivemind` gets them out of the box. Users can override this in `.opencode.json`:

```json
{
  "agents": {
    "coder": {
      "tools": {
        "croncreate": true,
        "crondelete": true,
        "cronlist": true
      }
    }
  }
}
```

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
    Firing        bool    // in-flight guard — true while task is executing
    LastRunAt     int64   // 0 = never run
    NextRunAt     int64   // 0 = no scheduled fire (status=done)
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
    ListActive(ctx context.Context) ([]CronJob, error)
    ListDue(ctx context.Context, now int64) ([]CronJob, error) // status=active AND firing=false AND next_run_at<=now
    CountActive(ctx context.Context, sessionID string) (int, error)
    Get(ctx context.Context, id string) (CronJob, error)

    // One-shot missed fires surfaced at startup. TUI subscribes to
    // MissedOneShotsEvent and renders per-job confirmation dialogs.
    ResolveMissedOneShot(ctx context.Context, id string, action MissedAction) error
    // MissedAction = RunNow | Discard | Keep

    Start(ctx context.Context) error   // starts scheduler; clears stale firing=true rows;
                                        // recomputes next_run_at for recurring;
                                        // collects missed one-shots; publishes MissedOneShotsEvent
    Stop()                             // stops scheduler goroutine
}
```

### Environment Variable

`OPENCODE_DISABLE_CRON=1` disables the cron system.

At startup (once, via `os.Getenv`):
- Cron tools are not registered (not added to `managerToolNames`)
- `/loop` slash command shows "Cron scheduling is disabled"
- `/crons` page shows "Cron scheduling is disabled"

Runtime (polled at every scheduler tick):
- If the env var flips to truthy mid-session (e.g. exported in a new shell
  then the process is signal-reloaded via a hypothetical SIGHUP, or the
  operator wraps the binary), `check()` bails before firing anything.
  Existing cron jobs remain in the DB but don't fire.
- Polling (`os.Getenv` per tick) is cheap and matches Claude's runtime
  killswitch pattern (`isKilled?()`).
- Note: Go doesn't hot-reload env vars in the same process by default;
  the runtime poll is for operators who may arrange reloading via signals
  or orchestrators. For single-user interactive use, startup-only is
  functionally equivalent.

Any existing active cron jobs in the DB remain persisted; they resume
firing when the env var is cleared and the process restarts.

## Implementation Plan

### Phase 1: Database Layer

- [ ] **1.1** Create SQLite migration `internal/db/migrations/sqlite/20260325120000_add_cron_jobs.sql` with `cron_jobs` table (including `firing BOOLEAN DEFAULT 0`), `ON DELETE CASCADE` from sessions, indexes on `session_id` and `(status, firing, next_run_at)`, `updated_at` trigger
- [ ] **1.2** Create MySQL migration `internal/db/migrations/mysql/20260325120000_add_cron_jobs.sql` (same schema, MySQL syntax)
- [ ] **1.3** Create sqlc query files `internal/db/sql/cron_jobs.sql` and `internal/db/sql/mysql/cron_jobs.sql` with operations: `CreateCronJob`, `GetCronJob`, `ListCronJobsBySession`, `ListActiveCronJobs`, `ListDueCronJobs` (status=active AND firing=false AND next_run_at <= ?), `ListMissedOneShots` (status=active AND is_recurring=false AND next_run_at < ?), `CountActiveCronJobsBySession`, `SetCronJobFiring` (set firing flag), `ClearStaleFiring` (set firing=false on startup), `UpdateCronJob`, `DeleteCronJob`
- [ ] **1.4** Add `CronJob` model to `internal/db/models.go`
- [ ] **1.5** Run sqlc generation, update `Querier` interface
- [ ] **1.6** Update `internal/db/schema/mysql.sql` with the new table

### Phase 2: Cron Service

- [ ] **2.1** Create `internal/cron/service.go` with `Service` interface, `CronJob` struct, `CreateParams`, and `MissedAction` enum
- [ ] **2.2** Add `github.com/robfig/cron/v3` dependency; implement `computeNextFire(schedule, from time.Time) (time.Time, error)` using `cron.ParseStandard()`. Verify dom/dow OR semantics and DST skip-forward behavior match expectations
- [ ] **2.3** Implement human-readable cron description (e.g., `"*/5 * * * *"` → `"every 5 minutes"`). Port the branch-table approach from Claude's `cronToHuman` — covers common patterns, falls through to raw cron string otherwise
- [ ] **2.4** Implement validation: parse schedule, verify `computeNextFire(schedule, now)` returns a time within the next 366 days, enforce max 50 jobs per session
- [ ] **2.5** Implement scheduler goroutine: 1-second tick, runtime poll of `OPENCODE_DISABLE_CRON`, query `ListDueCronJobs`, per-session serialization via `map[sessionID]*sync.Mutex`, check `IsSessionBusy` for active session
- [ ] **2.5.1** Implement per-session write mutex shared between cron scheduler and agent loop (exposed via session service or agent service) to prevent synthetic `tool_call`/`tool_result` interleaving with agent-authored pairs
- [ ] **2.6** Implement execution logic:
  - [ ] **2.6.1** Set `firing=true` on the row (DB UPDATE) before starting work
  - [ ] **2.6.2** Permission check via `permissions.Request(path="cron:<id>")`; on inactive session + no prior grant, clear firing and return
  - [ ] **2.6.3** Generate unique call_id; build ctx with sessionID and sentinel messageID `"cron:<cron_job_id>:<run_count>"`
  - [ ] **2.6.4** Invoke `taskTool.Run(ctx, ToolCall{ID: call_id, Input: ...})` and wait
  - [ ] **2.6.5** Acquire per-session write mutex; in a single DB transaction, insert synthetic assistant message with `tool_call` part and tool_result message with matching `tool_call_id`; commit and release
  - [ ] **2.6.6** Publish both messages via session pubsub (AFTER commit)
  - [ ] **2.6.7** Update cron_job: `last_run_at=now`, `run_count++`, `last_result`, `next_run_at = computeNextFire(schedule, time.Now())`, `firing=false`, `error=NULL`; for one-shot: `status="done"`, `next_run_at=NULL`
- [ ] **2.7** Implement `Start()` for resume-on-restart:
  - [ ] **2.7.1** `ClearStaleFiring()` — reset `firing=false` for any row left as firing=true from a prior crash
  - [ ] **2.7.2** For recurring active jobs: `next_run_at = computeNextFire(schedule, time.Now())` (drop missed recurring windows forward)
  - [ ] **2.7.3** For one-shot active jobs: `ListMissedOneShots(now)` → publish `MissedOneShotsEvent` for TUI
  - [ ] **2.7.4** Implement `ResolveMissedOneShot(id, action)` — RunNow sets next_run_at=now; Discard sets status=done; Keep is a no-op
- [ ] **2.8** Implement Go duration to cron expression conversion for `/loop` interval syntax (table from the `/loop` spec section — Nm, Nh, Nd; round Ns to ceil(N/60)m; warn user when rounding)
- [ ] **2.9** Add pubsub broker for TUI updates (`CronJob` events + `MissedOneShotsEvent`)
- [ ] **2.10** Listen for session `DeletedEvent` to clean up in-memory state (per-session mutexes) and call `permissions.RevokePath("cron:<id>")` for each deleted job (though CASCADE DELETE handles the DB side)

### Phase 3: Tools

- [ ] **3.1** Create `internal/llm/tools/cron.go` with `croncreate`, `crondelete`, `cronlist` tool implementations
- [ ] **3.2** Add `CronCreateToolName`, `CronDeleteToolName`, `CronListToolName` constants
- [ ] **3.3** Add cron tool names to `managerToolNames` in `internal/llm/agent/tools.go`
- [ ] **3.4** Update built-in agent registrations in `internal/agent/registry.go`: disable cron tools for `coder`, keep enabled for `hivemind`
- [ ] **3.5** Add tool prompts (as defined in this spec) guiding LLM on cron expression construction, subagent selection, and minute-mark avoidance
- [ ] **3.6** Implement `croncreate` validation: parse cron expression, verify it matches a future date within the next year, enforce max 50 jobs per session
- [ ] **3.7** Implement permission checking: use cron job ID as permission key, support persistent grant for recurring jobs
- [ ] **3.8** Add `CronIcon = "⏲"` to `internal/tui/styles/icons.go`

### Phase 4: Slash Commands & TUI

- [ ] **4.1** Add `/loop` slash command in `internal/tui/tui.go` `buildCommands`: parse interval + prompt; if prompt starts with `/`, wrap as explicit skill-invocation instruction for the subagent (see `/loop` spec section); call `CronService.Create()` with `next_run_at = time.Now()` so it fires immediately; show confirmation via `InfoMsg`
- [ ] **4.2** Add `/crons` slash command that navigates to the crons TUI page
- [ ] **4.3** Create `internal/tui/components/crons/table.go` — cron jobs table with columns: ID, Schedule, Source, Status, Runs, Next Run
- [ ] **4.4** Create `internal/tui/components/crons/details.go` — detail panel showing full job info + latest output
- [ ] **4.5** Create `internal/tui/page/crons.go` — page with table + details layout (same as agents/logs pattern)
- [ ] **4.6** Register crons page in `internal/tui/tui.go`, add keybinding for navigation
- [ ] **4.7** Add delete keybinding in crons table (`d` key)
- [ ] **4.8** Add `⏲ N` indicator to session list rendering for sessions with active crons
- [ ] **4.9** Emit status bar `InfoMsg` on every cron fire: `"⏲ <session>: <task>"` with 5s TTL
- [ ] **4.10** Implement missed-one-shot confirmation dialog: subscribe to `MissedOneShotsEvent`; on next session activation for an affected session, render a dialog per job with `[Run Now] [Discard] [Keep For Later]` actions that call `CronService.ResolveMissedOneShot`. Wrap the prompt body in a code fence in the dialog to prevent self-inflicted prompt injection (same rationale as Claude's `buildMissedTaskNotification`)

### Phase 5: Chat Rendering

- [ ] **5.1** Update `renderToolParams` in `message.go` to detect `⏲` prefix in task title and render cron-specific header (schedule + cron icon)
- [ ] **5.2** Add `getToolName` and `getToolAction` entries for cron tools
- [ ] **5.3** Ensure synthetic messages appear in the active session's chat list via pubsub subscription

### Phase 6: Integration

- [ ] **6.1** Wire `CronService` into `app.App`, initialize on startup (`Start()`), shutdown (`Stop()`)
- [ ] **6.2** Pass `CronService` to tool constructors and slash command handlers
- [ ] **6.3** Handle `OPENCODE_DISABLE_CRON` environment variable: skip tool registration, service startup, and slash command availability
- [ ] **6.4** Update `AGENTS.md` with cron-related commands and configuration

### Phase 7: Testing

- [ ] **7.1** Unit tests for cron expression parsing and Go duration conversion
- [ ] **7.2** Unit tests for human-readable cron description generation
- [ ] **7.3** Unit tests for cron service CRUD operations
- [ ] **7.4** Unit tests for scheduler logic (due job detection, busy waiting, one-shot cleanup, per-session serialization, in-flight guard prevents double-fire)
- [ ] **7.5** Unit tests for tool parameter validation (invalid cron expression, no-future-match expression, max jobs, etc.)
- [ ] **7.6** Unit tests for synthetic message creation (correct format, proper tool_call/tool_result pairing, consecutive seq under mutex)
- [ ] **7.7** Unit test: next_run_at after fire always anchored from `time.Now()`, not from prior next_run_at (simulate blocked scheduler, verify no rapid catch-up)
- [ ] **7.8** Unit test: startup resume clears stale `firing=true` rows; recurring jobs reschedule from now; one-shot missed jobs collected and `MissedOneShotsEvent` published
- [ ] **7.9** Unit test: `ResolveMissedOneShot` for each action (RunNow / Discard / Keep)
- [ ] **7.10** Unit test: `/loop` parsing handles `5m prompt`, `prompt every 5m`, bare `prompt` (default interval), leading slash-command wrapping, and rounds sub-minute durations up
- [ ] **7.11** Unit test: permission key `cron:<id>` — first run requests, "allow all" grants, subsequent runs skip; deleting the cron revokes the grant
- [ ] **7.12** Integration test: create cron → wait for execution → verify task tool was invoked with correct params → verify synthetic messages in parent session with matching call_id, consecutive seq, and correct tool_call_id pairing
- [ ] **7.13** Integration test: fire cron while agent is mid-turn on active session → verify cron waits for agent idle → verify messages don't interleave

## Edge Cases

### Agent Busy When Cron Fires (Active Session)

1. Cron job `cron_a1b2` is due at 14:35
2. Agent is mid-response on a user request
3. Scheduler detects `IsSessionBusy(sessionID) == true`
4. Scheduler skips this tick, retries every second
5. Agent finishes at 14:37, scheduler detects idle state
6. Cron job fires at 14:37
7. No catch-up: only one execution, not two

### Cron Fires on Inactive Session

1. User is viewing session A, session B has a cron job due
2. Scheduler fires the cron job for session B directly (no busy check needed)
3. Task tool runs in a child session, produces output
4. Synthetic messages written to session B's DB
5. Status bar flashes: `⏲ Session B: check deploy status` for 5 seconds
6. Session list shows `⏲ 1` next to session B
7. When user switches to session B, they see the synthetic messages in the conversation
8. On user's next prompt, the parent agent has full context of cron results

### Permission on Inactive Session (No Prior Grant)

1. Cron job fires for inactive session, no persistent permission granted yet
2. Cannot show permission dialog (no active UI context for that session)
3. Scheduler skips this execution, job remains active, next_run_at not updated
4. When user switches to that session and agent becomes idle, the cron fires and permission dialog appears
5. User can grant "allow all" to enable future background executions

### Session Deleted While Cron Active

1. User deletes session containing active cron jobs
2. `ON DELETE CASCADE` in DB removes all `cron_jobs` rows
3. Session service publishes `DeletedEvent`
4. CronService listens for session deletions, removes in-memory state for that session
5. Scheduler never fires those jobs again

### Process Restart

1. OpenCode exits (ctrl+c or crash)
2. Scheduler goroutine stops, in-memory state lost. Rows with `firing=true` are left in that state in the DB (a row was in the middle of firing when we crashed — the synthetic messages may or may not have been written).
3. OpenCode restarts, user opens same session
4. `CronService.Start()`:
   - `ClearStaleFiring()` — sets `firing=false` on every row; the task that was mid-flight is effectively abandoned (no way to recover the subagent state cleanly). The next cron tick re-fires it. Duplicate synthetic messages are possible if the pre-crash process had already committed them but hadn't updated `next_run_at`; the idempotency of `task_id` reuse means the subagent sees a retry in its history, which is acceptable.
   - For each recurring active job: `next_run_at = computeNextFire(schedule, time.Now())` — no catch-up for missed recurring windows.
   - For one-shot active jobs with `next_run_at < now`: collect into missed list, publish `MissedOneShotsEvent`. TUI surfaces a per-job confirmation dialog on next session activation; user chooses Run Now / Discard / Keep For Later. Prompt body is rendered inside a code fence to prevent self-inflicted prompt injection.
5. Scheduler resumes normal operation.

### Missed One-Shot While Process Was Down

1. User creates one-shot: "remind me at 3pm to push the release" → cron `0 15 * * *`, `is_recurring=false`, `next_run_at=<today 15:00>`
2. OpenCode crashes at 2:45pm, user restarts at 3:30pm
3. `CronService.Start()` finds this job: `next_run_at=15:00 < now=15:30` AND `is_recurring=false` → adds to missed list.
4. `MissedOneShotsEvent` published with `[{id: ..., cron: "0 15 * * *", prompt: "remind me...", missed_at: 15:00}]`
5. TUI renders a dialog on the affected session:
   ```
   ⏲ Missed scheduled task:
   ```
   remind me at 3pm to push the release
   ```
   Original fire: today 15:00 (30 minutes ago)
   [Run Now]  [Discard]  [Keep For Later]
   ```
6. User picks:
   - "Run Now" → `ResolveMissedOneShot(id, RunNow)` sets `next_run_at=now`; scheduler fires on next tick.
   - "Discard" → sets `status="done"`; job never fires but stays visible in `/crons`.
   - "Keep For Later" → no-op; dialog re-appears next startup.

### Scheduler Blocked for Multiple Windows (No Rapid Catch-Up)

1. Cron job `*/5 * * * *` fires normally for hours.
2. Agent is busy for 47 minutes (long analysis run) on the active session.
3. During those 47 min, 9 fire windows pass (15:00, 15:05, 15:10, ..., 15:40).
4. Agent becomes idle at 15:47.
5. Scheduler's next tick (at 15:47) detects the cron is due (`next_run_at=15:00 <= now`).
6. Cron fires ONCE at 15:47.
7. `next_run_at = computeNextFire("*/5 * * * *", 15:47)` = 15:50 — anchored from NOW, not from 15:00.
8. Next fire at 15:50, not rapid-fire through 15:00→15:40. Matches Claude's `reschedule from now, not from next`.

### Double-Fire Prevention (In-Flight Guard)

1. Scheduler tick at 15:00:00: job `cron_a1b2c3` is due, `firing=false`.
2. Scheduler marks `firing=true` (DB UPDATE) and starts executing.
3. Task takes 3 seconds.
4. Scheduler tick at 15:00:01 while task is still running: `ListDueCronJobs` filters `firing=false` → the row is excluded. No double-fire.
5. Task completes at 15:00:03. Scheduler writes synthetic messages, updates `next_run_at`, sets `firing=false`.
6. Subsequent ticks see the job again only when `next_run_at <= now` AND `firing=false`.

### Concurrent Cron Fires (Same Session)

1. Two cron jobs due at the same time for the same session
2. Per-session mutex in scheduler serializes execution
3. First job executes, completes, synthetic messages written atomically
4. Second job executes, completes, synthetic messages written atomically
5. Both results appear in conversation in deterministic order
6. For active session: agent busy check applies to each sequentially

### Message Interleaving Race

1. Agent finishes a turn (`activeRequests.Delete`), becomes idle
2. Cron scheduler detects idle, begins executing cron job
3. Simultaneously, user sends a new message which triggers agent `Run()`
4. Without mutex: cron's synthetic `tool_call` gets seq=N, user message gets seq=N+1, cron's `tool_result` gets seq=N+2 — corrupted pairing
5. With per-session write mutex: cron holds the lock while writing both synthetic messages atomically, user message waits, gets seq=N+2 — clean conversation

### One-Shot Completion

1. User schedules: "remind me at 3pm to push the release"
2. Cron expression: `0 15 * * *`, `is_recurring: false`
3. Job fires at 15:00, task executes
4. After completion: `status` set to `"done"`, `next_run_at` cleared
5. Job remains in DB and `/crons` table for visibility but never fires again

### Permission Denied

1. Cron job fires, permission dialog shown
2. User selects "Deny"
3. This run is skipped, job stays active
4. Next scheduled time: permission asked again
5. Job is NOT deleted on denial — only skipped

### Max Jobs Per Session

1. Session already has 50 active cron jobs
2. Agent calls `croncreate` for a 51st job
3. Tool returns error: "Too many scheduled jobs (max 50). Cancel one first."
4. Same validation in `/loop` slash command

### Invalid Cron Expression

1. Agent calls `croncreate` with `schedule: "invalid"`
2. Tool validation fails: "Invalid cron expression 'invalid'. Expected 5 fields: M H DoM Mon DoW."
3. No job created

### Cron Expression Matches No Future Date

1. Agent calls `croncreate` with `schedule: "0 0 31 2 *"` (Feb 31, never happens)
2. Tool validation: "Cron expression '0 0 31 2 *' does not match any calendar date in the next year."
3. No job created

## Open Questions

1. **Should cron tools be available to `coder` by default, or only `hivemind`?**
   - Currently proposed: only `hivemind` by default, users can enable for `coder` via config
   - **Recommendation**: Start with `hivemind`-only, gather feedback. The coder agent already has many tools, and scheduling is a coordination concern that fits hivemind's role.

2. **Should there be an auto-expiry like Claude's 7-day limit?**
   - Claude auto-expires recurring tasks after 7 days (`DEFAULT_CRON_JITTER_CONFIG.recurringMaxAgeMs`) to prevent forgotten loops and bound session lifetime.
   - **Recommendation**: Skip for now. Since OpenCode persists cron jobs and shows them in `/crons`, users have visibility. Can add later if needed via a configurable `max_lifetime` field.

3. **Should `/loop` default to `explorer` or use a configurable default subagent?**
   - **Recommendation**: Default to `explorer` (read-only, safe for automated polling). Allow overriding via syntax like `/loop 5m --agent workhorse check and fix the build`.

4. **Should cron jobs from one session be visible in `/crons` when viewing a different session?**
   - **Recommendation**: Show only current session's cron jobs by default. A "show all" toggle could be added later.

5. **Concurrent fire behavior for inactive sessions — parallel or serial?**
   - The task tool has `AllowParallelism: true`, so concurrent fires on the same inactive session would work. But synthetic message ordering could get confusing.
   - **Recommendation**: Serialize per-session (use a per-session mutex in the scheduler). This keeps message ordering deterministic. Different sessions can fire in parallel.

6. **`/loop` with slash-command prompts — subagent + skill tool or execute directly?**
   - OpenCode runs all cron fires in subagents for isolation. Claude runs them in the main agent, so `/loop 5m /babysit-prs` directly enqueues the slash command.
   - **Decision**: Subagents always, but when the prompt starts with `/`, wrap it as an explicit instruction to the subagent to invoke the `skill` tool with the parsed skill name and args. Subagents already have skill tool access. This preserves isolation while giving users the "schedule a slash command" ergonomic.

7. **Missed one-shot handling at restart**
   - **Decision**: Adopt Claude's approach. Surface missed one-shots with a Run Now / Discard / Keep For Later confirmation dialog on next session activation. Wrap the prompt body in a code fence to prevent self-inflicted prompt injection.
   - Recurring missed windows continue to drop forward with no catch-up (the schedule itself is the safeguard — a `*/5 * * * *` that missed 9 windows still has the 10th window coming).

## Success Criteria

- [ ] `croncreate` tool creates a persistent cron job stored in DB
- [ ] `crondelete` removes a cron job by ID
- [ ] `cronlist` returns all cron jobs for the current session with human-readable schedules
- [ ] Cron jobs execute on schedule via direct task tool invocation
- [ ] Synthetic `tool_call` + `tool_result` messages written to parent session on each fire
- [ ] Active session sees cron results in chat live via pubsub
- [ ] Inactive session has results stored in DB, visible when user switches to it
- [ ] `task_id` is reused across runs — subagent retains conversation history
- [ ] Deleting a session cascade-deletes its cron jobs from DB
- [ ] Process restart resumes active cron jobs from DB
- [ ] `/loop 5m <prompt>` creates a recurring cron job from the TUI
- [ ] `/crons` opens a table page showing all session cron jobs with delete capability
- [ ] Status bar shows `⏲ <session>: <task>` flash on every cron fire (5s TTL)
- [ ] Session list shows `⏲ N` indicator for sessions with active crons
- [ ] Cron tools are only available to primary agents (not subagents)
- [ ] Only `hivemind` has cron tools enabled by default
- [ ] Permission is asked on first cron execution; "allow all" skips future prompts
- [ ] Permission on inactive sessions is deferred until session becomes active
- [ ] Agent busy state is respected on active session — cron waits for idle before firing
- [ ] Cron fires per-session are serialized to maintain deterministic message ordering
- [ ] Synthetic message pairs are written atomically (single transaction AFTER task completes, consecutive seq) — no interleaving with agent/user messages
- [ ] `call_id` is unique per fire; `tool_call_id` in the tool_result message matches the `tool_call.id` in the synthetic assistant message
- [ ] Task tool is invoked with sentinel `MessageIDContextKey = "cron:<id>:<run_count>"` — validation passes, downstream unaffected
- [ ] `next_run_at` after each fire is anchored from `time.Now()` — scheduler blocked for multiple windows produces a single fire on resume, not rapid catch-up
- [ ] `firing=true` in-flight guard prevents double-fire during the task execution window
- [ ] Startup: `ClearStaleFiring()` resets firing flag on rows left from prior crashes
- [ ] Startup: missed one-shots (status=active AND is_recurring=false AND next_run_at<now) are surfaced via `MissedOneShotsEvent`; TUI renders Run Now / Discard / Keep For Later dialog with prompt body in a code fence
- [ ] Startup: recurring jobs reschedule from now; no catch-up for missed recurring windows
- [ ] `/loop` fires immediately on creation (next_run_at = now), then follows the cron schedule
- [ ] `/loop` with a slash-command prompt (`/loop 5m /foo bar`) wraps it as an instruction for the subagent to invoke the `skill` tool
- [ ] Permission key `cron:<id>` is per-job; "allow all" persists only for that cron; deleting the cron revokes the grant
- [ ] Cron parser is `github.com/robfig/cron/v3` `ParseStandard()`; DST skip-forward and dom/dow OR semantics work correctly
- [ ] Both SQLite and MySQL providers support the `cron_jobs` table
- [ ] `OPENCODE_DISABLE_CRON=1` disables the cron system at startup; runtime tick also polls the env var for mid-session kill
- [ ] Cron expression validation rejects invalid expressions and expressions with no future match
- [ ] `make test` passes

## References

- `internal/llm/agent/agent-tool.go` — Task tool implementation (cron invokes `Run()` directly)
- `internal/llm/agent/tools.go` — `managerToolNames` and `NewToolSet` (cron tools added here)
- `internal/agent/registry.go` — Built-in agent registration (tool access control)
- `internal/db/migrations/sqlite/20260220120000_add_flow_states.sql` — Migration pattern to follow
- `internal/db/sql/flow_states.sql` — sqlc query pattern to follow
- `internal/session/session.go` — Session service (cascade delete, event publishing)
- `internal/permission/permission.go` — Permission service (request/grant/deny flow)
- `internal/tui/page/agents.go` — TUI page pattern (table + details layout)
- `internal/tui/page/logs.go` — TUI page pattern (pubsub subscription for live updates)
- `internal/tui/tui.go` — Slash command registration in `buildCommands`, status bar routing
- `internal/tui/components/core/status.go` — Status bar component (InfoMsg handling, widget rendering)
- `internal/tui/util/util.go` — `InfoMsg` type for status bar messages
- `internal/tui/styles/icons.go` — Icon constants (add `CronIcon`)
- `internal/tui/components/chat/message.go` — Tool call rendering (`renderToolParams`, `getToolName`, `getToolAction`)
- `internal/tui/components/dialog/commands.go` — Command struct for slash commands
- `internal/llm/tools/skill.go` — Permission check pattern for tools
- Claude Code source: `src/tools/ScheduleCronTool/` — Reference implementation (tool definitions, prompts, UI rendering)
- Claude Code source: `src/utils/cronScheduler.ts` — Reference scheduler (1s tick, idle gating, jitter, lock)
- Claude Code source: `src/utils/cronTasks.ts` — Reference task store (jitter calculation, fire tracking)
- [Claude Code scheduled tasks docs](https://code.claude.com/docs/en/scheduled-tasks) — Public documentation
