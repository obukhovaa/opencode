# Chat Scroll Lock

**Date**: 2026-03-23
**Status**: Implemented
**Author**: AI-assisted

## Overview

Prevent the chat viewport from snapping to the bottom when new content arrives while the user is reading earlier messages. Auto-scroll resumes automatically when the user scrolls back to the bottom.

## Motivation

### Current State

The chat viewport (`messagesCmp` in `internal/tui/components/chat/list.go`) calls `viewport.GotoBottom()` unconditionally in three places:

```go
// 1. After async render completes (list.go ~line 110-126)
case renderFinishedMsg:
    m.viewport.SetContent(msg.viewportContent)
    m.viewport.GotoBottom()

// 2. On new/updated messages (list.go ~line 206-211)
if msg.Type == pubsub.CreatedEvent ||
    (msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == m.messages[len(m.messages)-1].ID) {
    m.viewport.GotoBottom()
}

// 3. When switching sessions (list.go ~line 679-681)
m.renderViewSync()
m.viewport.GotoBottom()
```

This creates problems:

1. **Lost reading position**: During streaming responses, every token update to the last message snaps the viewport to the bottom, making it impossible to read earlier messages
2. **No visual feedback**: The user has no way to know whether new content has arrived below their current scroll position

### Desired State

When the user scrolls up (via `ctrl+u`, `pgup`, mouse wheel, etc.), the viewport stays where it is regardless of new content arriving. A status bar indicator shows that new content is available below. When the user scrolls back to the bottom, auto-scroll resumes seamlessly.

This matches the behavior of tmux, iTerm2, VS Code terminal, Slack, Discord, and virtually every modern chat/terminal application.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Activation mechanism | Implicit via scroll position | No new keybindings needed; matches universal UX convention. Scroll up = lock, reach bottom = unlock |
| State tracking | `userScrolledUp bool` on `messagesCmp` | Simplest possible state; derived from `viewport.AtBottom()` after every scroll event |
| Status bar indicator | `↓ N new` badge in status bar | Shows exact count of new messages arrived while scrolled up; low cost since we already track the messages array |
| Session switch behavior | Always scroll to bottom | Loading a new session should show the latest state regardless of scroll lock |

## Architecture

```
                    ┌──────────────────────────┐
                    │     messagesCmp          │
                    │  ┌────────────────────┐  │
                    │  │ viewport.Model     │  │
                    │  │                    │  │
                    │  │  [earlier msgs]  ◄─┼──┼── user reading here
                    │  │  ...              │  │
                    │  │  [new content]     │  │   userScrolledUp = true
                    │  └────────────────────┘  │   → GotoBottom() suppressed
                    │                          │   → publish ScrollStateEvent
                    └──────────────────────────┘
                               │
                    ScrollStateEvent (pubsub)
                               │
                               ▼
                    ┌──────────────────────────┐
                    │      statusCmp           │
                    │  ... │ ↓ 3 new │ model ▶  │
                    └──────────────────────────┘
```

### State Transitions

```
STEP 1: User scrolls up
────────────────────────
Key press (ctrl+u / pgup / mouse wheel up) handled by viewport.
After viewport.Update(), check viewport.AtBottom().
If not at bottom → userScrolledUp = true, emit ScrollStateEvent{Locked: true}.

STEP 2: New content arrives
────────────────────────────
pubsub message event or renderFinishedMsg received.
If userScrolledUp → skip GotoBottom().
Content is still set via SetContent() so it exists in the viewport buffer.

STEP 3: User scrolls back to bottom
─────────────────────────────────────
Key press (ctrl+d / pgdn / mouse wheel down) handled by viewport.
After viewport.Update(), check viewport.AtBottom().
If at bottom → userScrolledUp = false, emit ScrollStateEvent{Locked: false}.
Auto-scroll resumes.
```

## Implementation Plan

### Phase 1: Scroll lock logic

- [x] **1.1** Add `userScrolledUp bool` field to `messagesCmp` struct in `internal/tui/components/chat/list.go`
- [x] **1.2** After every scroll key/mouse event dispatch to `viewport.Update()`, update `userScrolledUp` based on `viewport.AtBottom()`
- [x] **1.3** Gate the `GotoBottom()` call in `renderFinishedMsg` handler with `if !m.userScrolledUp`
- [x] **1.4** Gate the `GotoBottom()` call in `pubsub.Event[message.Message]` handler with `if !m.userScrolledUp`
- [x] **1.5** Keep the `GotoBottom()` call in `SetSession` unconditional (switching sessions should always show latest) and reset `userScrolledUp = false`

