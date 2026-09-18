# Tool Permissions: `tools` vs `allowTools`

An agent definition gates which tools it may call with exactly one of two keys:

| Key | Model | Unmentioned tool | Shape |
|-----|-------|------------------|-------|
| `tools` | deny-list | **enabled** | map of name/pattern → bool |
| `allowTools` | allow-list | **denied** | array of names/patterns |

They are mutually exclusive. Declaring both in one definition source is an
error (see [Mutual exclusivity](#mutual-exclusivity)).

## Why an allow-list exists

`tools` is allow-by-default: a tool the agent never mentions is granted. That
is convenient for a general-purpose coding agent and wrong for a narrow one,
because every tool added to the harness later is granted to every existing
agent retroactively — nobody edits an agent file to receive it, and nothing
in the definition records that it was never considered.

The pre-`allowTools` workaround was `tools: {"*": false, …explicit grants}`.
It works for the enable check, but the `"*": false` key also told the prompt
builder the agent was tool-less, so those agents silently lost the
parallel-tool-use and background-task prompt sections. That is fixed (an
explicit grant alongside `"*": false` now counts as having tools), but the
workaround remains the wrong tool for the job: it cannot be validated, it
cannot be rendered honestly in the TUI, and its `true`/`false` values suggest
a per-tool decision that `"*": false` has already made.

## Semantics

```yaml
allowTools:
  - struct_output
  - question
  - gitlab_*
```

| Situation | Result |
|-----------|--------|
| exact name listed | allowed |
| name matched by a listed wildcard (`gitlab_*`) | allowed |
| name not listed and matched by nothing | **denied** |
| single `"*"` entry | everything allowed (escape hatch) |
| key absent / empty array | not an allow-list; `tools` applies |

Matching uses the same wildcard syntax as `tools` keys, and the same **case
sensitivity** — an MCP tool whose real name is mixed-case must be listed as it
is spelled. (`deferredTools` deliberately folds case; `allowTools` does not.)

An allow-list applies at every gate, not just at tool construction:

| Gate | Effect in allow-list mode |
|------|---------------------------|
| tool set construction (`NewToolSet`) | unlisted tools are never built |
| `EvaluateToolPermission` | unlisted ⇒ `deny` |
| `EvaluateReadToolPermission` | unlisted ⇒ `deny` |
| `IsToolEnabled` | listed |
| `IsToolExplicitlyEnabled` | listed by name/pattern (not by a bare `"*"`) |
| `HasTools` | true (a non-empty allow-list means the agent has tools) |

Denying at the permission gates as well as at construction is deliberate:
construction only decides what is offered to the model, so a tool that reaches
the permission layer by any other route must still be refused.

Things an allow-list does **not** change:

- **Subagent manager-tool stripping** still applies on top. A subagent
  (`mode: subagent`) that lists `task`, `todowrite`, `croncreate`, … is warned
  and does not get it — manager tools are for primary agents.
- **`permission` rules** still apply to the tools you did allow. Allowing
  `bash` does not pre-approve every command; `permission: {bash: deny}` still
  denies.
- **`deferredTools`** is orthogonal: it decides when a tool's *schema* is
  loaded, not whether the tool is granted.

## Engine-injected tools are not implicit

Three tools are usually added on the agent's behalf rather than called by
name in a prompt. In allow-list mode none of them is implicit:

| Tool | Needed when | If omitted |
|------|-------------|------------|
| `struct_output` | the agent (or its flow step) has an output schema | no structured output; the registry logs a warning at load |
| `toolsearch` | the agent sets `deferredTools` | deferral is ignored wholesale (fail-open); a warning is logged |
| `question` | the agent asks the user something (primary agents only) | the agent cannot ask |

Both warnings name the agent and its allow-list. They are warnings, not boot
failures: either combination can be legitimate mid-migration, and refusing to
start over one is worse than the agent running one tool short.

Default-deny tools keep requiring an explicit name. `croncreate`,
`crondelete` and `cronlist` are opted into by listing them; a bare `"*"`
entry does **not** grant them, matching the deny-list default (an agent with
no `tools` map does not get cron either).

## Mutual exclusivity

Three definition tiers exist, in increasing precedence:

1. built-in agents (Go code — `tools` only)
2. markdown frontmatter (`.agents/types/*.md`, `.opencode/agents/*.md`, …)
3. `.opencode.json` `agents` block

**Within one source**, declaring both keys is an error:

- markdown ⇒ the file fails to parse, the whole definition is discarded, and
  the error (naming the file path) is logged at `Error`. A built-in agent
  reverts to its compiled-in defaults; a non-built-in one disappears.
- `.opencode.json` ⇒ the boot fails. The JSON schema rejects it too, so an
  editor flags it first.

**Across sources**, the two keys are a legal override — otherwise a built-in
that carries a `tools` map in Go code could never be allow-listed. The higher
tier wins and *replaces* the lower one:

- a tier declaring `allowTools` drops the inherited `tools` map, logging a
  warning with the dropped keys;
- a tier declaring `tools` drops the inherited `allowTools`, likewise.

Read that warning. A built-in's `tools` map may hold opt-ins you want to keep
— `hivemind`'s `croncreate: true`, for instance — and they go away with the
map. `allowTools` slices replace whole (they are not merged), with duplicate
entries dropped and warned about.

## Migration

Converting an agent from the `"*": false` workaround:

```yaml
# before
tools:
  "*": false
  read: true
  grep: true
  struct_output: true

# after
allowTools:
  - read
  - grep
  - struct_output
```

Then check, in order:

1. Does the agent have an `output.schema`, or is it used by a flow step that
   injects one? List `struct_output`.
2. Does it set `deferredTools`? List `toolsearch`.
3. Does it use MCP tools? List them by pattern (`gitlab_*`, `jira_*`) and mind
   the case.
4. Does it need `lsp`? Listing it also adds the `# LSP Information` prompt
   section; omitting it removes both.
5. Is it a primary agent that asks the user things? List `question`.

### Version footgun

**Frontmatter YAML parsing is not strict.** A build that predates `allowTools`
ignores the key silently — no error, no warning — so an agent migrated too
early runs with *no* restriction at all rather than a narrow one. Do not adopt
`allowTools` in a workspace until every runtime that loads it is new enough to
understand it. The same applies to `.opencode.json`, where an older build's
schema validation and its `tools`-only loader will not object either.
