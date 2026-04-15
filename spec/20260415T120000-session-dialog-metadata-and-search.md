# Session Dialog Metadata and Search

**Date**: 2026-04-15
**Status**: Draft
**Author**: AI-assisted

## Overview

Enrich the session selector dialog (Ctrl+S) and the prune session dialog (Ctrl+P) with
(a) a per-session metadata row showing last-updated time and token usage, and
(b) a search bar at the top of the dialog that filters sessions by title
substring in real time.

Both dialogs already share the same component (`internal/tui/components/dialog/session.go`)
so the changes apply to both with a single implementation.

## Motivation

### Current State

- The session dialog renders each session as a **single line** containing only
  `Session.Title` (`internal/tui/components/dialog/session.go:166`).
- Only 10 sessions are visible at once. The only way to reach older sessions is
  continuous j/k scrolling — there is no search.
- The data needed to inform session choice (last updated, tokens used, cost) is
  already stored on the `Session` row but is not surfaced anywhere in the dialog.

### Desired State

- Each session item renders as **two lines**: a title line and a muted metadata
  line showing the relative last-updated time and token usage. The metadata row
  is designed to be extended later with additional sections (e.g., associated
  cron jobs) without another round of layout work.
- A search bar at the top of the dialog filters the visible session list by a
  case-insensitive substring of `Session.Title`, updating on every keystroke.
- Keyboard-only flow is preserved: when the dialog opens, focus is on the search
  input and arrow/jk still navigates the list. Enter selects the highlighted
  session. Escape closes the dialog. Ctrl+U clears the query.
- Both Ctrl+S (switch) and Ctrl+P (prune) benefit from these changes — no
  behavior divergence between the two, only the dialog title differs as today.

## Research Findings

### Existing Dialog Component

Source: `internal/tui/components/dialog/session.go` (lines 1-243)

| Concern | Current behavior |
|---|---|
| Struct | `sessionDialogCmp { sessions []session.Session; selectedIdx int; width, height int; selectedSessionID string; title string }` (lines 31-38) |
| Navigation | `up/k`, `down/j`, `enter`, `esc` (lines 40-74) |
| Render per item | `itemStyle.Padding(0, 1).Render(sess.Title)` (line 166) — title only |
| Visible window | Max 10 items, centered scroll when more exist (lines 138-165) |
| Invocation | `sessionDialog` (Ctrl+S) and `deleteSessionDialog` (Ctrl+P) are two instances of the same component; only the title differs (`SetTitle("Prune Session")` in `tui.go:657`) |

### Session Data Model

`internal/session/session.go:14-27` and the DB model in `internal/db/models.go:46-59`:

```go
type Session struct {
    ID               string
    ProjectID        string
    ParentSessionID  string
    RootSessionID    string
    Title            string
    MessageCount     int64
    PromptTokens     int64    // tracked separately
    CompletionTokens int64    // tracked separately
    SummaryMessageID string
    Cost             float64
    CreatedAt        int64    // Unix seconds (SQLite strftime('%s', 'now'))
    UpdatedAt        int64    // Unix seconds, auto-updated by trigger
}
```

The `update_sessions_updated_at` trigger bumps `updated_at` on every row update,
so this column reflects "last activity" for all practical purposes.

### Session Listing

`session.Service.List(ctx)` returns only root sessions (`parent_session_id IS NULL`)
for the current project, ordered `created_at DESC`
(`internal/db/sessions.sql.go:164-169`). The dialog already receives this list
via `SetSessions()` — no additional service calls are required for the MVP of
this feature.

### Reusable Search Pattern

The completion dialog (`internal/tui/components/dialog/complete.go:94-101` and
the update loop at lines 154-180) already demonstrates the integration pattern
for a live-filtering text input backed by a list. The session dialog can follow
the same approach but filter in-memory.

