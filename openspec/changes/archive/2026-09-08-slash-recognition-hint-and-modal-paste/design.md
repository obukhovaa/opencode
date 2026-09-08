## Context

See `proposal.md` — Why. Two constraints shape the approach.

**Overlay routing.** `appModel.Update` dispatches to overlays in two places: the
argument dialog is handled inside `case tea.KeyPressMsg`, and every other overlay
in a block of `if a.showXDialog { … }` guards that each end with
`if _, ok := msg.(tea.KeyPressMsg); ok { return }`. Paste matches neither, so it
falls through to the page.

**Editor height.** `chat-editor-layout` requires the textarea height to be a
function of state computed in Update, never mutated in View, and currently ties
the one reserved row to attachment presence.

## Goals / Non-Goals

**Goals:**

- A focused overlay owns user input, by message class rather than by an
  enumeration of dialogs.
- Recognition feedback that cannot disagree with what submit does.
- No new reserved rows and no new width overflow.

**Non-Goals:**

- Per-character highlighting inside the draft (not expressible with the current
  textarea — see proposal).
- Surfacing *rejection* reasons (action-command composition, invocation cap) in
  the hint. Those need the expander's policy, not a scan, and already surface as a
  warning on submit.
- Fixing the pre-existing case where the attachment badge itself exceeds a very
  narrow container.

## Decisions

### D1: Classify input by message type, not by dialog

`consumesInput(msg)` returns true for `tea.KeyPressMsg`, `tea.PasteMsg`,
`tea.PasteStartMsg` and `tea.PasteEndMsg`; the 13 overlay guards call it instead
of type-asserting `tea.KeyPressMsg`. The argument dialog, routed separately, gets
its own `case tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg:` branch that
forwards only while the dialog is open, so ordinary pasting into the editor is
untouched.

Both `textinput` and `textarea` already handle `tea.PasteMsg`, so routing is all
that was missing.

*Alternative considered:* handle paste only in the argument dialog. Rejected — the
question dialog, filepicker and session filter have the same defect, and the
predicate fixes them in one edit.

### D2: The hint is cached state, refreshed from one site

The hint's presence changes the reserved row, and `chat-editor-layout` forbids
computing height in View. So recognition is cached on the model and recomputed in
Update. Rather than adding a call to each of the ~8 branches that can mutate the
textarea, `Update` is a thin wrapper over the previous body (`update`) that calls
`refreshRecognition()` afterwards; every branch already returns `m` itself, so the
wrapper acts on the same instance.

`refreshRecognition` is a no-op unless the draft actually changed, and short-circuits
before touching the registry unless some line begins with `/` — which is the
necessary condition for an invocation, so ordinary typing never builds a registry.

### D3: Scanning is a second, cheaper injected hook

`InvocationScanner func(string) []slashcmd.Invocation` sits alongside
`SubmissionExpander`. It is deliberately separate: it runs on every keystroke and
must not substitute arguments or run `` !`cmd` `` shell markup. Both resolve
against `chatPage.slashRegistry()`, so the hint cannot promise something the
submit path would not deliver.

*Alternative considered:* reuse `SubmissionExpander` for the hint. Rejected —
expansion runs shell commands.

### D4: Attachments and the hint share one row

`hasAffordanceRow()` replaces the attachment-count check in
`syncTextareaHeight()`, so the reservation stays 0 or 1 no matter how many
affordances are active — the editor is a short bottom panel and a second reserved
row would cost a quarter of it.

Chips are dropped, not wrapped, once they exceed the width budget (the row is one
line, and the no-overflow contract binds it), with a muted `+N` marker accounting
for what was dropped so the hint never under-reports. The joined row is padded to
the container width with the theme background, because `JoinVertical` pads shorter
lines with unstyled cells that render as a black gap.

## Risks / Trade-offs

- **Blocking paste from the page while any overlay is open.** → Correct for text
  entry, and the non-text overlays (quit, permission, session list) ignore paste
  anyway, so nothing that previously worked stops working.
- **A scan per keystroke.** → Bounded by the `/`-at-column-0 pre-check and the
  draft-changed guard; a keystroke already re-wraps and re-renders the textarea.
- **The hint reports recognition, not acceptance.** A draft can show chips and
  still be rejected on submit (action command mixed with content, over the cap).
  → Those cases warn on submit and preserve the text; conflating the two would
  require running the expander on every keystroke.
