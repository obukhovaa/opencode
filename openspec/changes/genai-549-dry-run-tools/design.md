# Design

## Where the mode comes from

The mode comes from the CLI flag `--dry-run-root <root job id>`, and it holds for the whole process. The orchestrator runs dry-run jobs only on per-Job pods, which run one job in one process and are deleted after it, and never on the agent pool. So the mode is set once at startup and never cleared: `dryrun.Enabled()` reads a process-wide value, and every step, subagent (`agent-tool-async.go:88`), background shell and resumed session in the process sees it with no copying per call site.

The flag is also the handshake: an image without it exits on the unknown flag, before any tool call. The root must match `[A-Za-z0-9-]+` (c2-agent job ids are UUIDs), because it becomes part of a session id that travels as a URL path segment; anything else exits with an error.

**Rejected: the job arg `dry_run: true`, read per call.** The first draft did this so a long-lived pool server could serve dry-run and live runs one after the other. Review showed the pool pod is not scratch: it keeps the clone, local commits and edited files between runs, so c2-agent now keeps dry-run jobs off the pool, and a per-process mode is enough.

**Rejected: `flowArgHeaders` for the mode.** Mapped args are treated as sensitive and kept out of persisted flow state (`internal/config/config.go:49-56`), so a resumed run would lose them. They only reach sse and http servers, and our GitLab, Jira and TeamCity servers are stdio.

## Flow state

opencode keys `flow_states` by session prefix, which flows build from the issue id (`session.prefix`, resolved in `resolveSessionPrefix`, `internal/flow/service.go:323`), and all runners share one store. A dry-run run on a real issue would otherwise leave rows, canned outputs included, that the next live run on that issue resumes. In dry-run, `RunWithOptions` puts `dry-run-<root>-` in front of the session prefix, whether it was resolved from the spec or given by the caller. The orchestrator does not recover a killed dry-run job, because the run's fake objects live in this process's memory, so a root's rows are never resumed on another pod. Each eval repetition is a new root, so it starts clean.

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
| MCP pass-through | the server's `dryRun.passthrough` | not handled, runs. Only for tools whose server applies the mode itself: the orchestrator's `createJob`, which creates the child with the parent's dry-run mode |
| bash | parser walk | runs, or handled per command (below) |
| task / subagent | the call itself | runs; the subagent's own calls are intercepted, because they run in the same process |
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

A handled call is recorded in the trace. Its span keeps the tool input, as for any call, and its response metadata carries `dryRun: true`, `intercepted` and `fakeId`, which also reach the PIT-478 relay. Nothing is written to the pod's disk: dry-run runs only on per-Job pods, which are deleted after the run, so a file there would be gone before the eval runner could read it.