The shared `internal/tui/components/util/simple-list.go` component does not
currently support multi-line items. The session dialog already renders its list
by hand (it does not go through `simple-list`), so we can evolve its rendering
without disturbing other dialogs.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Filter source | In-memory over the already-loaded `[]session.Session` | `List()` already returns all root sessions for the project; project size is modest and this keeps the change localized. |
| Filter algorithm | Case-insensitive substring match on `Title` | Matches user mental model — no wildcards, no fuzzy library dependency. Can evolve later. |
| Search field focus model | Search input is always focused when dialog is open; j/k/up/down/enter/esc bypass the text input | Keyboard-only flow. Users never need to tab between panes. Arrow keys and j/k still move the selection even while the input is focused — we route those keys to the list handler before letting the textinput see them. |
| Query clear shortcut | `Ctrl+U` clears the query | Standard readline convention; does not conflict with any current session-dialog binding. |
| j/k vs typing | j/k navigate the list instead of inserting `j`/`k` into the query **only** when the query is empty; once the user types anything, j/k insert literally. | Avoids the common "I tried to search for 'jira'" dead-end. Mirrors how git branch pickers behave. Arrow keys always navigate regardless of query state. |
| Metadata row format | `updated 2h ago • 4.3k / 1.2k tokens` (muted style) | Two compact fields keeps items to two lines. The `4.3k / 1.2k` layout makes prompt and completion visible without a legend — widely recognized in the project already (status bar uses similar shorthand). |
| Metadata row styling | Rendered with `theme.CurrentTheme().TextMuted` and no bold | Title stays visually dominant; metadata reads as supporting context. |
| Future cron section | Metadata row is built by concatenating named sections with ` • ` | Adding a cron-count section later is a one-line append; no re-layout needed. Out of scope for this spec. |
| Visible window size | Reduce from 10 items to 7 | Each item now renders as 2 lines; 7 items keeps the dialog under the 10-line single-line footprint it had before and leaves room for the search bar. |
| Selection on filter change | When the filtered list changes, reset selection to index 0 of the filtered view | Matches every common incremental-search UX; prevents "selected item scrolled offscreen" bugs. |
| Empty-filter result | Show a single muted line "no sessions match" and disable Enter | Gives explicit feedback; Enter cannot select a non-existent session. |
| Relative time formatting | `just now` (<60s), `Ns`, `Nm`, `Nh`, `Nd`, `Nw`, then absolute `YYYY-MM-DD` beyond 4 weeks | Keeps width bounded and readable. No external library needed. |
| Token formatting | Human-readable: `<1000` as-is, otherwise `N.Nk` with one decimal, `N.NM` above 1M | Matches the look-and-feel of the existing status bar context/cost widget. |
| Scope | Applies to both session and prune dialogs simultaneously (single component) | They are one component; splitting them would be churn. |
| Selection stability on re-filter | The highlighted session ID is remembered; if it is still present in the filtered list, keep it highlighted; otherwise fall back to index 0 | Avoids the jarring "I typed one more letter and lost my place" feeling when the match is still present. |

## Architecture

### Component Changes

```
internal/tui/components/dialog/session.go
├── sessionDialogCmp
│   ├── sessions          []session.Session   // full list from SetSessions
│   ├── filtered          []session.Session   // filtered view (nil → use sessions)
│   ├── selectedSessionID string               // preserved across filter changes
│   ├── query             textinput.Model      // NEW: search input
│   └── ... existing fields
└── methods
    ├── SetSessions(sessions) — resets query state
    ├── filter() — recomputes `filtered` based on `query.Value()`
    ├── rememberSelection() / restoreSelection() — map between ID and selectedIdx
    ├── View() — renders search bar + filtered list with 2-line items
    └── Update() — routes keys between textinput and list
```

### Key Routing

```
sessionDialogCmp.Update(msg)
  │
  ├── tea.KeyPressMsg
  │     ├── esc → CloseSessionDialogMsg
  │     ├── enter → SessionSelectedMsg (if filtered list non-empty)
  │     ├── up / down / ctrl+u → list navigation / clear query
  │     ├── j / k when query is empty → list navigation
  │     └── otherwise → textinput.Update(msg) → re-filter if value changed
  │
  └── (other messages are unchanged)
```

### Rendering Layout

```
┌────────────────────────────────────────────────────────┐
│  Switch Session                                        │
│  ──────────────────────────────────────────────────── │
│  > █                                                   │  ← search input (focused)
│  ──────────────────────────────────────────────────── │
│  Refactor session dialog                               │  ← title (primary if selected)
│    updated 2h ago • 4.3k / 1.2k tokens                 │  ← metadata (muted)
│                                                        │
│  Add cron scheduling                                   │
│    updated 1d ago • 812 / 204 tokens                   │
│  ...                                                   │
└────────────────────────────────────────────────────────┘
```

Width is computed as `max(longestItemWidth, searchBarMinWidth, titleWidth) + padding`.

### New / Changed Public API

