## Why

Two gaps in the slash-command experience shipped by `deferred-slash-command-expansion`:

1. **A modal does not own pasted text.** With the argument dialog open, pasting
   inserts into the chat editor *behind* the dialog. `tea.PasteMsg` is not a key
   press, and the TUI's overlay routing deliberately passes every non-key message
   down to the page ("Only block key messages send all other messages down"), so
   bracketed paste lands where the user cannot see it. Pre-existing, and it affects
   every text-entry overlay, not only the argument dialog.
2. **Recognition is invisible until submit.** Whether a `/…` line will actually be
   expanded depends on rules the user cannot see: the token must resolve, sit at
   column 0, and be outside a code fence. Typing `/reviewer` instead of
   `/skill:reviewer` looks identical and silently sends plain text.

## What Changes

- Overlays consume input messages, not just key presses. A new `consumesInput`
  predicate covers `tea.KeyPressMsg`, `tea.PasteMsg`, `tea.PasteStartMsg` and
  `tea.PasteEndMsg`, and replaces the `tea.KeyPressMsg`-only guard at all 13
  overlay dispatch sites. The argument dialog, which is routed separately from
  that block, gets an explicit paste branch.
- The editor draws a **recognition hint**: one chip per invocation the current
  draft will actually expand (`⚡ /skill:reviewer`, in the success colour) or run
  (an action command, in the info colour). An unrecognised `/token` produces no
  chip — the absence of a chip is the signal.
- Attachments and the hint share the **single** row above the input, so the
  reserved height stays 0 or 1. Chips that do not fit the container width are
  dropped and counted (`+2`) rather than wrapped, and the row is padded to the
  container width so it cannot leave an unstyled background gap.

Per-character colouring inside the draft itself was investigated and rejected: the
`bubbles` textarea renders each line with a single style and exposes no
highlight hook, and its only per-line hook (`SetPromptFunc`) is indexed by
*display* line, so it mis-marks every soft-wrapped line.

## Capabilities

### New Capabilities
<!-- None. -->

### Modified Capabilities

- `slash-command-invocation`: adds the recognition-hint requirement, and adds a
  paste scenario to the argument-dialog requirement.
- `chat-editor-layout`: the reserved row above the textarea is driven by the
  affordance row (attachments **or** the recognition hint), not by attachments
  alone.

## Impact

- `internal/tui/tui.go` — `consumesInput`, the paste branch for the argument
  dialog, and the 13 overlay guards.
- `internal/tui/components/chat/editor.go` — `InvocationScanner`, the cached
  recognition state, `hasAffordanceRow` / `affordanceRow` / `recognitionContent` /
  `padRow`, and an `Update` wrapper that refreshes the hint from one site.
- `internal/tui/page/chat.go` — `scanInvocations` and the extracted
  `slashRegistry` shared with `expandSubmission`.
- `internal/tui/styles/icons.go` — `SkillIcon`.

No config, schema, or persisted-format change.
