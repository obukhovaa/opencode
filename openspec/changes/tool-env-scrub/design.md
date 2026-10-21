# Design

## Context

opencode starts child processes from about a dozen places, and each builds its environment
on its own, almost always as `os.Environ()` plus a few additions: the persistent shell
(`internal/llm/tools/shell`), background bash and `monitor` (`internal/llm/tools`), the TUI
`!` shell (`internal/tui/components/chat/editor.go`), CLI tools (`internal/clitool`, which
already filters by `env.inherit`/`env.pass`), hooks (`internal/hooks`), stdio MCP servers
(`internal/llm/agent/mcp_pool.go`) and LSP servers and installers (`internal/lsp`).
opencode reads its own secrets with `os.Getenv` at several points after startup (the bridge
reads the registrar password lazily), so removing them from opencode's process environment
is not an option.

## Goals / Non-Goals

**Goals:**
- One place that decides what a child inherits, used by every spawn site.
- A deny list that is safe by default (engine secrets) and extensible without code changes,
  both per workspace (`.opencode.json`) and per deployment (one env var set by whatever
  launches opencode, which works across every workspace it serves).
- Close the `/proc/<ppid>/environ` side door on Linux.

**Non-Goals:**
- Hiding files. A deployment that renders a secret into `~/.opencode.json` or any other
  file readable by the agent's user still exposes it to `cat`; that is a file-permission or
  uid-separation concern.
- Hiding secrets a child was given on purpose (an MCP server's own `env`): a same-user
  process can read that child's `/proc/<pid>/environ`. Operators who care give such
  servers a separate uid or an HTTP transport.
- An allow-list mode. Tools legitimately need arbitrary variables (`GOPATH`, proxies, cloud
  credentials for the work itself); a deny list breaks nothing it does not name.

## Decisions

**A small `internal/toolenv` package.** `Filter(env, patterns)` is pure and table-tested;
`Environ()` returns `Filter(os.Environ(), DenyList())`; `DenyList()` unions the built-ins,
`config.Get().ToolEnv.Deny` (nil-safe before config load) and `OPENCODE_TOOL_ENV_DENY`.
Every spawn site swaps `os.Environ()` (or `cmd.Environ()`) for `toolenv.Environ()`.
Alternative: a wrapper around `exec.Command`. Rejected because several sites hand the
environment to a library (`mcp-go`'s command func) or assemble it from a parent slice
(`clitool.BuildEnv`), so an env-level function fits every site with a one-line change.

**Union, no removal of built-ins.** A deny list that config can shrink invites someone to
"fix" a broken hook by un-denying the API password. A hook that genuinely needs it can be
given it explicitly (there is no per-hook env today, which is the trade-off below).

**Glob via `path.Match`.** Env names contain no `/`, so `path.Match` gives shell-style globs
with no new dependency. Entries without metacharacters are compared exactly, and a malformed
pattern falls back to exact compare instead of erroring, so a typo in the deny list never
disables filtering.

**Explicit per-child values win.** The filter applies to what is inherited; the MCP/LSP
`env` lists and CLI `env.set` literals are appended after filtering, because an operator who
writes a value into one server's config has granted it to that server. `${env.NAME}`
expansion in `env.set` reads the filtered parent, so a manifest cannot pull a denied value
out by reference.

**`PR_SET_DUMPABLE=0` on Linux, only when something was denied.** Children run as opencode's
uid, and `/proc/<pid>/environ` of a dumpable same-uid process is readable, which would undo
the filter via `cat /proc/$PPID/environ`. Unsetting the variables in-process does not help:
the kernel serves the original environment block. Making the process non-dumpable is what
`ssh-agent` does. It is skipped when no denied variable was present, so a desktop user with
no engine secrets keeps core dumps and debugger attach. It is a separate commit so it can be
reviewed (or dropped) on its own. `execve` resets the flag, so children are unaffected.

## Risks / Trade-offs

- [A hook or stdio MCP server that called back into opencode's API with
  `OPENCODE_SERVER_PASSWORD` from its inherited env stops authenticating] → MCP servers can
  be given it through their `env` config; hooks have no per-hook env, so such a hook must
  read it from elsewhere. Called out as BREAKING in the proposal and in the docs.
- [Non-dumpable process: no core dumps, `dlv attach`/`gdb -p` as the same user fail,
  `/proc/<pid>/{environ,fd,maps}` become root-owned] → Only applied when secrets are
  present, which is the server deployment case where those are already unwanted; logged
  at INFO once at startup so the cause is discoverable.
- [Persistent shell is created once and reused] → The deny list is read at spawn time;
  config changes take effect for the next shell. Same lifetime as `shell.args` today.

## Migration Plan

No data migration. Deployments that also export secrets under their own names add them to
`OPENCODE_TOOL_ENV_DENY` (or `toolEnv.deny`) when they adopt the release. Rollback is the
previous release.
