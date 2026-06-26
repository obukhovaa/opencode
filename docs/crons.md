# Crons

Crons schedule prompts to run at a future time — once or on a recurring cadence — inside a child subagent session. Each fire executes via the [task tool](./flows.md#task-tool-bridge), so the scheduled work runs in an isolated session that inherits the parent session's context but never blocks the user.

Use crons for: "remind me at 2pm to check the deploy", "every 5 minutes, tail the build logs", "every weekday at 9am, summarize overnight CI failures".

## How it works

A cron job is a database row with a 5-field cron expression and a prompt. A scheduler ticks once per second, finds due jobs, and asks the task tool to spawn the configured subagent with the prompt. When the subagent finishes, its output is committed as a synthetic `task` tool_call + tool_result pair into the parent session — so the conversation history reads as if the parent agent had run the task itself.

Key properties:

- **Isolated from the parent session**: each fire runs in a child subagent session, not the parent. The parent agent isn't paused; you can keep working.
- **Same session reused across fires**: every fire of the same cron job reuses the **same** `task_id` (which is the subagent's session ID). The subagent resumes its prior conversation — full history of every previous fire is in context for the next one.
- **At-most-once per fire window**: a `firing` flag plus an atomic `ClaimForFiring` SQL update prevents duplicate execution if multiple scheduler ticks race.
- **Survives restart**: jobs persist in the database. On startup, recurring schedules reanchor from now (missed windows are dropped); one-shots whose fire time passed during downtime surface in a confirmation dialog.

### Context accumulates across fires

Because every fire reuses the same subagent session, conversation history grows unboundedly. A `/loop 5m` job running for a day accumulates ~288 fires worth of turns, tool calls, and outputs in a single session. Implications:

- **The model's context window will eventually fill.** Long-lived recurring crons should produce short outputs.
- **A bad fire poisons subsequent fires.** If one fire derails (errors, wrong tool path, loops), that text becomes context for the next fire. Cancel and recreate if a recurring cron starts misbehaving.
- **This is intentional**, not a bug — the design assumes "monitor and remember" tasks ("has the deploy stabilized since you last checked?") benefit from continuity. If you want each fire to start fresh, schedule it as a one-shot and have the parent agent re-create after each completion, or break the work into a flow.

## Schedule format

Standard 5-field POSIX cron, evaluated in the **process's local timezone**:

```
minute hour day-of-month month day-of-week
```

| Expression       | Meaning                              |
|------------------|--------------------------------------|
| `*/5 * * * *`    | every 5 minutes                      |
| `0 * * * *`      | top of every hour                    |
| `0 9 * * *`      | daily at 9:00am                      |
| `30 14 * * *`    | daily at 2:30pm                      |
| `0 9 * * 1-5`    | weekdays at 9:00am                   |
| `30 14 15 6 *`   | June 15 at 2:30pm (one-shot)         |

Parsed by [robfig/cron v3](https://pkg.go.dev/github.com/robfig/cron/v3) — the same parser used by Kubernetes CronJob. No timezone offsets; the agent should generate expressions for the user's local time directly.

## Using crons

There are three ways to schedule a cron, ranked from most to least convenient.

### 1. The `/loop` slash command (TUI only)

Quick recurring schedule from chat input:

```
/loop 5m tail the last 100 lines of build.log and report any FAIL
/loop 1h /smoke-tests
/loop 30m check the deploy status and ping me if anything regressed
```

Format: `/loop [interval] <prompt>`. Interval is a Go [`time.Duration`](https://pkg.go.dev/time#ParseDuration) string (`30s`, `5m`, `2h`, `24h`); default is 10 minutes if omitted. The interval is converted to the closest cron expression — `5m` → `*/5 * * * *`, `1h` → `0 * * * *`, `24h` → `0 0 * * *`.

`/loop` jobs:

- Run via the **`explorer`** subagent (read-only). For write-capable scheduled work, use the `croncreate` tool instead.
- **Fire immediately on creation**, then follow the schedule. Lets you confirm the job behaves as expected without waiting for the first scheduled tick.
- Are tagged `source: loop` (visible in the cron jobs page).

If the prompt starts with `/`, it's treated as a skill invocation — the cron prompts the subagent to call the skill tool with the given name and arguments and report its output verbatim.

### 2. The `croncreate` tool (agent)

When asked by the user to schedule something, the agent calls `croncreate` directly:

```json
{
  "schedule": "*/15 * * * *",
  "prompt": "Check the production deploy status; ping me if any service is degraded",
  "subagent_type": "explorer",
  "task_title": "Monitor prod deploy",
  "is_recurring": true
}
```

Parameters:

| Field           | Required | Description                                                                 |
|-----------------|----------|-----------------------------------------------------------------------------|
| `schedule`      | yes      | 5-field cron expression in local time                                       |
| `prompt`        | yes      | What the subagent should do on each fire                                    |
| `subagent_type` | yes      | `explorer` for read-only; `workhorse` for tasks that write files / run cmds |
| `task_title`    | yes      | Short display title (max 80 runes)                                          |
| `is_recurring`  | no       | Default `true`. Set `false` for "do X once at Y" one-shots                  |

Returns a job ID like `cron_a1b2c3d4`. The agent can show it to the user and use it with `crondelete`.

**One-shot example** — agent generated for "remind me at 4:15pm today":
```json
{
  "schedule": "15 16 13 5 *",
  "prompt": "Remind me to merge the release PR",
  "subagent_type": "explorer",
  "task_title": "Reminder: merge release PR",
  "is_recurring": false
}
```

The agent pins month and day-of-month to today's values rather than using `*`, so the cron only matches once.

### 3. Companion tools

- **`crondelete`** — cancel a job by ID. Removes it from the database; the job stops firing immediately.
- **`cronlist`** — list every cron job in the current session, with schedule, status, run count, next-run time, and last result.

All three tools (`croncreate`, `crondelete`, `cronlist`) are baseline agent tools — available by default to coder, workhorse, hivemind, and explorer without special configuration.

## TUI — viewing and managing jobs

Press `/crons` (or navigate via the command palette) to open the cron jobs page. Shows every job in the current session with:

- Job ID, schedule (human + cron), source (`loop` or `agent`), status (`active` / `done` / `paused`)
- Run count, next-run time
- Last output (truncated to 500 chars), any error

Keybindings: `j/k` or arrows to navigate, `d` to delete, `esc` to return.

When the process restarts and a one-shot's fire window passed while you were down, a confirmation dialog surfaces it: **Run Now** / **Discard** / **Keep For Later**. Recurring jobs silently reanchor from current time — they don't backfill missed windows.

## Subagent selection guidance

- **`explorer`** (default for `/loop`): read-only tools — grep, glob, read, ls, bash without writes. Use for monitoring, summarizing, checking status.
- **`workhorse`**: full tool access including write, edit, bash. Use when the scheduled task needs to make changes (commit, push, modify configs).

Pick the minimum-privilege subagent the task actually needs. A scheduled "summarize the test logs" task does not need write access.

## Permissions

When a cron job is about to fire, the scheduler asks for permission keyed on `cron:<job_id>`, with tool name `cron` and action `execute`. You can pre-grant in config:

```json
{
  "permission": {
    "rules": {
      "cron": "allow"
    }
  }
}
```

Or per-job-pattern:

```json
{
  "permission": {
    "rules": {
      "cron": {
        "*": "ask",
        "cron:loop-*": "allow"
      }
    }
  }
}
```

If the auto-approve mode is on for the session (toggle with `/auto-approve` or start with `--auto-approve`), cron permissions are auto-granted.

### Denial behavior

If you deny a cron's permission request, the scheduler advances `next_run_at` to the next scheduled fire (for recurring jobs) or marks the job done (for one-shots). It does **not** re-prompt every tick — that would force you to deny once per second until you killed the job. To stop the job permanently, run `crondelete` or press `d` on the crons page.

## Caveats and edge cases

### Inactive sessions

A cron job lives in a session. If that session isn't the active one in the TUI and the job needs permission (no `allow` rule, no auto-approve, no prior session grant), the scheduler defers the fire by 60 seconds and tries again. Until you focus the session, the job won't run.

Auto-approved jobs and jobs with explicit `cron: allow` rules run regardless of which session is active.

### Session became busy after task ran

The scheduler tries to commit the synthetic `task_call`/`task_result` pair into the parent session atomically — it briefly holds the session-busy slot to prevent the parent agent from inserting a message in between. If a user message arrives during the narrow window between "we ran the task" and "we got the lock", the synthetic write is skipped and the result is preserved only on the cron row (visible via the crons page). The job still advances `next_run_at` correctly; it does not re-fire.

### Errors

If the task tool returns an error, the cron's `error` field captures the message and `next_run_at` is advanced to the next scheduled fire (recurring) or the row is marked done (one-shot). A persistent error on a recurring job does not block the scheduler — it just keeps failing each fire window until you delete the job or fix the underlying problem. If the schedule itself becomes unparseable, the job is paused.

### Missed one-shots after downtime

If opencode is down when a one-shot was supposed to fire, on next startup the scheduler buffers a "missed one-shots" event and the TUI surfaces a dialog. Pick:

- **Run Now**: fires immediately
- **Discard**: marks done without firing
- **Keep For Later**: keeps in queue; dialog re-surfaces on next startup

Missed *recurring* jobs are not surfaced — they silently reanchor from now and drop the missed windows. (A daily 9am job that missed the last 3 days does not fire 3 times to catch up.)

### Per-session cap

A session can hold at most **50 active cron jobs**. Creating a 51st returns an error; cancel one first.

### Schedule validation

`croncreate` rejects schedules that match nothing in the next 366 days (e.g. `0 0 31 2 *` — February 31). This catches typos before the job lands in the database.

### Shutdown wait

The scheduler waits for any in-flight cron fires to finish before allowing shutdown. A long-running scheduled task can delay opencode exit by minutes. Press `Ctrl+C` twice for force-shutdown if needed (kills child processes; in-flight cron work is lost).

### Disabling crons entirely

Set `OPENCODE_DISABLE_CRON=1` in your environment to skip cron initialization. The crons page shows a "disabled" message, `/loop` reports the same, and `croncreate` is not registered for any agent.

### Multiple opencode processes against one database

When you run more than one opencode process against the same database (e.g. `opencode serve` for the chat bridge plus `opencode` TUI in another terminal), only **one** process schedules cron jobs at a time. A cross-process leader lock pins scheduling to whichever process acquires the lock first:

- **SQLite**: an OS file lock at `<dataDir>/cron.lock`. Held by the open file descriptor; released by clean shutdown or process exit.
- **MySQL**: a per-project `GET_LOCK` on a dedicated `*sql.Conn`. Released by `RELEASE_LOCK`, by closing the conn, or by MySQL killing the conn (network blip, server restart, idle timeout).

Followers stay dormant and retry every 5 seconds; on the leader's exit a follower takes over within that window. The leader pings the lock every 30 seconds; if the MySQL conn was killed in the background the leader downgrades to follower (logged as `Cron leader lock lost; downgrading to follower`) and the next follower retry takes over. This prevents a split-brain where the original holder keeps claiming jobs after the server-side `GET_LOCK` has already been released.

This matters most for the chat bridge: a cron scheduled in a bridge-bound session needs the bridge's permission resolver to fire. If a TUI process raced the bridge process and acquired the lock, the TUI's scheduler would either defer the job 60s (no bridge resolver in this process) or pop a permission dialog the chat user cannot see. **Start the process that owns the bridge first** to make sure it wins the lock. Followers log a single `Cron scheduler started as follower` line at boot so the deployment's role is obvious.

## Data layout

Cron jobs are persisted in the `cron_jobs` table (both SQLite and MySQL backends). Schema in [internal/db/migrations/sqlite/20260507120000_add_cron_jobs.sql](../internal/db/migrations/sqlite/20260507120000_add_cron_jobs.sql) and the corresponding MySQL migration. Key columns:

- `id` — job ID (`cron_<hex>`)
- `session_id` — parent session; FK with `ON DELETE CASCADE`, so deleting a session removes its crons
- `schedule`, `prompt`, `subagent_type`, `task_title`, `task_id`
- `is_recurring`, `source` (`loop` or `agent`), `status` (`active`/`done`/`paused`)
- `firing` — atomic claim flag for fire serialization
- `last_run_at`, `next_run_at`, `run_count`
- `last_result` (truncated to 10K chars), `error`

The cron task's child session uses `task_id` to resume across fires — robfig's parser is deterministic, so the same task_id consistently maps to the same subagent conversation lineage.
