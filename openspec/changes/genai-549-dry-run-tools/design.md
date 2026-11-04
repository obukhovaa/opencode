# Design

## Where the mode comes from

The mode is the job arg `dry_run: true`, set by the orchestrator. The fork already propagates job args to every place a tool can run under the job: steps, sync and async subagents (`agent-tool-async.go:88`), background bash, resumed sessions (`app.go:470`) and bridge turns (`SetProcessJobArgs`). A new context key would have to be copied at each of those sites by hand. `dryrun.Enabled(ctx)` reads `tools.JobArgsFromContext(ctx)` and falls back to `tools.ProcessJobArgs()`. It is true only for the boolean `true`.

`--dry-run` on the CLI sets the same arg for a CLI run. It is also the per-Job handshake: an image without the flag must fail on it.

**Rejected: `flowArgHeaders` for the mode.** Mapped args are treated as sensitive and kept out of persisted flow state (`internal/config/config.go:49-56`), so a resumed run would lose them. They only reach sse and http servers, and our GitLab, Jira and TeamCity servers are stdio.

## The interceptor

At the top of `runToolBounded`, before the bounded and unbounded branches:

```
if dryrun.Enabled(ctx) {
    if resp, handled := dryrun.Intercept(ctx, tool, call); handled {
        return resp, nil
    }
}
```

`Intercept` classifies the call:

| Class | Source | Result |
|---|---|---|
| local built-in | edit, write, read, glob, grep, ls, todo | not handled, runs |
| MCP read | the server's `dryRun.read` | not handled, runs, unless it names a reserved id (see fakes) |
| MCP write | the server's `dryRun.write` | handled: recorded, canned success |
| bash | parser walk | runs, or handled per command (below) |
| task / subagent | the call itself | runs; the subagent's own calls are intercepted, because they inherit the job args |
| anything else | none | handled: `IsError`, "tool <name> is not classified for dry-run" |

The canned success is `WithResponseMetadata(NewTextResponse(<json>), {"dryRun": true, "intercepted": "<tool>", "fakeId": …})`. The JSON is a per-tool-family template (`create_*` returns an object with `id`, `iid`, `web_url` built from the fake id; `*note*`, `*comment*`, `transition*` return `{"ok": true}`). A write tool with no template gets `{"ok": true, "dryRun": true}`.

**Why name lists and not a pattern catch-all.** A pattern like `gitlab_*` cannot tell `gitlab_get_merge_request` from `gitlab_merge_merge_request`. An explicit list next to the server's definition is reviewed when the server gains a tool, and a tool nobody listed is refused rather than run. A refusal costs an eval run. A pass-through costs a real write.

## Bash

The parser from `EvaluateBashPermission` yields each simple command in the script, including those in pipes, `&&` chains, subshells and `$(…)`. For each one, by basename:

- `git`: `push`, `send-email` and `request-pull` are faked. All else runs, commits included.
- `glab`: `mr create|update|merge|note|approve|close|reopen`, `issue create|note|close`, `ci run|retry|cancel`, `release create`, and `api` with any `-X`/`--method` other than GET or with `-f`/`-F`/`--input` are faked. All else runs.
- `curl`, `wget`: faked when the method is not GET or HEAD, or a body flag is present (`-d`, `--data*`, `-F`, `--form`, `-T`, `--upload-file`, `--post-*`). All else runs.

When any command in a script is faked, the whole script is not run: running half of a script is worse than running none. The response says which command was faked, and the recorded write holds the full script.

PATH shims for the same three binaries stay as a second layer, for scripts the agent writes to a file and runs. They are not the boundary.

**The gap.** An interpreter running code that opens a socket itself (`python`, `node`, `go run`) is not classified. Blocking interpreters would block every test and build. The eval set is chosen from tasks whose flows write only through MCP and `git`/`glab`, and the PIT-478 tool I/O relay lets the scorer flag a run whose bash output shows an HTTP write. This is a known, written-down limit, not a guarantee.

## Paths outside `runToolBounded`

| Path | Rule |
|---|---|
| bridge and flow chat posts (`internal/bridge`, `internal/flow/service.go`) | dropped in dry-run, logged with the text they would have posted |
| `` !`cmd` `` prompt markup | each command goes through the bash classifier; a faked one renders as `[dry-run: <command> not run]` |
| hook commands | the bash classifier applies before the hook runs |
| webfetch | GET only; another method is refused |

## Fakes with state

Fake ids come from a reserved range, `9_000_000_000 + n`, per run. The run keeps an in-memory store, keyed by fake id, of the recorded write and the object its template returned.

- A read whose input contains a reserved id (any numeric or string field that parses into the range) is answered from the store. The answer is the object the faked create returned, with fields the template did not set left out.
- A read that names a reserved id with nothing in the store fails with "dry-run object <id> was never created in this run".
- A write that names a reserved id is faked like any write.

This makes create, then comment, then read-back work without a real object. It does not simulate server-side effects, such as a pipeline started by a push. A flow that waits on one sees the read fail with the dry-run error, and the run is scored on that.

## Recording

Every handled call is appended to `$AGENT_HOME/dry-run/writes.jsonl` as `{ts, session, tool, input, fakeId}`, and carries `dryRun: true` in its tool response metadata, which reaches the trace and the PIT-478 relay. The eval runner reads whichever of the two it needs; that choice belongs to the runner task.

## Pool reuse

A pool pod serves dry-run and live runs one after the other. Nothing dry-run is held per process. The mode is read per call from the run's job args. The store and the JSONL are keyed by job id. The persistent shell is respawned at run start (as today), so no shim `PATH` leaks from one run to the next.
