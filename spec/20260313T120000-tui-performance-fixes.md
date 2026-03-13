# TUI Performance Fixes — Progressive Unresponsiveness

**Date**: 2026-03-13
**Status**: Draft
**Author**: AI-assisted

## Overview

After upgrading to the Charmbracelet v2 ecosystem (spec `20260309T120000-tui-dependency-upgrade-v2.md`), OpenCode becomes progressively unresponsive during long sessions. Typing in the chat editor exhibits ~1 second latency per character. The root cause is a combination of unconditional spinner ticks driving continuous re-renders, expensive uncached allocations in hot render paths, synchronous DB calls blocking the event loop, and O(n²) computations in `View()` that grow with conversation length.

## Motivation

### Current State

The TUI renders at the spinner's tick rate (~8 fps) unconditionally, even when idle. Each render frame:
- Allocates a new `glamour.TermRenderer` for every markdown block (no cache)
- Creates dozens of `lipgloss.NewStyle()` objects via `styles.BaseStyle()` calls
- Iterates all messages × all tool calls in `countToolsWithoutResponse()` and `hasUnfinishedToolCalls()` (O(n²))
- Runs a regex-based ANSI rewriter (`ForceReplaceBackgroundWithLipgloss`) over full rendered strings
- Calls `lipgloss.Height()`/`lipgloss.Width()` on the full terminal output for each overlay (re-parses ANSI)
- Issues synchronous DB queries inside `renderToolMessage()` for every `TaskToolName` tool call

### Problems

1. **Progressive degradation**: Performance worsens linearly with conversation length due to O(n²) `View()` computations and O(n) full-list rebuilds on every pubsub event
2. **Idle CPU burn**: Spinner tick fires continuously even when no agent is running, driving unnecessary `Update→View` cycles
3. **Blocking event loop**: Synchronous DB calls in `renderToolMessage()` and `sidebar.processFileChanges()` stall the Bubble Tea event loop
4. **Allocation pressure**: `GetMarkdownRenderer()` allocates a full Chroma renderer + 40-field style config on every call; `BaseStyle()` allocates a new `lipgloss.Style` on every call
5. **Data race**: `SetSession()` returns a `tea.Cmd` that calls `renderView()` from a goroutine while the main loop may be reading/writing the same fields

### Desired State

OpenCode remains responsive throughout long sessions. Typing latency stays under 16ms regardless of conversation length. CPU usage is near zero when idle.

## Research Findings

### Performance Profile (estimated per render frame, 100-message session)

| Hot Path | Current Cost | Target Cost |
|----------|-------------|-------------|
| `spinner.Tick` when idle | ~8 fps render cycles | 0 fps (no tick) |
| `GetMarkdownRenderer()` | 1 glamour alloc per markdown block | 1 alloc per (width, theme) change |
| `countToolsWithoutResponse()` | O(messages × toolCalls) per frame | O(1) cached value |
| `renderView()` on pubsub event | O(n) full join + O(uncached) renders | O(1) incremental append |
| `ForceReplaceBackgroundWithLipgloss()` | O(content_length) regex per frame | cached, only on content change |
| `BaseStyle()` | ~50 `lipgloss.NewStyle()` per frame | 1 per theme change |
| `lipgloss.Height/Width(appView)` | 2 ANSI parses per overlay (up to 10 overlays) | 1 cached pair per frame |
| `projectDiagnostics()` | O(clients × diagnostics) per frame | cached, update on LSP events |
| DB call in `renderToolMessage()` | 1 query per TaskTool per render | 0 (pre-fetched in Update) |
| DB call in `findInitialVersion()` | 1 query per file change event | cached per session |

### Bubbletea v2 Render Model

