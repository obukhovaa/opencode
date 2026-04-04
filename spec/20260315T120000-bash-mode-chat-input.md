# Bash Mode in Chat Input

**Date**: 2026-03-15
**Status**: Implemented
**Author**: AI-assisted

## Overview

Add an interactive bash mode to the TUI chat input. When the user types `!` as the first character, the editor switches to a shell prompt (`$` instead of `>`), executes commands via the persistent shell, and appends output as non-submitting user messages — letting the user run multiple commands before writing an instruction that triggers the assistant. Extend the `` !`cmd` `` shell output markup to work in flow step prompts alongside custom commands, enabling flows to capture real-time shell output during execution.

## Motivation

### Current State

```go
// editor.go — View() renders a fixed ">" prompt
func (e *editorCmp) View() string {
    // ...
    prompt := t.TextMuted().Render("> ")
    // ...
}
```

```go
// editor.go — send() always emits SendMsg, which triggers assistant submission
func (e *editorCmp) send() tea.Cmd {
    text := strings.TrimSpace(e.textarea.Value())
    // ...
    return func() tea.Msg {
        return SendMsg{Text: text, Attachments: attachments}
    }
}
```

Users who want to gather shell output before prompting the assistant must:

1. Leave the TUI, run commands in a terminal, copy output, paste back — context switching.
2. Rely entirely on the agent's bash tool — burning tokens and losing direct control.
3. Use custom command `!` backtick markup — only available in predefined `.md` files, not ad-hoc.

### Desired State

- Typing `!` as the first character in an empty editor activates bash mode.
- The prompt changes from `>` to `$`; text renders in a monospace style.
- Pressing Enter executes the input via the persistent shell and appends stdout/stderr as a user message **without triggering LLM submission**.
- Pressing Escape or Backspace-on-empty returns to normal mode.
- The user can run as many commands as needed, then switch to normal mode and type an instruction that submits everything (accumulated shell outputs + new message) to the assistant.
- Custom command templates and flow step prompts support `!` backtick inline syntax (e.g., `` !`git log --oneline -5` ``) to inject shell output into prompts at execution time.

## Research Findings

### TypeScript (Web) Reference Implementation

Source: `packages/app/src/components/prompt-input.tsx` (branch `7ec398d`)

| Aspect | Implementation |
|---|---|
| Mode state | `store.mode: "normal" \| "shell"` toggled via `!` keypress, Escape, or command palette |
| Activation | `handleKeyDown`: if `event.key === "!"` and cursor at position 0 and mode is `normal` → `setMode("shell")`, `preventDefault()` |
| Deactivation | Escape → `setMode("normal")`; Backspace on empty input → `setMode("normal")` |
| Visual cues | Monospace font class (`font-mono!`), placeholder changes, prompt icon switches to console icon, agent/model selectors hidden via spring animation |
| Submission | `handleSubmit` calls `client.session.shell({ sessionID, agent, model, command: text })` — a separate API endpoint |
| History | Separate `shellHistory` persisted store; up/down arrow navigates shell history independent of prompt history |
| Popover suppression | `@` and `/` popovers disabled in shell mode |
| Radio toggle | Bottom tray shows shell/normal radio group; keyboard shortcuts `mod+shift+x` (shell) / `mod+shift+e` (normal) |

**Key finding**: The web client treats shell mode as a first-class input mode with a dedicated server endpoint (`session.shell`). The server appends the command and its output as user-role messages without invoking the LLM.

### Command Markup Shell Output

From OpenCode docs (`/docs/commands/#shell-output`):

Custom command templates use `` !`command` `` to inline shell output:

```markdown
Here are the current test results:
!`npm test`

Based on these results, suggest improvements.
```

Commands run in the project root; output replaces the `` !`...` `` block before the prompt is sent. This is already documented but needs a Go TUI implementation that mirrors the processing.

### Flow Prompt Template System

Flow step prompts currently support `${args.key}` substitution via `substituteArgs()` in `internal/flow/service.go`. The expansion pipeline is:

1. `validateArgs()` — JSON Schema check
2. `substituteArgs(step.Prompt, args)` — replaces `${args}` (full JSON dump) and `${args.key}` (per-key)
3. Previous step output injection — prepends `"Previous step (X) output:\n..."` if available
4. Structured output merging — step results merged into `args` for subsequent steps

`` !`cmd` `` expansion is **not currently supported** in flow prompts. Adding it would let flow authors capture real-time system state at each step:

```yaml
steps:
  - id: analyze
    prompt: |
      Current test results:
      !`go test ./...`

      Current git state:
      !`git diff --stat`

      Analyze failures and suggest fixes.
  - id: fix
    agent: workhorse
    prompt: |
      Build output after changes:
      !`go build ./...`

      Fix any remaining compilation errors from the analysis: ${args.analysis}
```

This is valuable because flow steps execute sequentially — between steps, the workhorse agent may have changed files, so capturing fresh shell output at each step gives the next agent accurate, up-to-date context rather than stale data from flow start.

### Existing Go Shell Infrastructure

| Component | Location | Relevant API |
|---|---|---|
| Persistent shell | `internal/llm/tools/shell/shell.go` | `GetPersistentShell(workdir).Exec(ctx, command, timeout)` → `(stdout, stderr, exitCode, error)` |
| Shell config | `config.Shell` | `Path` and `Args` fields; defaults to `$SHELL` / `/bin/bash -l` |
| Bash tool | `internal/llm/tools/bash.go` | Uses the same persistent shell; has output truncation and temp-file persistence |
| Chat editor | `internal/tui/components/chat/editor.go` | `editorCmp` with `send()`, `Update()`, `View()` |
| Message types | `internal/tui/components/chat/chat.go` | `SendMsg{Text, Attachments}` |

**Key finding**: The persistent shell already exists and is well-tested. Bash mode can reuse `shell.GetPersistentShell` directly — no new process management needed.

### Go Server-Side Session Shell Endpoint

The TypeScript client calls `client.session.shell()`. The Go server needs an equivalent endpoint (or the TUI can handle shell execution client-side since it runs in-process). Two approaches:

1. **Server endpoint** (like TS): Add `POST /session/{id}/shell` that executes the command, creates user messages for command+output, and returns. Enables web/IDE clients.
2. **TUI-only** (simpler): Execute via `shell.Exec()` directly in the TUI, create user messages via the session service. No new endpoint.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Activation trigger | `!` as first character in empty editor | Matches the TS implementation; intuitive for shell users; doesn't conflict with `!` in normal text |
| Shell execution in TUI | Direct via `shell.GetPersistentShell().Exec()` | TUI runs in-process; avoids adding a server endpoint that only the TUI would use initially |
| Output message creation | Create user messages via session service, not via `SendMsg` | Output must appear in the message list without triggering LLM submission |
| Output truncation | Reuse `bash.go` truncation logic (50KB / 2000 lines) | Consistent with agent bash tool behavior; prevents unbounded output in chat |
| Command echo | Show the command itself as one user message, output as a second | Clearly separates what was run from what it produced; matches TS behavior |
| Prompt indicator | `$ ` instead of `> ` | Standard shell prompt convention |
| Shell history | Separate from normal message history | Shell commands and natural language prompts have different recall needs |
| Popover suppression | Disable `@` file and `/` slash command popovers in shell mode | Shell input should not trigger completion UIs meant for natural language |
| Command markup `!` backtick | Process in custom command and flow prompt template expansion | Runs commands during template rendering before the prompt is sent |
| Shared expansion function | Single `ExpandShellMarkup(ctx, template, cwd)` used by both commands and flows | Avoids duplicating shell markup parsing logic across two callsites |
| Flow expansion ordering | `${args}` substitution first, then `!` backtick expansion | Allows args to parameterize shell commands, e.g., `` !`git log ${args.branch}` `` |

## Architecture

### TUI Input Mode Flow