### Phase 2: Status bar indicator

- [x] **2.1** Add `newMessageCount int` field to `messagesCmp` to track messages arriving while scrolled up
- [x] **2.2** Increment `newMessageCount` on `pubsub.CreatedEvent` when `userScrolledUp` is true; reset to 0 when `userScrolledUp` clears
- [x] **2.3** Add `ScrollStateMsg` to `internal/tui/components/chat/chat.go` carrying `Locked bool` and `NewMessages int` (TUI msg instead of pubsub event — simpler, same routing)
- [x] **2.4** Emit `ScrollStateMsg` from `messagesCmp` on lock/unlock transitions and on `newMessageCount` changes
- [x] **2.5** Handle `ScrollStateMsg` in `statusCmp` (`internal/tui/components/core/status.go`)
- [x] **2.6** Render a `↓ N new` badge (using theme warning or info color) when scroll lock is active and count > 0

### Phase 3: Mouse wheel support

- [x] **3.1** Verify that `viewport.MouseWheelEnabled` is set and mouse wheel events flow through to `viewport.Update()`
- [x] **3.2** Ensure `userScrolledUp` is updated after mouse wheel events using the same `AtBottom()` check

## Edge Cases

### Streaming response while scrolled up

1. User scrolls up to read an earlier message
2. Assistant response streams in, updating the last message rapidly
3. `GotoBottom()` is suppressed; content accumulates in the viewport buffer below the visible area
4. Status bar shows `↓ N new` (incrementing as new messages arrive)
5. User scrolls to bottom; `userScrolledUp` clears; counter resets; they see the full streamed response

### Async re-render while scrolled up

1. User scrolls up
2. Cache miss triggers async render; `renderFinishedMsg` arrives with new viewport content
3. `SetContent()` is called (viewport preserves scroll offset by default), `GotoBottom()` is skipped
4. User's reading position is preserved

### SetContent resets scroll position

1. If `viewport.SetContent()` resets the scroll offset to 0 (top), the user would jump to the top instead of staying put
2. Need to verify `viewport.Model` behavior — if it resets, save and restore `YOffset` around `SetContent()` calls when `userScrolledUp` is true

### User sends a new message while scrolled up

1. User is scrolled up reading history
2. User types and sends a new message (this implies they're done reading)
3. The new user message event should clear `userScrolledUp` and scroll to bottom
4. This is already handled: sending a message creates a `pubsub.CreatedEvent` which triggers `GotoBottom()` — but it would be gated. Need to ensure that when the *user* creates a message (not assistant), scroll lock is cleared

## Open Questions

1. **Does `viewport.SetContent()` preserve scroll offset?**
   - If it resets to top or bottom, we need to save/restore `YOffset`
   - **Recommendation**: Test empirically with the charm v2 viewport; if it resets, wrap `SetContent()` with offset save/restore when `userScrolledUp` is true

2. **Should user sending a message clear scroll lock?**
   - Options: (a) Always clear on user message, (b) Only clear on explicit scroll-to-bottom
   - **Recommendation**: (a) — sending a message signals intent to re-engage with the conversation flow

3. ~~**Should the indicator show a message count (e.g., `↓ 3 new`) or just `↓ MORE`?**~~
   - **Resolved**: Show `↓ N new` with exact count. Implementation cost is low — just increment a counter on `CreatedEvent` while `userScrolledUp` is true, reset on unlock

## Success Criteria

- [x] User can scroll up during streaming and viewport stays in place
- [x] Scrolling back to bottom resumes auto-scroll
- [x] Status bar shows `↓ N new` with accurate count when scrolled up and new messages arrived
- [x] Session switching always scrolls to bottom
- [x] No visual glitches when `SetContent()` is called while scrolled up (YOffset save/restore around SetContent)

## References

- `internal/tui/components/chat/list.go` — `messagesCmp`, viewport, scroll key handling, `GotoBottom()` calls
- `internal/tui/components/core/status.go` — Status bar rendering, widget pattern
- `internal/pubsub/events.go` — Event type definitions
- `internal/tui/page/chat.go` — Chat page layout, component wiring
