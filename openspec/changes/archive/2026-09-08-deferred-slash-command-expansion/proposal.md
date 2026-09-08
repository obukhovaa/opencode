## Why

Selecting a slash command or user-invocable skill in the TUI sends a message
immediately. The user never gets the composed prompt back in the editor, so there
is no way to say *what to do with* the skill, no way to stack a second skill into
the same turn, and no way to review the invocation before it reaches the model.
The only escape hatch — typing `/name args` by hand — resolves just one command at
the head of the input, and when the agent is busy that text is enqueued verbatim
and later delivered to the model as the literal string `/name args`.

## What Changes

- Resolving a slash command no longer sends a message. For **prompt commands**
  (builtin commands with `Content`, `user:`/`project:` custom commands) and
  **user-invocable skills**, the completion popup and the argument dialog write
  `/<name> <args>` back into the editor as ordinary, editable text. The user edits
  the surrounding message, adds more invocations, and submits when ready.
- Submitting expands **every** invocation in the message, not just a leading one.
  A line whose first token resolves to a prompt command or skill is replaced
  in place by that command's expanded content (`<skill_content>` block for skills);
  free-form prose keeps its position relative to the blocks.
- **Action commands** (`/new`, `/compact`, `/vim`, `/rename`, `/loop`, …) keep
  today's fire-immediately behavior — they perform a TUI action and have no prompt
  content, so they are never staged and never composable. Submitting a message
  that mixes an action command with other content is rejected with a warning
  instead of being silently sent to the model.
- Expansion moves **before** the queue boundary, so a slash invocation typed while
  the agent is busy is expanded at submit time and the queued payload is the real
  prompt. This fixes the current literal-text delivery.
- Argument handling becomes round-trippable: positional splitting is
  quote-aware, the argument dialog quotes values that contain spaces when it
  writes them back into the editor, and named `$FOO` placeholders in custom
  commands are bound positionally by first-appearance order (matching how the
  argument dialog already orders its fields).
- User messages render `<skill_content name="x">…</skill_content>` collapsed to a
  single summary line in the chat transcript. Display only — the stored message
  and the model payload keep the full text.
- `opencode -p` / flow `--prompt` share the same expander, so a non-interactive
  prompt can also carry several invocations. Bridge chat commands
  (`/sessions`, `/new` in Slack/Telegram/Mattermost) are a separate namespace and
  are untouched.

## Capabilities

### New Capabilities

- `slash-command-invocation`: How `/`-prefixed input in the TUI and in
  non-interactive prompts is classified, staged, argument-filled, and expanded
  into the message sent to the model — including multi-invocation expansion,
  action-command exclusivity, the queue-boundary ordering guarantee, and the
  collapsed transcript rendering of expanded skill blocks.

### Modified Capabilities

<!-- None. `chat-editor-layout` invariants (prompt-width reservation, no state
     mutation in View) are preserved as-is; `prompt-surface` governs the fixed
     per-request prompt payload, not user message composition; the chat queue's
     spec still lives in the un-archived `queue-user-messages-while-busy` change,
     so the expansion-before-enqueue ordering is stated in the new capability
     instead of as a delta against it. -->

## Impact

Affected code:

- `internal/slashcmd/` — new invocation classifier and multi-invocation expander
  alongside the existing `Parse`/`Resolve`/`BuildPrompt`; quote-aware positional
  splitting; `CommandInfo` gains a way to distinguish action from prompt commands.
- `internal/skill/substitute.go` — quote-aware `splitArgs`, named-placeholder
  binding order.
- `internal/tui/page/chat.go` — `resolveInlineSlash` becomes the submit-time
  expander; `CommandRunCustomMsg` handling for prompt commands and skills becomes
  "insert into editor" instead of "send".
- `internal/tui/components/chat/editor.go` — accepts an insert-text message; the
  expansion runs before the enqueue/dispatch fork in `send()`.
- `internal/tui/components/dialog/arguments.go`, `custom_commands.go` — the
  argument dialog reports values for staging rather than for immediate execution.
- `internal/tui/components/chat/message.go` — collapsed rendering of
  `<skill_content>` blocks in user messages.
- `cmd/flow.go` — `resolveSlashPrompt` delegates to the shared expander.

Not affected: `internal/bridge/**` (its own `/command` namespace), the `skill`
agent tool, agent-side preloaded skills, and the on-disk skill/command formats.
No config or `opencode-schema.json` change.