```
┌─────────────────────────────────────────────┐
│               editorCmp                      │
│  mode: "normal" | "shell"                    │
│  shellHistory: []string                      │
│  shellHistoryIdx: int                        │
├─────────────────────────────────────────────┤
│  Update()                                    │
│   ├── key "!" at pos 0 → mode = "shell"     │
│   ├── Escape in shell  → mode = "normal"    │
│   ├── Backspace empty  → mode = "normal"    │
│   ├── Enter in shell   → executeShell()     │
│   └── ↑/↓ in shell     → navigateHistory()  │
├─────────────────────────────────────────────┤
│  View()                                      │
│   └── mode == "shell" ? "$ " : "> "         │
├─────────────────────────────────────────────┤
│  executeShell()                              │
│   ├── shell.GetPersistentShell(cwd).Exec()  │
│   ├── create user message (command echo)     │
│   ├── create user message (output)           │
│   └── clear editor, stay in shell mode       │
└─────────────────────────────────────────────┘
```

### Message Flow (Shell Mode vs Normal Mode)

```
SHELL MODE (Enter):
──────────────────
  User types: "git status"
  → executeShell("git status")
  → persistent shell returns stdout/stderr
  → append user message: "$ git status"
  → append user message: "<output>"
  → editor cleared, mode stays "shell"
  → NO LLM submission

NORMAL MODE (Enter):
────────────────────
  User types: "fix the failing tests"
  → send() emits SendMsg
  → session creates user message
  → LLM agent invoked with full history
    (includes prior shell output messages)
```

### Command Markup Expansion (Custom Commands)

```
TEMPLATE:                          EXPANDED:
───────────                        ─────────
Review these changes:              Review these changes:
!`git diff --stat`          →      <actual diff stat output>
                                   
Fix any issues.                    Fix any issues.
```

### Command Markup Expansion (Flow Prompts)

```
FLOW STEP PROMPT:                  EXPANDED (at execution time):
─────────────────                  ──────────────────────────────
Current test output:               Current test output:
!`go test ./...`            →      ok   internal/config  0.3s
                                   FAIL internal/flow    0.5s

Previous analysis:                 Previous analysis:
${args.analysis}            →      <value from prior step output>

Fix the failing tests.             Fix the failing tests.
```

### Shared Shell Markup Expansion