- `session.Service` — **no changes required**. Existing `List()` is sufficient.
- No new tea.Msg types are required beyond what exists (`SessionSelectedMsg`,
  `CloseSessionDialogMsg`). Filtering is an internal concern of the dialog.

## Implementation Plan

### Phase 1: Metadata Row

- [ ] **1.1** Add helpers in a new file `internal/tui/components/dialog/session_format.go`:
  - `formatRelativeTime(updatedAtUnix int64, now time.Time) string` — returns `just now`, `Ns`, `Nm`, `Nh`, `Nd`, `Nw`, or `YYYY-MM-DD`.
  - `formatTokenCount(n int64) string` — returns `"812"`, `"4.3k"`, `"1.2M"`.
  - `formatSessionMetadata(s session.Session, now time.Time) string` — composes the full metadata row, joining named sections with ` • `.
- [ ] **1.2** In `sessionDialogCmp.View()`, replace the single-line `sessionItems = append(..., sess.Title)` with a two-line render:
  - Line 1: title, styled as today (primary+bold when selected, plain otherwise).
  - Line 2: metadata row, styled with `theme.CurrentTheme().TextMuted`, indented by 2 spaces under the title.
  - Join the two lines with `\n` before appending to `sessionItems`.
- [ ] **1.3** Reduce the visible-window constant from 10 to 7 and verify scroll math still centers the selection correctly.
- [ ] **1.4** Update `width` computation to account for the longer metadata row where it might exceed the title width.

### Phase 2: Search Bar

- [ ] **2.1** Add `query textinput.Model` to `sessionDialogCmp`; initialize with `charm.land/bubbles/v2/textinput` in the constructor (matching the import path used in `arguments.go`). Prompt: `> `. Placeholder: `search sessions`.
- [ ] **2.2** Render the search bar at the top of the dialog (above the first session item, below the title). Use the same width calculation as the list.
- [ ] **2.3** In `Update()`, route keys:
  - `esc` → close (unchanged).
  - `enter` → select, but only if the filtered list is non-empty.
  - `up`, `down`, `ctrl+u` → handle at the component level (never reach textinput). `ctrl+u` clears the query.
  - `j`, `k` → navigate **only** when `query.Value() == ""`; otherwise pass to textinput.
  - Any other key → `query, cmd = query.Update(msg)`; if the value changed, call `filter()`.
- [ ] **2.4** Implement `filter()`:
  - If query is empty, set `filtered = nil` (sentinel meaning "use `sessions`").
  - Otherwise lowercase the query and walk `sessions`, keeping any whose lowercased title contains it.
  - After filtering, call `restoreSelection()` to keep the same `selectedSessionID` highlighted when possible, otherwise reset to index 0.
- [ ] **2.5** Introduce `visibleSessions()` helper that returns `filtered` when non-nil and `sessions` otherwise; update all call sites (render, selection, enter handling) to use it.
- [ ] **2.6** When `visibleSessions()` is empty, render a single muted `no sessions match` line instead of the list and make Enter a no-op.
- [ ] **2.7** `SetSessions()` resets `query` to empty and clears `filtered`. `SetTitle()` is unchanged.

### Phase 3: Selection Stability

- [ ] **3.1** Add `rememberSelection()` that records the current `selectedSessionID` from `visibleSessions()[selectedIdx]` before filtering.
- [ ] **3.2** Add `restoreSelection()` that, after filtering, scans `filtered` for the remembered ID; if found, sets `selectedIdx` accordingly; otherwise sets `selectedIdx = 0`.
- [ ] **3.3** Ensure `selectedSessionID` is always updated whenever `selectedIdx` or the visible list changes (navigation keys, filter changes).

### Phase 4: Testing

- [ ] **4.1** Unit tests for `formatRelativeTime` covering boundaries (59s, 60s, 59m, 60m, 23h, 24h, 6d, 7d, 27d, 28d).
- [ ] **4.2** Unit tests for `formatTokenCount` covering 0, 999, 1000, 1500, 999_999, 1_000_000, 1_500_000.
- [ ] **4.3** Unit tests for `filter()` covering case-insensitive match, no-match-empty-state, and selection stability across filter changes.
- [ ] **4.4** Snapshot-style view test (if the project already has one for dialogs — otherwise skip) that verifies the rendered output contains the metadata line.

### Phase 5: Documentation

- [ ] **5.1** Update any keybinding help text in `tui.go` if it references "switch session" — now it should also imply "search sessions".
- [ ] **5.2** Mention the search bar in the dialog title or in a short help hint at the bottom of the dialog (e.g., `↑↓ navigate  ⏎ select  ^U clear  esc close`).