In bubbletea v2, `View()` returns `tea.View` and is called after every `Update()`. There is no built-in dirty-checking — every `Update` triggers a full `View`. This makes it critical to:
- Minimize `Update` frequency (don't tick when not needed)
- Make `View` cheap (cache rendered strings, avoid allocations)
- Never block in `Update` or `View` (async DB, async file I/O)

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Spinner lifecycle | Conditional: start tick when agent becomes busy, stop when idle | Eliminates ~8 fps idle render cycles; spinner is invisible when idle anyway |
| Markdown renderer cache | `sync.Map` keyed on `(width, themeID)`, invalidated on `ThemeChangedMsg` | glamour renderer is expensive; width+theme are the only inputs that change |
| Tool call counts | Compute in `Update()` on message events, store as struct field | Moves O(n²) out of `View()` into event-driven `Update()` |
| `BaseStyle()` caching | Package-level cached style, invalidated on theme change via `ThemeChangedMsg` | Eliminates ~50 `lipgloss.NewStyle()` allocations per frame |
| Task tool DB calls | Pre-fetch in `Update()` when message arrives, cache in `map[toolCallID][]message.Message` | Removes synchronous DB from render path |
| Sidebar file versions | Cache `initialVersions map[path]history.File` per session, update incrementally | Eliminates repeated `ListBySessionTree` DB queries |
| `ForceReplaceBackgroundWithLipgloss` | Cache result alongside rendered content in `cachedContent` map | Regex runs once per content change, not per frame |
| Overlay dimensions | Cache `appViewWidth`/`appViewHeight` once per `View()` call | Eliminates redundant ANSI parsing (currently 2 parses × up to 10 overlays) |
| Filepicker double-update | Remove the unconditional `default:` case update | Bug fix: filepicker should only update when shown |
| `SetSession` data race | Move `renderView()` call to `Update()` handler for `renderFinishedMsg` | Eliminate goroutine access to shared model state |
| Log table rebuild | Incremental: append new row, maintain sorted order | Eliminates O(n log n) sort + JSON marshal on every log event |
| `projectDiagnostics()` | Cache diagnostic counts, update on LSP diagnostic events | Eliminates O(clients × diagnostics) iteration per frame |

## Implementation Plan

### Phase 1: Critical — Stop Idle Rendering

- [ ] **1.1** Conditional spinner tick in `list.go`:
  - Remove `m.spinner.Tick` from `Init()` return
  - Add `spinnerActive bool` field to `messagesCmp`
  - On `pubsub.Event[agent.AgentEvent]` or when `IsAgentWorking()` transitions to true: set `spinnerActive = true`, return `m.spinner.Tick`
  - On agent done / `IsAgentWorking()` transitions to false: set `spinnerActive = false`
  - In `Update()` line 162–164: only call `m.spinner.Update(msg)` and re-queue tick when `spinnerActive` is true
  - Guard: on `spinner.TickMsg`, check `spinnerActive` before processing

- [ ] **1.2** Cache markdown renderer in `styles/markdown.go`:
  - Add package-level cache: `var mdRendererCache struct { sync.Mutex; renderer *glamour.TermRenderer; width int; themeID string }`
  - In `GetMarkdownRenderer(width)`: check cache hit on `(width, theme.CurrentTheme().ID())`, return cached if match
  - Add `InvalidateMarkdownCache()` function, call from theme change handler
  - Thread `ThemeChangedMsg` through to invalidate

- [ ] **1.3** Move `countToolsWithoutResponse` and `hasUnfinishedToolCalls` out of `View()`:
  - Add `pendingCounts pendingToolCounts` and `hasUnfinished bool` fields to `messagesCmp`
  - Recompute in `Update()` after any message event changes `m.messages` (in `pubsub.Event[message.Message]` handler)
  - In `working()`: read from cached fields instead of calling functions

### Phase 2: High — Reduce Render Cost

- [ ] **2.1** Cache `styles.BaseStyle()`:
  - Add package-level `var cachedBaseStyle lipgloss.Style` and `var baseStyleThemeID string`
  - In `BaseStyle()`: compare `theme.CurrentTheme().ID()` with `baseStyleThemeID`, return cached if match
  - Invalidate on theme change (check theme ID changed)

- [ ] **2.2** Remove synchronous DB call from `renderToolMessage()` in `message.go`:
  - Add `taskMessages map[string][]message.Message` field to `messagesCmp`
  - In `Update()` on `pubsub.Event[message.Message]` with `CreatedEvent`: if the message's SessionID matches a known task tool call ID, pre-fetch and cache `messagesService.List(ctx, toolCallID)` in a `tea.Cmd`
  - Pass `taskMessages` map to `renderAssistantMessage()` / `renderToolMessage()` instead of `messagesService`
  - In `renderToolMessage()` line 769: read from map instead of calling `messagesService.List()`

- [ ] **2.3** Cache overlay dimensions in `tui.go` `View()`:
  - Compute `appViewHeight := lipgloss.Height(appView)` and `appViewWidth := lipgloss.Width(appView)` once after building `appView`
  - Reuse cached values for all overlay positioning (currently computed 2× per overlay, up to 10 overlays = 20 ANSI parses)

- [ ] **2.4** Fix filepicker double-update in `tui.go`:
  - Remove the unconditional `a.filepicker.Update(msg)` call in the `default:` case (line 676–678)
  - The filepicker is already updated conditionally when `a.showFilepicker` is true (line 682–689)

- [ ] **2.5** Cache `ForceReplaceBackgroundWithLipgloss` results:
  - In `renderMessage()` and `renderToolResponse()`: the background replacement is already part of `cachedContent` since it runs during `renderView()` — verify no `View()` path calls it on uncached content
  - In `logs/table.go` `View()` and `logs/details.go` `View()`: cache the result, invalidate on theme change or content change
  - In `agents/table.go` `View()`: same pattern

### Phase 3: Medium — Reduce Event Processing Cost

- [ ] **3.1** Fix `SetSession` data race in `list.go`:
  - Remove the goroutine in the returned `tea.Cmd` (line 475–478)
  - Call `m.renderView()` synchronously in `SetSession()` before returning the cmd
  - Or: return a `tea.Cmd` that only returns `renderFinishedMsg{}` and call `m.renderView()` in the `renderFinishedMsg` handler in `Update()`

- [ ] **3.2** Cache sidebar file versions in `sidebar.go`:
  - Add `initialVersions map[string]history.File` field to `sidebarCmp`
  - Populate in `loadModifiedFiles()` (already fetches all files)
  - In `findInitialVersion()` (line 448): look up from `initialVersions` cache instead of calling `ListBySessionTree()` (DB query)
  - Invalidate cache on `SessionSelectedMsg` (already calls `loadModifiedFiles()`)
  - On `pubsub.Event[history.File]` with `InitialVersion`: add to cache directly

- [ ] **3.3** Incremental log table updates in `logs/table.go`:
  - Instead of `setRows()` (full rebuild with sort + JSON marshal for all rows), add a method `appendRow(log LogMessage)` that inserts in sorted position
  - In `Update()` on `pubsub.Event[logging.LogMessage]`: call `appendRow(msg.Payload)` instead of `setRows()`
  - Keep `setRows()` for `Init()` (initial load)

- [ ] **3.4** Cache `projectDiagnostics()` in `status.go`:
  - Add `cachedDiagnostics string` and `diagnosticsDirty bool` fields to `statusCmp`
  - Set `diagnosticsDirty = true` on LSP-related pubsub events (if any exist) or on a coarse timer
  - In `projectDiagnostics()`: return `cachedDiagnostics` if not dirty; recompute and cache if dirty
  - Since there's no LSP diagnostic pubsub event currently, use a simple approach: recompute only when `View()` is called AND a flag is set (e.g., on every Nth frame or on `WindowSizeMsg`)

- [ ] **3.5** Stop mutating state in `View()` methods:
  - `logs/table.go` line 66–68: move `table.SetStyles()` to `Update()` on `ThemeChangedMsg`
  - `agents/table.go`: same pattern
  - `page/chat.go` line ~265: move `activeDialog.SetWidth(editorWidth)` to `SetSize()` or `Update()`

### Phase 4: Low — Polish

- [ ] **4.1** Cache `getHelpWidget()` and `getAgentHintWidget()` in `status.go`:
  - These are already computed at `NewStatusCmp` time (line 344–345) but the cached values `helpWidget`/`agentHintWidget` are only used for width calculation (line 194), not for rendering (line 169–170)
  - Fix: use the cached module-level vars in `View()` directly
  - Invalidate on `ThemeChangedMsg` and `ActiveAgentChangedMsg`

- [ ] **4.2** Cache `resolveModel()` in `status.go`:
  - Called every `View()`, reads config and registry
  - Cache result, invalidate on `ActiveAgentChangedMsg` and `ModelSelectedMsg`

- [ ] **4.3** Cache `lspsConfigured()` in `chat.go`:
  - Called from `sidebar.View()` and `initialScreen()`
  - Cache at component level, invalidate on LSP state changes

- [ ] **4.4** Reduce `layout.KeyMapToSlice()` reflection cost:
  - Called from `tui.go` `View()` when help is shown, and from `BindingKeys()` in table components
  - Cache the result per key map pointer, invalidate never (key maps don't change at runtime)

- [ ] **4.5** Scope sidebar subscription lifecycle:
  - `sidebar.go` line 50–51: uses `context.Background()` for subscription — goroutine never cleaned up
  - Pass a cancellable context from the parent component
  - Cancel on `SessionSelectedMsg` when sidebar is replaced, or on component teardown

- [ ] **4.6** Clean up `cachedContent` map growth in `list.go`:
  - Currently grows unboundedly (old message IDs never evicted)
  - On `SessionSelectedMsg` / `SessionClearedMsg`: clear the entire map
  - This already partially happens (map is recreated on session change) but verify no leak path

## Edge Cases

### Theme change during active session

1. `ThemeChangedMsg` arrives
2. Must invalidate: markdown cache, base style cache, diagnostics cache, help widget cache, all `cachedContent` entries
3. `messagesCmp.rerender()` already clears `cachedContent` and calls `renderView()` — verify this is sufficient
4. New caches (markdown, base style) must also be invalidated

### Spinner start/stop race

1. Agent finishes between `Update()` and `View()`
2. `spinnerActive` is set to false, but a queued `spinner.TickMsg` arrives
3. Guard: in `Update()`, ignore `spinner.TickMsg` when `spinnerActive` is false (don't re-queue tick)

### Session with many task tool calls

1. A session has 50+ task tool calls, each requiring a `messagesService.List()` for child messages
2. Pre-fetching all 50 in `Update()` would be expensive
3. Approach: lazy-fetch on first render, cache result; subsequent renders use cache
4. Cache key: `toolCallID`; invalidate entry when a child message event arrives for that tool call

### Empty session / no messages

1. `countToolsWithoutResponse()` and `hasUnfinishedToolCalls()` on empty slice → zero cost
2. Cached values default to `pendingToolCounts{}` and `false` — correct

### Concurrent pubsub events during render

1. Multiple `pubsub.Event[message.Message]` arrive in rapid succession (streaming)
2. Each triggers `renderView()` which is O(n) with cache
3. The message cache (`cachedContent`) means only the changed message is re-rendered
4. The final `lipgloss.JoinVertical` over all messages is still O(n) string ops — acceptable as long as individual messages are cached

### Log table with 1000+ entries

1. Current: `setRows()` sorts all 1000+ entries and JSON-marshals each one on every new log
2. After fix: only the new entry is inserted in O(log n) position
3. Edge case: if `logging.List()` returns entries out of order (shouldn't happen), the incremental approach may produce wrong order
4. Fallback: keep `setRows()` available for forced full rebuild

## Success Criteria

- [ ] Typing latency in chat editor stays under 16ms with 200+ messages in session
- [ ] CPU usage is near zero when idle (no agent running, no typing)
- [ ] No data races detected by `go test -race ./internal/tui/...`
- [ ] `make test` passes
- [ ] Theme switching still works correctly (all caches invalidated)
- [ ] Spinner renders when agent is working, stops when idle
- [ ] Sidebar file changes still update correctly
- [ ] Log table still updates on new log entries
- [ ] Overlay positioning is visually identical
- [ ] No regressions in dialog behavior (permission, session, command, model, help, quit, init, filepicker, theme, arguments)

## References

- `internal/tui/components/chat/list.go` — spinner tick (line 69), `renderView()` (lines 172–248), `countToolsWithoutResponse()` (lines 306–331), `hasUnfinishedToolCalls()` (lines 345–356), `working()` (lines 358–388), `SetSession()` data race (lines 475–478)
- `internal/tui/components/chat/message.go` — `toMarkdown()` (line 46–49), `renderToolMessage()` DB call (line 769), `subagentBadge()` registry call (line 811), `ForceReplaceBackgroundWithLipgloss` calls (lines 69, 552, 559, 586, 599, 617, 654, 660)
- `internal/tui/styles/markdown.go` — `GetMarkdownRenderer()` (lines 20–26), `generateMarkdownStyleConfig()` (lines 30–280)
- `internal/tui/styles/styles.go` — `BaseStyle()` (lines 17–22)
- `internal/tui/styles/background.go` — `ForceReplaceBackgroundWithLipgloss()` (lines 28–127)
- `internal/tui/tui.go` — filepicker double-update (lines 675–690), overlay dimension parsing (lines 837–1037), `View()` (lines 824–1042)
- `internal/tui/components/core/status.go` — `projectDiagnostics()` (lines 229–311), `getHelpWidget()` (lines 87–96), `resolveModel()` (lines 148–162), `View()` (lines 164–227)
- `internal/tui/components/chat/sidebar.go` — `findInitialVersion()` DB call (lines 448–469), `processFileChanges()` (lines 415–446), `context.Background()` subscription (line 50–51)
- `internal/tui/components/logs/table.go` — `setRows()` full rebuild (lines 92–119), `View()` mutating styles (lines 64–69)
- `internal/tui/layout/overlay.go` — `PlaceOverlay()` style allocation (lines 43–63)
- `internal/tui/components/chat/chat.go` — `lspsConfigured()` (line 39)
- `internal/pubsub/broker.go` — silent event drops (lines 104–109), buffer size 64 (line 8)
