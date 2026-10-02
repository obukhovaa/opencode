# Cron and heartbeat

Two ways to make an agent act on a schedule:

| | Cron | Heartbeat |
|---|---|---|
| What runs | A fixed prompt, in a **child subagent session** | The agent itself, in the **bound chat session**, following an agenda file |
| Created by | The agent (`croncreate`), or `/loop` in the TUI | The chat (`/heartbeat …`), or the agent (`heartbeat` tool) |
| Schedule | 5-field cron expression, one-shot or recurring, process-local time | Interval within active hours/days, UTC |
| Output | Committed into the parent session as a synthetic `task` call; posted to bound chats | Posted under `💓 Heartbeat HH:MMZ` only when there is something to report |
| Parent session busy | Waits until it is idle, then runs in its own child session | Waits until it is idle; a human message cancels a running beat |
| Where | Every mode (TUI, `serve`, ACP) unless `OPENCODE_DISABLE_CRON=1` | Daemon mode only (`opencode serve` without `--flow` / `--pool-mode`) |
| Storage | `cron_jobs` | `bridge_heartbeats` |

Use **cron** for a specific job with its own result ("every weekday at 09:07, list failing pipelines"). Use **heartbeat** for periodic check-ins that need the conversation's context and stay silent when nothing changed.

## Cron

### How it works

A cron job is a row with a schedule and a prompt. The scheduler ticks every second, skips sessions with a run in flight, claims due jobs atomically (`firing` flag, `ClaimForFiring`), and runs the prompt through the `task` tool with the chosen subagent. The result is committed into the parent session as a synthetic `task` tool_call/tool_result pair. In a chat-bridge session it is also posted to every bound peer under a `⏲ <title>` header, with the `<task_id>` trailer stripped; failed fires are not posted.

