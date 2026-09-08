## Context

See `proposal.md` — Why. The current implementation is spread across four sites that
each resolve a slash invocation slightly differently:

| Site | Trigger | Today |
| --- | --- | --- |
| `chatPage.Update` / `CompletionSelectedMsg` | popup selection | strips `/query` from the editor, then either shows the argument dialog or sends the expanded skill |
| `chatPage.Update` / `CommandRunCustomMsg` | argument-dialog submit, and `loop`/`rename` handlers | substitutes args, expands shell markup, sends |
| `chatPage.resolveInlineSlash` | `chat.SendMsg` (hand-typed `/name args`) | resolves one leading command via `slashcmd.Parse`/`Resolve` |
| `cmd/flow.go resolveSlashPrompt` | `-p` / flow `--prompt` | resolves one leading command |

Two structural facts constrain the approach:

- `editorCmp.send()` owns the FIFO routing decision (enqueue when the queue is
  non-empty or the session is busy, dispatch otherwise). The queue's drain worker
  calls `agent.Run(sessionID, msg.Text)` directly, so anything not expanded before
  `send()` forks reaches the model verbatim — the literal-`/command` bug.
- `chat.SendMsg` is currently both "user pressed Enter" and "here is fully expanded
  content, send it" (the `ActionSkill` branch of `resolveInlineSlash` re-emits it).
  Expanding on receipt of `chat.SendMsg` would therefore expand twice.

## Goals / Non-Goals

**Goals:**

- One expander, one call site. Expansion is a pure function of (text, command
  registry, session id) plus an injected shell-markup hook.
- The four sites above collapse to: *stage text* (popup / argument dialog) and
  *expand text* (submit / non-interactive prompt).
- Failure never eats the user's typing.

**Non-Goals:**

- No config surface. No opt-out flag restoring send-on-select, and therefore no
  `Config` field, no `cmd/schema/main.go` change, no `opencode-schema.json` regen.
- No new argument syntax. `$ARGUMENTS`, `$ARGUMENTS[N]`, `$N`, `$FOO`,
  `${SKILL_DIR}`, `${SESSION_ID}` keep their current meaning; only the *splitting*
  becomes quote-aware.
- No expand/collapse interaction on the transcript's collapsed skill line — it is a
  static summary.
- Nothing in `internal/bridge/**`. Its `/sessions`, `/new`, `/cost` commands are a
  different namespace parsed by `bridge/service/commands.go`.

## Decisions

### D1: A pure expander in `internal/slashcmd`, injected shell hook

New `internal/slashcmd/expand.go`:

```go
type Registry struct {
    Commands []CommandInfo
    Skills   []skill.Info
}

type Kind int // KindAction, KindPrompt, KindUnresolved

type Invocation struct {
    Kind    Kind
    Name    string
    Args    string
    Command *CommandInfo
    Skill   *skill.Info
}

type ExpandOptions struct {
    SessionID   string
    Interactive bool
    ShellExpand func(string) string // nil = no shell markup expansion
}

type Expansion struct {
    Prompt     string       // expanded message text; empty when Action != nil
    Action     *CommandInfo // set iff the message is exactly one action command
    ActionArgs string
    Count      int          // number of expanded invocations
}

func Scan(text string, reg Registry) []Invocation
func Expand(text string, reg Registry, opts ExpandOptions) (Expansion, error)
```

`ShellExpand` is injected rather than imported so `slashcmd` does not pull in
`internal/format` + `internal/config` and so unit tests can expand thousands of
messages without forking a shell. Callers pass
`func(s string) string { return format.ExpandShellMarkup(ctx, s, config.WorkingDirectory()) }`.