## Edge Cases

### Very Long Titles

A title longer than the dialog width should be truncated with an ellipsis on the title line only. The metadata line is short by construction. Truncation uses `lipgloss.Width` and a 1-char `…` suffix. Dialog width never exceeds the terminal width minus padding.

### Zero-Token Sessions

Freshly created sessions can have `PromptTokens == 0 && CompletionTokens == 0`. The metadata row still renders but drops the token section entirely, producing e.g. `updated just now`. The ` • ` separator is added only between non-empty sections.

### Clock Skew / Future Timestamps

If `UpdatedAt` is somehow ahead of `now` (clock skew, malformed data), `formatRelativeTime` clamps the delta to zero and returns `just now`. Negative durations never appear.

### Filter Collapses List to Zero, Then Reopens

When the user types a query that matches nothing and then deletes characters until the match returns, `restoreSelection()` must still find the previously remembered `selectedSessionID`. The ID is preserved across all filter transitions — it is cleared only on dialog close (`SetSessions()` reset) or Enter.

### j/k in Query Mid-Word

If the query is currently `fo` and the user types `j`, they clearly intend to continue typing (`foj`). Only navigate with j/k when the query is literally empty. Arrow keys navigate unconditionally.

### Ctrl+P vs Ctrl+P Inside Textinput

The dialog-opening keybinding (Ctrl+P) is handled in `tui.go` **before** the event reaches the dialog, so there is no conflict when the user presses Ctrl+P while the textinput is focused inside a different dialog. Within this dialog, Ctrl+P is not bound and will simply pass through to textinput (which has no binding for it either).

### Resizing While Open

On `tea.WindowSizeMsg`, recompute both the dialog width and the visible-window math. Trim the visible window if the terminal is too short to fit 7 two-line items plus the search bar; in that case fall back to however many fit, minimum 1 item.

### Empty Session List

If `sessions` is empty (new project, never created a session), render a single muted line `no sessions yet`. Search bar is hidden in this case since it has nothing to filter.

## Open Questions

1. **Should the search query be persisted across dialog open/close?**
   - **Proposal**: No. The dialog is short-lived and users typically want a clean slate. Revisit if usage data suggests otherwise.

2. **Should we also add fuzzy matching (e.g., `ab` matches `auto-build`)?**
   - **Proposal**: Not in MVP. Substring match covers the common case and is explainable. A follow-up spec can add fuzzy ranking once we see real data.

3. **Should the metadata row show `cost` too?**
   - **Proposal**: Not in MVP. The spec is about last-updated + tokens; cost would add a third section and push line width. The architecture allows adding it later as a named section.

4. **Should we show message count?**
   - **Proposal**: Not in MVP. Token counts already convey activity scale; adding message count would crowd the line.

5. **Should associated cron jobs be grouped or numeric?**
   - **Out of scope**, but the metadata-sections architecture is designed so that a future `N cron(s)` section can be appended without layout changes.

## Success Criteria

- [ ] Opening the session selector (Ctrl+S) shows each session with a title and a muted metadata row containing relative updated time and token counts.
- [ ] Opening the prune session dialog (Ctrl+P) shows the same enriched rendering.
- [ ] A search bar is present at the top of both dialogs, focused on open.
- [ ] Typing filters the list in real time by case-insensitive title substring.
- [ ] j/k/up/down still navigate the filtered list; Enter selects the highlighted session.
- [ ] Ctrl+U clears the query and restores the full list.
- [ ] The previously-highlighted session remains highlighted across filter changes when still present; otherwise selection falls back to the first filtered item.
- [ ] Empty filter result shows an explicit "no sessions match" message; Enter is a no-op.
- [ ] No regression on navigation, selection, or prune behavior compared to current main.
- [ ] Unit tests cover relative-time formatting, token formatting, and filter/selection stability.

## References

- Current dialog: `internal/tui/components/dialog/session.go`
- Keybindings: `internal/tui/tui.go:70-96`, `internal/tui/tui.go:647-661`
- Session model: `internal/session/session.go:14-41`
- DB model and trigger: `internal/db/models.go:46-59`, `internal/db/migrations/sqlite/20250424200609_initial.sql`
- Live-filter reference: `internal/tui/components/dialog/complete.go:94-180`
- Generic list (not used here, for context): `internal/tui/components/util/simple-list.go`