```
┌─────────────────────────────────────────────┐
│         ExpandShellMarkup(ctx, tpl, cwd)    │
│  regex: !`([^`]+)`                          │
│  for each match:                            │
│    shell.GetPersistentShell(cwd).Exec(cmd)  │
│    replace match with stdout                │
│    on error: include stderr / error text    │
└─────────────────────────────────────────────┘
          ▲                     ▲
          │                     │
  custom_commands.go      flow/service.go
  (after $ARG subst)      (after ${args} subst)
```

## Implementation Plan

### Phase 1: Editor Bash Mode

- [x] **1.1** Add `mode string` field to `editorCmp` (`"normal"` / `"shell"`) and `shellHistory []string` / `shellHistoryIdx int`
- [x] **1.2** In `Update()`, detect `!` keypress when cursor is at position 0 and input is empty — switch to shell mode, consume the key
- [x] **1.3** In `Update()`, handle Escape in shell mode → switch back to normal; handle Backspace on empty input → switch back to normal
- [x] **1.4** In `View()`, render `$ ` prompt when in shell mode; apply a different text style (if supported by textarea model)
- [x] **1.5** Add new message types `ShellExecMsg`, `ShellResultMsg`, `ShellModeChangedMsg` for internal use
- [x] **1.6** In `Update()`, on Enter in shell mode: capture text, clear editor, return a `tea.Cmd` that executes `shell.GetPersistentShell(cwd).Exec()` and returns a `ShellResultMsg{Command, Stdout, Stderr, ExitCode, Error}`
- [x] **1.7** Handle `ShellResultMsg` in the chat page: create user messages (command echo + output) via message service without triggering LLM
- [x] **1.8** Suppress `/` slash and `@` file completion popovers when in shell mode

### Phase 2: Shell History

- [x] **2.1** Maintain a separate in-memory shell command history ring
- [x] **2.2** Up/Down arrow keys navigate shell history when in shell mode
- [ ] **2.3** Optionally persist shell history across sessions (consider size limits)

### Phase 3: Shell Markup Expansion (Shared)

- [x] **3.1** Create a shared `ExpandShellMarkup(ctx context.Context, template string, cwd string) string` function in `internal/format/shell_markup.go`
- [x] **3.2** Parse `` !`...` `` blocks via regex `` !`([^`]+)` ``; support multiline commands within backticks
- [x] **3.3** Execute each matched command via `shell.GetPersistentShell(cwd).Exec()` with the bash tool's default timeout (2 min)
- [x] **3.4** Replace the `` !`...` `` block with the command's stdout in the expanded template
- [x] **3.5** Handle command failures: on non-zero exit include both stdout and stderr; on timeout or error include an error annotation (e.g., `[command error: ...]`)
- [x] **3.6** Apply output truncation per-command (reuse `bash.go` limits) to prevent a single noisy command from bloating the prompt

### Phase 4: Command Markup in Custom Commands

- [x] **4.1** Call `ExpandShellMarkup()` in the custom command pipeline after `$ARG` / `$N` substitution and before `sendMessage()`
- [x] **4.2** Insert the call in the `CommandRunCustomMsg` handler in `page/chat.go` after argument substitution
- [x] **4.3** Verify that existing `` !`git diff` `` usages in `commands/commit.md` work correctly

### Phase 5: Command Markup in Flow Prompts

- [x] **5.1** Call `ExpandShellMarkup()` in `flow/service.go`'s `runStep()` after `substituteArgs()` and before passing the prompt to the agent
- [x] **5.2** Expansion happens at step execution time (not at flow parse time) so each step captures fresh output
- [x] **5.3** Pass the flow's working directory as `cwd`
- [x] **5.4** Log expanded prompts at debug level for flow troubleshooting
- [x] **5.5** Handle expansion errors: if a shell command fails in a flow prompt, include the error output in the prompt text rather than failing the entire flow step — let the agent decide how to handle it

### Phase 6: Visual Polish

- [x] **6.1** Add keybindings to the help bar for shell mode (`!` to enter, `Esc` to exit)
- [x] **6.2** Style shell output messages differently from normal user messages (e.g., monospace, muted border)
- [x] **6.3** Show exit code badge on non-zero exits

## Edge Cases

### Long-Running Commands

1. User runs `sleep 60` or `npm install` in shell mode
2. The TUI blocks on `Exec()` — needs async execution
3. Should show a spinner/indicator while the command is running and support Ctrl+C cancellation via context

### Large Output

1. User runs `cat largefile.log`
2. Output exceeds 50KB or 2000 lines
3. Apply the same truncation as `bash.go` — head/tail preview with temp file path

### Shell Mode and Attachments

1. User has file attachments when switching to shell mode
2. Attachments should be preserved but not shown in the shell prompt view
3. Restored when switching back to normal mode

### Empty Command

1. User presses Enter in shell mode with no input
2. Should no-op, not execute an empty command

### Flow Shell Markup with Failing Commands

1. A flow step prompt contains `` !`go test ./...` `` and the tests fail (exit code 1)
2. The output (stdout + stderr) should still be inlined — test failures are useful context
3. Only fatal errors (command not found, timeout) should produce an error annotation

### Flow Shell Markup with Args in Commands

1. A flow step prompt contains `` !`git log ${args.branch}` ``
2. `${args.branch}` must be substituted before shell execution
3. If the arg is missing, the placeholder stays literal and the shell command likely fails — the error output is included

### Concurrent Flow Steps with Shell Markup

1. Multiple flow steps execute in parallel, each with `` !`cmd` `` markup
2. Each step gets its own `Exec()` call on the persistent shell
3. The persistent shell's `commandQueue` serializes executions — parallel steps may wait on each other
4. This is acceptable for correctness; if it becomes a bottleneck, consider per-step shell instances

### Session Not Created Yet

1. User types `!` before any session exists
2. A session must be created before shell output messages can be appended
3. Create session on first shell command execution

### Switching Modes Mid-Conversation

1. User runs shell commands, switches to normal mode, types instruction, submits
2. All shell output messages are already in the session history
3. The assistant sees them as user messages and can reference the output

## Open Questions

1. **Should shell execution be async with a spinner, or block the editor?**
   - Blocking is simpler but freezes the UI for slow commands
   - Async requires a running/cancellable state in the editor
   - **Recommendation**: Async with spinner and Ctrl+C support — commands like `go test` can take seconds

2. **Should the output be stored as a single message or two (command echo + output)?**
   - Single: less clutter, but harder to distinguish command from output
   - Two: clearer separation, matches TS behavior
   - **Recommendation**: Two messages — one for `$ command`, one for output — but consider a single message with formatted sections if message volume becomes a UX concern

3. **Should shell mode persist across session switches?**
   - TS implementation resets to normal on submit
   - Keeping shell mode across sessions could be confusing
   - **Recommendation**: Reset to normal mode when the session changes

4. **How should command markup `!` backtick interact with named arguments (`$ARGUMENTS`)?**
   - Arguments should be substituted first, then shell commands executed
   - e.g., `` !`git log --author=$1` `` should substitute `$1` before executing
   - **Recommendation**: Process `$ARGUMENTS` / `$N` substitution before `` !`...` `` expansion

5. **Should there be a server-side shell endpoint for web/IDE clients?**
   - The TS client already has `session.shell()` — the Go server may need parity
   - TUI can work without it (in-process), but other clients need it
   - **Recommendation**: Defer server endpoint to a follow-up; implement TUI-only first

6. **Should flow shell markup have a per-command timeout?**
   - Default 30s is reasonable for most commands but `go test` can take minutes
   - Options: (a) fixed 30s, (b) configurable per-flow, (c) inherit from bash tool's default (2 min)
   - **Recommendation**: Use the bash tool's default timeout (2 min) for consistency; allow override via flow-level config in a follow-up

7. **Should `ExpandShellMarkup` support nested backticks or pipes?**
   - The regex `` !`([^`]+)` `` doesn't support backtick-containing commands
   - Pipes (`|`), `$(...)` subshells, and `&&` chains work fine since they don't contain backticks
   - **Recommendation**: Keep the simple regex; document that shell backticks inside `` !`...` `` should use `$(...)` syntax instead

## Success Criteria

- [x] Typing `!` as first character in empty editor activates shell mode with `$` prompt
- [x] Commands execute via the persistent shell and output appears as user messages
- [x] Output does not trigger LLM submission
- [x] Escape and Backspace-on-empty exit shell mode
- [x] Normal mode Enter submits to the assistant, which can see prior shell outputs in context
- [x] Shell history (up/down) works independently from normal prompt history
- [x] Custom command `` !`cmd` `` markup executes and inlines output
- [x] Flow step prompt `` !`cmd` `` markup executes at step runtime and inlines fresh output
- [x] Flow `${args}` substitution happens before `` !`cmd` `` expansion
- [x] Shell markup errors in flows don't crash the step — error text is inlined
- [x] Long output is truncated consistently with the bash tool
- [x] No regressions in normal editor behavior

## References

- `internal/tui/components/chat/editor.go` — Primary file to modify for input mode switching
- `internal/tui/components/chat/chat.go` — `SendMsg` type and shared message types; needs `ShellExecMsg` / `ShellResultMsg`
- `internal/tui/components/chat/message.go` — May need shell output rendering style
- `internal/llm/tools/shell/shell.go` — `GetPersistentShell().Exec()` for command execution
- `internal/llm/tools/bash.go` — `persistAndTruncate()` for output truncation logic
- `internal/session/session.go` — Session service for creating user messages
- `internal/tui/components/dialog/custom_commands.go` — Custom command template loading; call `ExpandShellMarkup` after `$ARG` substitution
- `internal/flow/service.go` — `runStep()` and `substituteArgs()`; call `ExpandShellMarkup` after args substitution
- `internal/flow/flow.go` — `Step.Prompt` field that will contain `` !`cmd` `` markup
- `internal/tui/components/dialog/commands/commit.md` — Existing `` !`git diff` `` usage that validates the implementation
- `packages/app/src/components/prompt-input.tsx` — TS reference implementation for mode switching
- `packages/app/src/components/prompt-input/submit.ts` — TS reference for `session.shell()` call