`Scan` always resolves as if interactive so action commands classify normally;
`Interactive` is policy, applied by `Expand`, which rejects a `TUIOnly` command when
it is false. `Scan` reuses `Parse` for the name/args split (widened from "first
space" to "first whitespace rune"), so there is one parser rather than two.

The two callers that have no TUI command table — `cmd/flow.go` and the e2e driver —
share `dialog.CommandRegistry()` (builtins + discovered custom commands + skills),
which replaces `flow.go`'s private `buildCLICommands`. It lives in `dialog` because
that is already where `LoadCustomCommands` lives, and `dialog` already imports
`slashcmd` (the reverse would be an import cycle).

*Alternative considered:* keep expansion inside `chatPage` and let `cmd/flow.go`
duplicate it. Rejected — the two already drifted (the CLI path has a bespoke
"replace remaining `$FOO` with empty string" step that the TUI lacks).

### D2: Action vs prompt is derived, not listed

`CommandInfo.IsAction() bool { return ci.TUIOnly && ci.Content == "" }`.

Every action command today is exactly `TUIOnly` with no embedded prompt; every prompt
command has content. Requiring both guards the degenerate case of a custom command
whose markdown body is empty — it stays a prompt command that expands to nothing
rather than becoming an action with no handler.

*Alternative considered:* an explicit `Kind` field on `CommandInfo`. Rejected for now
— it is a second source of truth that a new builtin can forget to set, and the derived
rule is already correct for all 13 builtins plus custom commands. If a future command
needs to be TUI-only *and* carry a prompt, add the field then.

### D3: Line-anchored, fence-aware scanning

An invocation is a line starting with `/` at column 0, outside a ``` fence, whose
first token resolves. Args are the rest of the line.

*Alternatives considered:*

- *Claude Code's rule* (only the first token of the whole message, everything after is
  args) — cannot express two skills in one turn, which is the point of the change.
- *Any `/token` anywhere in the text* — false-positives on paths (`/usr/bin`), on
  prose ("run /commit afterwards"), and on pasted diffs. Rejected.
- *An explicit terminator/sigil* (e.g. `@skill:name`) — new syntax to learn, and
  breaks the "typed `/x` still works" path.

Column 0 + fence-awareness + name-must-resolve + the `\/` escape + the 10-invocation
cap together make an accidental expansion require a line that starts exactly with the
name of a command the user has installed. That residual case is what the escape is
for.

### D4: Expansion runs exactly once, inside `editorCmp.send()`

`chat.NewEditorCmp` gains an injected callback:

```go
type SubmissionExpander func(text string) (slashcmd.Expansion, error)
func NewEditorCmp(app *app.App, expand SubmissionExpander) tea.Model
```

`send()` calls it *before* the enqueue/dispatch fork:

- `err != nil` → return `util.ReportWarn`, leave the textarea and attachments intact.
- `Action != nil` → reset the textarea and emit `chat.RunActionMsg{Command, Args}`.
  The editor cannot run the action itself — the `tea.Cmd` lives on
  `dialog.Command.Handler`, which only the page's command table has — so the page
  maps the `CommandInfo` back to its handler in one place (`chatPage.actionCommand`).
  Attachments are deliberately *not* cleared: there is no message to carry them, so
  they stay staged for the next one.
- otherwise → route `Expansion.Prompt` through the existing queue/dispatch fork
  unchanged.

`chatPage` supplies the callback, so it stays the owner of the command registry and
the active session id. Because `chatPage` is a pointer model (`NewChatPage` returns
`&chatPage{…}`) the closure may capture it — this is *not* the `appModel` value-receiver
hazard in `CLAUDE.md`, which applies to the value-receiver root model only.
`NewChatPage` is restructured to build `p` first, then the editor container from
`p.expandSubmission`, then `p.layout`.

Consequences:

- `chat.SendMsg` becomes unambiguously "already-expanded content, dispatch it".
  `chatPage.resolveInlineSlash` is deleted; the `SendMsg` handler calls
  `p.sendMessage` directly.
- `openEditor()` (ctrl+e) currently emits `chat.SendMsg` itself, which would bypass
  the expander. It changes to emit a new `editorContentMsg{Text}`; the editor sets the
  textarea value and calls `m.send()`, so the external-editor path shares one code
  path with Enter. Because that now *replaces* the input, `openEditor` also seeds the
  temp file with the current draft — otherwise text typed before ctrl+e (staged
  invocations included) would be silently discarded. Previously it opened a blank
  buffer and left the typed text stranded in the textarea after sending.

*Alternative considered:* expand in `chatPage`'s `chat.SendMsg` handler. Rejected —
the queued payload would stay unexpanded (the bug), and the ActionSkill re-emit makes
double expansion possible, which is not harmless: a skill body may itself contain a
line beginning with `/review`.

### D5: The argument dialog stages text instead of executing

`CloseMultiArgumentsDialogMsg{Submit: true}` currently becomes
`dialog.CommandRunCustomMsg`, which `chatPage` substitutes and sends. It splits:

- `CommandID` is `loop` or `rename` → unchanged, `appModel` keeps handling it.
- otherwise → `dialog.StageInvocationMsg{Text: "/" + name + " " + quotedArgs}`, which
  the editor inserts at the cursor (prefixing a newline when the cursor is not at
  column 0). `dialog.CommandRunCustomMsg`'s prompt-carrying role disappears.

Values are joined with `quoteArg` (wrap in `"` and backslash-escape embedded `"` when
the value contains whitespace, a quote, or is empty), which is the inverse of the new
quote-aware splitter — so what the dialog collected is what the expander binds.

Named-placeholder commands round-trip because `ParameterizedCommandHandler` already
derives `argNames` in first-appearance order with dedup, and the expander binds
`argNames[i] → positional[i]` (D6). Positional (`$0`/`$1`) skills already round-trip
via `joinPositionalArgs`, which moves into the expander's quoting helper.

Numeric fields are padded to their own slot (`positionalSlots`): the declared indices
need not start at 0 or be contiguous — a skill may reference only `$2` — and staging
its value in slot 0 would bind it to nothing.

*Alternative considered:* structured chips above the textarea holding the arg map.
Rejected with the user — chips are not editable, do not survive ctrl+e, and the
requested model was "edit the message".

### D6: Argument binding in one place

`skill.SubstituteContent` keeps its substitution order but `splitArgs` becomes
quote-aware (single and double quotes, backslash escape inside double quotes; on a
parse failure fall back to `strings.Fields`, so malformed input degrades instead of
erroring). Named `$FOO` binding by first-appearance order moves out of
`cmd/flow.go`'s ad-hoc "blank the leftovers" step and out of `chatPage`'s
`strings.ReplaceAll` loop into the expander, which applies, in order: named →
`$ARGUMENTS[N]` → `$ARGUMENTS` → `$N` → append `ARGUMENTS:` when nothing matched.

`slashcmd.HasOnlyArgumentsPlaceholder` and the `SubstituteArgs` helper lose their only
callers and are removed.

### D7: Transcript collapse is a display-only transform

`renderUserMessage` gains a pre-pass, in the same style as the existing
`isShellCommandMessage` special-casing: replace each
`<skill_content name="X">…</skill_content>` region with one line
`⚡ skill:X · N lines`. Applied to user messages only, before `renderMessage`, so
markdown rendering, height computation, and focus all keep working off the rendered
string. The stored `message.Message` is untouched, so the model payload, the session
transcript, and `/export` keep the full text.

### D7a: An action command's own arguments are not "other content"

`isBareAction` treats text on the action command's own line as other content — and so
rejects the submission — *unless* the command declares an `ArgumentHint`. That hint is
already the declaration that separates the action commands which take arguments
(`/rename`, `/loop`) from those which do not (`/new`, `/compact`, …), so no new field
is needed.

Without the distinction, `/new and then look at the diff` would clear the session and
silently drop the sentence, which is exactly the class of surprise this change exists
to remove. The same derivation routes the argument dialog's result: `appModel` looks
the command up in its own table and branches on `IsAction()`, rather than keeping a
second list of "the ones that take arguments".

### D8: Cap at 10 invocations per submit

A pasted block of `/`-lines outside a fence would otherwise inline every matching
skill body into one prompt. Ten is above any plausible hand-authored turn and far
below a prompt-size problem. Exceeding it is an error (D4's error path), not a silent
truncation.

## Risks / Trade-offs

- **Behavior change: selecting a command from the popup no longer sends.** `/init`,
  `/commit`, and every custom command now need an extra Enter. → This is the
  requested change; call it out in `docs/skills.md` and the change's summary. Action
  commands (`/new`, `/compact`, …) are unaffected, which covers the muscle-memory
  cases that would be most jarring.
- **Accidental expansion of pasted text.** → Column-0 anchor, fence-awareness,
  name-must-resolve, `\/` escape, 10-invocation cap. Residual: a pasted file whose
  line starts with an installed command name, outside a fence.
- **Shell markup in a queued message runs at submit time, not at drain time.** →
  Intended and specified; it matches the session and working tree the user was looking
  at when they submitted. Worth a note in the docs because a long queue can make the
  captured output stale.
- **Removing `resolveInlineSlash` touches the send path guarded by the queue change
  that is still un-archived** (`openspec/changes/queue-user-messages-while-busy`).
  → The FIFO fork inside `send()` is not modified, only preceded; the queue change's
  own tests must stay green, and its spec's routing scenarios are re-run as-is.
- **`ExpandOptions.ShellExpand` injection means a caller can forget it** and silently
  ship un-expanded ``!`cmd` `` markup. → Both call sites are in this change and each
  gets a test asserting markup expansion; the field is documented as
  "nil disables expansion" rather than defaulting to a no-op silently.

## Migration Plan

Pure in-process behavior change: no schema, no database, no config, no persisted
format. Rollback is a revert. Sessions created before the change are unaffected —
their stored messages already contain expanded text, and D7's collapse applies to
them retroactively as a display improvement.

## Open Questions

- Should the collapsed transcript line be expandable (focus + a key) later? Deferred:
  it changes no spec requirement and no task, and the summary line is a strict
  improvement over the current wall of text either way.
