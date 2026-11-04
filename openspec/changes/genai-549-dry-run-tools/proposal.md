# Dry-run mode: a run's external writes are recorded, not executed

## Why

GENAI-549. The developer flows need a golden eval set that runs against real GitLab, Jira and TeamCity and writes to none of them. The orchestrator marks such a run with the job arg `dry_run: true` (c2-agent change `genai-549-dry-run-jobs`). It cannot stop the writes itself, because they happen in the agent pod: stdio MCP servers, `git push` over the PAT credential helper, `glab`, `curl`. The runtime is the only place that sees every one of them.

There is no dry-run or mock mode in the fork today.

## What changes

- **One interceptor at the tool choke point.** `runToolBounded` (`internal/llm/agent/tool_watchdog.go:107`) has two callers, the parallel and the sequential tool paths (`agent.go:2175`, `agent.go:2413`). Built-in, MCP, CLI, deferred and subagent tool calls all pass through it, after hooks have rewritten the input. In dry-run, it classifies the call before `tool.Run`.
- **Classification is explicit and fails closed.** Each MCP server's config lists its tools under `dryRun.read` and `dryRun.write`, next to `flowArgHeaders`. A read runs. A write is not run: it is recorded and answered with a canned success. A tool on neither list is refused with an error the model sees. Built-in tools that touch only the pod (edit, write, read, glob, grep) run.
- **Bash is classified by command, not by PATH.** The interceptor walks the command with the same parser `EvaluateBashPermission` uses (`internal/permission/bash.go:19`) and matches each command's basename, so `/usr/bin/git push` is caught like `git push`. `git push`, writing `glab` subcommands and `curl` or `wget` with a method other than GET and HEAD, or with a body, are recorded and faked. Everything else runs.
- **Paths outside the choke point get a rule each.** Bridge and flow chat posts are dropped. `` !`cmd` `` prompt markup goes through the bash classifier. Hook commands go through the bash classifier. Webfetch is GET-only.
- **Fakes keep state for one run.** A faked create returns an id from a reserved range. A later read of that id is answered from the recorded write, or fails with a dry-run error. It is never a real 404 that the model would chase.
- **Confirmation.** `opencode --dry-run` on the CLI, and `dryRun: true` echoed in the `POST /flow` response, so the orchestrator can refuse a runtime that would ignore the mode.

## Impact

`internal/llm/agent/tool_watchdog.go`, a new `internal/dryrun` package, `internal/config/config.go` (`MCPServer.DryRun`), `internal/permission/bash.go` (the parser, reused), the shell markup in `internal/llm/prompt` / `shell/markup.go`, hooks, webfetch, `internal/bridge`, `internal/flow/service.go`, the CLI flags and the `POST /flow` handler.

- The guarantee is narrower than "no writes at all". It covers every harness tool, MCP tool, hook and `` !`cmd` ``, and in bash the commands `git`, `glab`, `curl` and `wget`. A script the agent writes and runs with an interpreter (`python -c 'requests.post(…)'`) is not classified. The design names this gap and how the eval set keeps it closed.
- The piano-developer workspace config needs `dryRun.read` / `dryRun.write` on its GitLab, Jira and TeamCity servers before a dry-run job can call them. Until then those tools are unclassified and every call is refused, which is safe.