- **Same child session every fire.** All fires of a job reuse one `task_id`, so the subagent sees its previous runs. Context grows with every fire: keep recurring outputs short, and recreate a job that has gone off the rails.
- **Persistent.** Jobs survive restarts. Recurring jobs re-anchor from now (missed windows are dropped). A one-shot missed during downtime stays due and fires on the first tick that passes the permission gate; in the TUI a dialog offers **Run Now** / **Discard** / **Keep For Later** first.
- **Requester.** A job stores the requester of the turn that created it and replays it on every run (see [telemetry](./telemetry.md#requester)).

### Schedule

Standard 5-field cron in the **process's local timezone** (UTC in most containers), parsed by [robfig/cron v3](https://pkg.go.dev/github.com/robfig/cron/v3):

| Expression | Meaning |
|---|---|
| `*/5 * * * *` | every 5 minutes |
| `7 9 * * 1-5` | weekdays at 09:07 |
| `30 14 15 6 *` + `is_recurring: false` | once, June 15 at 14:30 |

`croncreate` rejects schedules with no match in the next 366 days (e.g. `0 0 31 2 *`).

### Creating jobs

**`croncreate` (agent):**

| Field | Required | Description |
|---|---|---|
| `schedule` | yes | 5-field cron expression |
| `prompt` | yes | What the subagent does on each fire |
| `subagent_type` | yes | Any subagent; usually `explorer` (read-only) or `workhorse` (writes, runs commands) |
| `task_title` | yes | Display title, max 80 runes |
| `is_recurring` | no | Default `true`; `false` = one-shot (pin day and month) |

Returns a job ID (`cron_<hex>`). **`cronlist`** lists the session's jobs (schedule, status, runs, next and last run, error); **`crondelete`** cancels one.

**`/loop` (TUI):** `/loop <interval> <prompt>`, e.g. `/loop 30m check the deploy`. The first word is the interval (a Go duration); `DurationToCron` turns it into `*/N` minutes (rounded up, restarting each hour, so `45m` fires at :00 and :45), whole hours, or daily at 00:00 from `24h`. Runs on `explorer`, fires once immediately, `source: loop`. A prompt starting with `/` runs that skill.

**Bridge `/crons`** lists active jobs across the workspace, with ★ on the current session.

**Enabling the tools.** `croncreate`, `crondelete` and `cronlist` are default-deny: an agent gets them only with an explicit `"tools": {"croncreate": true, "crondelete": true, "cronlist": true}`. Only primary agents (`mode: agent`) can hold them; a subagent's entry is ignored with a warning. Built-in `hivemind` has them enabled. `OPENCODE_DISABLE_CRON=1` registers none of them, and `/loop` and the `/crons` page report crons as disabled.

### TUI page

`/crons` lists the session's jobs (ID, schedule, source, status, runs, next run, last output, error). `j`/`k` navigate, `d` deletes, `esc` returns.

### Permissions

A fire asks permission as tool `cron`, action `execute`, path `cron:<job_id>`, straight through the permission service. Config `permission.rules` do **not** apply. A fire is granted by:

- auto-approve for the session (`--auto-approve` or `/auto-approve`);
- an "allow for session" answer in the TUI (covers every cron of that session until restart);
- in a bridge-bound session, `router.permissionMode`: `allow` grants, `deny` denies, empty/`ask` leaves the request unanswered.

A job whose session is neither the TUI's active one nor bridge-bound (and not auto-approved) is deferred 60 s and retried, logging `Cron job deferred: session is not watched…` once. On a headless `serve` this repeats forever unless auto-approve or a bridge binding resolves it. A denied fire advances to the next window (recurring) or marks the job done (one-shot); it is not re-asked every tick.

### Behaviour details

- **Unattended sessions.** In a session no one is watching (not the TUI's selected session, not bridge-bound), a `question` call answers itself with the first option.
- **Parent busy at commit.** If a user message lands between the run and the synthetic write, the write is skipped; the result stays on the job row.
- **Errors** are recorded on the row and the job moves to its next window; an unparseable schedule pauses the job.
- **Limits.** At most 50 active jobs per session; `last_result` keeps 10,000 bytes.
- **Shutdown** cancels in-flight fires and waits for them to stop.
- **Several processes, one database.** Only the lock holder schedules: `<dataDir>/cron.lock` (SQLite) or a per-project `GET_LOCK` (MySQL). Followers log `Cron scheduler started as follower` and retry every 5 s; the leader pings its lock every 30 s and, if the MySQL connection died, logs `Cron leader lock lost; downgrading to follower`. Results reach chat only from the process that ran the job, so start the process that owns the chat bridge first.

Schema: `internal/db/migrations/{sqlite,mysql}/*cron_jobs*.sql`; deleting a session cascades to its jobs.

## Heartbeat

On a schedule the chat bridge wakes the bound session with a heartbeat turn; the agent works through its agenda file, and only what is new reaches the chat.

### Requirements

- **Daemon mode**: `opencode serve` without `--flow` or `--pool-mode`. Flow runners and pool pods have no scheduler.
- **An inbound-active bot.** Scheduled beats and the reminder run only for chats of an adapter this process serves inbound (it holds the bot's connection and identity lock). A bot with `"inbound": "disabled"` (inbound mediated by an orchestrator) and the `external` channel get neither; `/heartbeat on` there says so, and `/heartbeat now` still runs a beat.
- **An agenda file** (default `HEARTBEAT.md` in the working directory). A missing file, or one with only headings, empty list items or checkboxes, rules, code fences and one-line HTML comments, skips the beat without a model call. Keep it on a persistent volume where the working directory is wiped on redeploy.

### Turning it on

It is off per chat until someone chooses. In the chat:

| Command | Effect |
|---|---|
| `/heartbeat` / `status` | State, schedule, model, agenda file, last and next beat |
| `/heartbeat on` / `off` | Start / stop |
| `/heartbeat now` | One beat now, without moving the schedule; queued behind a running turn |
| `/heartbeat every <dur>` | Interval, `10m`–`24h`, default `1h` |
| `/heartbeat hours <HH-HH>` / `all` | Active hours, UTC, end exclusive; `22-06` wraps midnight |
| `/heartbeat days weekdays` / `all` | Skip Saturday and Sunday (UTC) |
| `/heartbeat model <id>` / `default` | Beat on another configured model (no shared prompt cache) |
| `/heartbeat file <path>` / `default` | Agenda file, inside the working directory |

Settings combine: `/heartbeat on every 30m hours 05-21 days weekdays`. They are stored in the database and survive restarts, redeploys and `/reset`.

Anything else after `/heartbeat` goes to the agent ("every half hour on weekdays, 7 to 23 Oslo time, watch my merge requests"); it converts times to UTC, applies them with the `heartbeat` tool and writes what to check into the agenda file. The tool is opt-in: `"tools": {"heartbeat": true}`, primary agents only. Without it only the exact form works.

**Slack:** the client intercepts messages that start with `/`. Type a space first (` /heartbeat on`); the bridge trims it.

### How a beat runs

- **In the bound session**, through the session dispatcher: never overlaps another run, and a reply to the report continues the same conversation.
- **On a UTC grid** starting at the active hours, or at 00:00 UTC without them: `every 7h` beats at 00, 07, 14, 21; `every 2h hours 07:30-21` at 07:30, 09:30, …; `every 24h hours 07-08` is a daily 07:00 beat. The scheduler ticks every 30 s, so a beat can be up to ~30 s late.
- **Busy sessions defer** (agent working, message queued, or an interactive flow step owns the session). Beats missed while down collapse into one; a late beat fires only inside the active hours and days.
- **Messages come first.** A human message cancels a running beat; it posts nothing and is recorded as skipped (`preempted by a message`). A beat cancelled by `/abort` or shutdown is skipped (`cancelled`), not failed.
- **Quiet.** No queued-ack, progress card, tool cards or intermediate text. A reply of `HEARTBEAT_OK` (or up to 300 runes whose first or last line is `HEARTBEAT_OK`) posts nothing; any other reply goes out under `💓 Heartbeat HH:MMZ`; a failed beat posts one line. When beats start being skipped for a reason (e.g. the agenda vanished), the chat gets one `skipped: <why>` notice.
- **One beat per session per slot**, even when several chats are bound; the report reaches every bound chat.
- **Cost.** A beat is a full turn in the main session (prompt cache applies on the session's own model); auto-compaction applies as to any turn.
- **No DST.** Windows are UTC all year; shift the hours when the clocks change.

### Setup reminder

When a daemon starts, a chat that never chose on or off gets a short setup reminder, at most once a week. Only top-level direct messages of an inbound-active bot are reminded: never channels, threads, flow-step or subagent sessions, `external`, or mediated bots. `router.heartbeatReminder: false` turns it off ([bridge config](./bridge.md#top-level-router-fields)).

Schema: `internal/db/migrations/{sqlite,mysql}/*bridge_heartbeats*.sql`, one row per chat binding; spec: `openspec/specs/bridge-heartbeat/spec.md`.
