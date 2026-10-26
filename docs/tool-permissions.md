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

## Workspace CLI tools: two boundaries

A workspace CLI tool (`.agents/tools/<name>.yaml`, see [cli-tools.md](cli-tools.md)) is gated like any other tool name — `tools: {snow: true}` or an `allowTools` entry — with one difference: a manifest with `grant: explicit` (the default) is **not** granted by a bare `"*": true`, exactly as the cron tools are not. Name it, or use `grant: implicit` in the manifest.

Keep the two boundaries apart when reasoning about such a tool:

- **What the process can do** is the manifest's hard policy (`args.deny` / `args.allow` on the argument vector, `env`, `cwd`, `timeout`, argv-only exec). No agent configuration loosens it; a violation is a tool error the model sees, and the run continues.
- **What the model may ask for** is the permission chain: `agent.permission.<name>` → `permission.rules.<name>` → the manifest's `permission` default → `agent.permission["*"]` → `rules["*"]` → `ask`, with the space-joined argument string as the pattern input. `deny` here is a tool error the model sees, like every other rule denial (see [When a call is denied](#when-a-call-is-denied)).

## When a call is denied

### Bash compound commands (CD-5483)

Bash permission globs apply to the complete invocation **and** each executable
statement in its parsed Bash syntax. An allow rule such as
`python3 /workspace/scripts/*` cannot authorize an appended `; curl ...`.
The same check covers `&&`, `||`, pipelines (including `|&`), newlines,
background lists, subshells, blocks, conditionals, loops, function bodies,
command substitutions (`$(...)` and backticks), and process substitutions.
Literal operators inside quotes and comments are not executable statements.

Every check finishes before the foreground shell or background task starts.
Any `deny` rejects the whole call, even when the denied branch would not run;
otherwise any `ask` prompts once for the complete original command. All
checks must allow for automatic execution. Existing whole-command denies and
prompts remain authoritative, so exact rules that allowed two commands
separately do not automatically grant a previously denied compound invocation.
The simple-command safe-list exemption is unchanged and is never applied to
individual parts of a compound call.

Malformed or unsupported Bash syntax is refused rather than falling back to
the glob match. The parser does not execute or expand shell code. This is a
command-policy gate, not an operating-system sandbox: an allowed script or
interpreter retains its existing capabilities, variables and persistent-shell
state are not symbolically evaluated, and other configured shell dialects must
use syntax the Bash parser can validate.

### Denial results

A tool call can be refused in two ways, and they end differently:

| Who refused | Example | What happens |
|---|---|---|
| A configured rule evaluated to `deny` | `permission: {bash: {"rm -rf *": "deny"}}`, `permission.rules.edit["*.env"] = "deny"`, a CLI tool manifest's `permission: deny` | The model gets an error tool result naming the tool and the input that was denied (an input over 512 bytes is cut), and the run **continues**. Sibling calls in the same turn keep running. |
| A human rejected an `ask` prompt | pressing deny in the TUI permission dialog, the bridge resolving a prompt as deny | The run **ends** with finish reason `permission_denied`, and the turn's other calls are cancelled, as before. |

A tool that `tools: {…: false}` or an `allowTools` list leaves out is not offered to the model at all, so a call to it gets `Tool not found` rather than a denial.

The rule-denial message tells the model the action itself is forbidden: retrying is pointless, and reaching the same effect through another command, tool or script is not allowed either. It is expected to carry on with the parts of the task that do not need the action, or to report that the rule blocks it.

A circuit breaker stops a model that keeps reaching for denied calls instead of adapting. The run ends with `permission_denied`, exactly as a rejected prompt would, once either bound is reached:

- **3 turns in a row** in which no tool call ran except rule-denied ones, starting with a turn that had a denial. The bound counts turns, not calls: one turn of parallel calls that all hit the same rule, sent before the model had seen any denial, counts once. A call answered without running (loop detected, tool not found, blocked by a PreToolUse hook) is no progress, so a model that repeats the identical denied call — loop-detected from its third try — still trips the bound. A turn in which any call ran and was not denied resets the streak.
- **10 rule-denied calls** in one run (one `agent.Run`: a TUI turn, a flow step attempt, a headless `-p` run), however they interleave with other calls.

A turn that delivers an accepted `struct_output` is never ended by the breaker: the run finishes on it anyway, and ending it would drop the output.

Both bounds are constants (`maxConsecutiveRuleDeniedTurns`, `maxRuleDenialsPerRun` in `internal/llm/agent/permission_denial.go`). Read-category tools (`read`, `view_image`, `grep`, `glob`, `ls`) are the exception: they report both a rule denial and a rejected prompt to the model as a `Permission denied` text result themselves, so their denials never reach the agent loop and do not count towards the breaker.

A PreToolUse hook's `deny` and a `skill` permission denial were already tool errors and are unchanged; neither counts as a denial towards the breaker (a hook-blocked call is no progress either, see above).

**Behaviour change.** Before GENAI-497 any rule denial outside the read-category tools ended the run on the first attempt, which is why workspaces avoided `deny` rules. A flow that relied on a `deny` to stop a step outright now sees the step continue until the model gives up, reports the block, or the breaker trips.


## Read permissions are evaluated on the canonical path

`permission.read` (and the per-tool keys `grep`, `glob`, `ls`, `view_image`, which fall back to `read`, then `*`) applies to all five read-category tools: `read`, `view_image`, `grep`, `glob` and `ls`. Before the check, the tool makes the path absolute against the working directory, cleans it (`//`, `/./`, `/../` collapse) and resolves its symlinks. A path that does not resolve is denied. The patterns are evaluated against both the cleaned and the resolved path and the stricter verdict wins, so a `..` or a symlink inside an allowed directory cannot reach a file the patterns do not allow, and the tool then reads the resolved path it checked. Write allow patterns against canonical paths (for example `/private/tmp/...` rather than `/tmp/...` on macOS).

`grep` and `glob` also drop every match whose resolved path the patterns do not allow (`glob` follows symlinks, and the `grep` regex fallback opens them). An `ask` verdict cannot be put to the user per match, so a match is kept only while it resolves inside the directory that was approved. A pattern `dir/**` does not match `dir` itself: list the directory with `dir` and `dir/**` both allowed.

`read` and `view_image` open the checked path and then confirm the opened file is still that path (a symlink swapped in after the check is refused with `Permission denied`). `ls`, `glob` and `grep` only enumerate, and re-check each match's resolved path afterwards; a swap racing that enumeration is not detected. A path that does not exist now answers `Permission denied` rather than "File not found" when permissions are configured, because it cannot be resolved.
