# Top Bar & MCP Sidebar

**Date**: 2026-04-02
**Status**: In Progress
**Author**: AI-assisted

## Overview

Add a top bar that mirrors the status bar visually and replace redundant sidebar content with an MCP server status section. The top bar shows session name, cwd, and project name. The sidebar gains an MCP section showing configured servers and their active/inactive state per agent.

## Motivation

### Current State

The window layout stacks `pageContent + statusBar` vertically. The messages container has `padding(1,1,0,1)` which produces a blank line at the top of the window:

```go
// tui.go — View()
components := []string{pageContent}
components = append(components, a.status.View().Content)
appView := lipgloss.JoinVertical(lipgloss.Top, components...)

// tui.go — Update(), WindowSizeMsg
msg.Height -= 1 // only accounts for bottom status bar
```

The sidebar duplicates information already available elsewhere:

```go
// sidebar.go — View()
lipgloss.JoinVertical(
    header(m.width),    // logo + cwd  ← duplicated on initial screen
    " ",
    m.projectSection(), // "Project: <id>"  ← no other use for this space
    " ",
    m.sessionSection(), // "Session: <title> [local/remote]"
    " ",
    lspsConfigured(m.width),
    " ",
    m.modifiedFiles(),
)
```

This creates problems:

1. **Wasted top line**: A blank row at the top of the window looks unfinished, especially when OpenCode runs inside another terminal panel (tmux, IDE terminal, tiling WM)
2. **Sidebar clutter**: Logo, cwd, and project ID consume ~7 lines of sidebar space without providing actionable info — they're static context better suited for a persistent bar
3. **No MCP visibility**: MCP servers are configured in `.opencode.json` but invisible in the UI. Users can't tell which MCP servers are configured, nor whether their tools loaded successfully for the active agent

### Desired State

A one-line top bar (same visual treatment as the bottom status bar) provides at-a-glance context. The sidebar uses the freed vertical space for an MCP section that shows server names and their active status, updating reactively when the user switches agents.

## Research Findings

### Status bar widget pattern

The existing `statusCmp` in `internal/tui/components/core/status.go` uses a left-to-right widget composition pattern:

```
[help] [agents] [auto-approve] [scroll] [tokens] [spacer/info] [diagnostics] [model ▶ Agent]
```

Each widget is a styled `lipgloss.Render()` call with `styles.Padded()` for consistent spacing. Width arithmetic ensures no overflow. The same pattern applies to the top bar with different content.

**Key finding**: The status bar already handles theme changes, agent switches, and session events — the top bar needs the same message routing.

### MCP tool loading

MCP tools are loaded asynchronously in `internal/llm/agent/tools.go` via `mcpRegistry.LoadTools()`. The `mcpRegistry` in `internal/llm/agent/mcp-tool.go` caches tool lists per server name with a 30-minute TTL. Tool names are namespaced as `<serverName>_<toolName>`.

Each agent's tool set is resolved once via `resolveTools()` (guarded by `sync.Once`). After resolution, `agent.Tools()` returns the final `[]tools.BaseTool` slice.

**Key finding**: To determine if an MCP server is "active" for a given agent, we need to check whether any tool with the server's name prefix exists in the agent's resolved tool set. The `app.ActiveAgent()` exposes `Tools()` which returns the resolved tools.

**Implication**: MCP active state is derivable at render time — no new subscriptions or event channels needed.

### LSP sidebar section pattern

The `lspsConfigured()` function in `internal/tui/components/chat/chat.go` provides a direct template:

- Reads static config (`install.ResolveServers(cfg)`)
- Renders a bold title + bullet list
- Caches output by `(width, themeID)` tuple

The MCP section follows the same pattern but adds a per-item active/inactive indicator.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Top bar component | New `TopBarCmp` in `internal/tui/components/core/topbar.go` | Keeps status bar unchanged; clean separation of concerns |
| Top bar content | `[session name]` left, `[cwd]` center-left, `[project name]` right | Most useful contextual info; mirrors IDE title bar conventions |
| Height accounting | `msg.Height -= 2` in tui.go | Two bars now: top + bottom. Single place to change |
| MCP active detection | Check `app.ActiveAgent().Tools()` for tools prefixed with server name | Uses existing resolved tool set; no new async plumbing |
| MCP section placement | Between LSP section and modified files in sidebar | Logical grouping: LSP then MCP (both are external integrations) |
| Sidebar header removal | Remove `header()`, `projectSection()`, `sessionSection()` from sidebar | This info moves to top bar; frees ~7 lines |
| Initial screen | Keep `header()` + `lspsConfigured()` on initial screen (no session) | Top bar shows "No session" when empty; initial screen still needs branding |
| Cache invalidation for MCP | Invalidate on `ActiveAgentChangedMsg` | Agent switch changes which tools are loaded, so active status may differ |

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│ [session name]              [cwd: /path/to/project]   [project-id] │  ← TopBarCmp (1 line)
├──────────────────────────────────────────┬──────────────────────────┤
│                                          │ LSP                     │
│                                          │ • gopls (gopls)         │
│           Messages / Initial             │                         │
│                                          │ MCP                     │
│                                          │ ● github (active)       │
│                                          │ ○ slack (inactive)      │
│                                          │                         │
│                                          │ Modified Files:         │
│                                          │ ...                     │
├──────────────────────────────────────────┤                          │
│ Editor                                   │                          │
├──────────────────────────────────────────┴──────────────────────────┤
│ [help] [agents] ... [spacer] [diagnostics] [model ▶ Agent]        │  ← StatusCmp (1 line)
└─────────────────────────────────────────────────────────────────────┘
```

### Top bar layout

```
Left:                                                          Right:
┌──────────────┬─────────────────────────┬─────────────────────────┐
│ session name │  cwd: /path/to/project  │           project-id    │
└──────────────┴─────────────────────────┴─────────────────────────┘
```

- Session name uses `t.Primary()` foreground on `t.BackgroundDarker()` background (matching status bar spacer style)
- CWD uses `t.TextMuted()` foreground
- Project name uses `t.Secondary()` background (matching model widget style), right-aligned
- When no session is active, session widget shows nothing or a placeholder

### MCP section in sidebar

```
MCP
● github (npx @github/mcp)
○ slack (npx @slack/mcp)
```

- `●` (filled circle) for active servers — rendered in `t.Success()` color
- `○` (empty circle) for inactive/no-tools-loaded — rendered in `t.TextMuted()` color
- Server command shown in parentheses, truncated like LSP entries
- For SSE/HTTP type servers, show URL instead of command

## Implementation Plan

### Phase 1: Top bar component

- [x] **1.1** Create `internal/tui/components/core/topbar.go` with `TopBarCmp` interface and `topbarCmp` struct
- [x] **1.2** Implement `View()` rendering: session name (left), cwd (mid), project name (right-docked) — all on a single line using the same `styles.Padded()` pattern as status bar
- [x] **1.3** Handle `SessionSelectedMsg`, `SessionClearedMsg`, `ActiveAgentChangedMsg`, `ThemeChangedMsg` in `Update()`
- [x] **1.4** Wire `TopBarCmp` into `appModel` in `tui.go`: construct in `New()`, init in `Init()`, update in `Update()`, render in `View()` above page content
- [x] **1.5** Change `msg.Height -= 1` to `msg.Height -= 2` in `WindowSizeMsg` handler to account for both bars
- [x] **1.6** Compose view as `topbar + pageContent + statusbar` in `View()`

### Phase 2: Sidebar cleanup

- [x] **2.1** Remove `header()` call from sidebar `View()` (logo, cwd)
- [x] **2.2** Remove `projectSection()` call and method from sidebar
- [x] **2.3** Remove `sessionSection()` call and method from sidebar
- [x] **2.4** Adjust sidebar `View()` to start with LSP section directly (or a small spacer)

### Phase 3: MCP sidebar section

- [x] **3.1** Create `mcpServersConfigured()` function in `internal/tui/components/chat/chat.go` (alongside `lspsConfigured()`)
- [x] **3.2** Read `config.Get().MCPServers` for configured server names and connection details
- [x] **3.3** Accept the active agent's tools list and derive active state per MCP server: a server is active if any tool in the list has the `<serverName>_` prefix
- [x] **3.4** Render with `●`/`○` indicators, server name, and truncated command/URL
- [x] **3.5** Add cache struct similar to `cachedLspsConfigured`, keyed by `(width, themeID, agentName)`
- [x] **3.6** Integrate into sidebar `View()` between LSP and modified files sections
- [x] **3.7** Pass active agent info to sidebar — either via constructor dependency on `app.App` or via a new message type. The sidebar needs to know the current agent's tools

### Phase 4: Reactive updates

- [x] **4.1** Handle `ActiveAgentChangedMsg` in sidebar to invalidate MCP cache and re-render
- [x] **4.2** Ensure `ActiveAgentChangedMsg` propagates to sidebar (it already flows through `tui.go` → page → layout → sidebar via standard Bubble Tea message routing)
- [x] **4.3** Verify that initial render after app startup shows correct MCP state once tools finish loading asynchronously

## Edge Cases

### No MCP servers configured

1. `config.Get().MCPServers` is empty
2. `mcpServersConfigured()` returns empty string
3. Sidebar skips the MCP section entirely (no "MCP" header shown)

### MCP tools still loading

1. App starts, MCP tools load asynchronously via goroutines in `NewToolSet()`
2. Sidebar renders before tools finish loading — all MCP servers show as inactive
3. Once `resolveTools()` completes (triggered by first `agent.Run()` or `agent.Tools()` call), next render shows correct active state
4. **Mitigation**: This is acceptable — the sidebar refreshes on session/agent events anyway. Could optionally add a "loading" spinner but likely overkill

### Agent switch changes MCP active state

1. User presses `tab` to switch from Coder to Hivemind
2. `ActiveAgentChangedMsg` fires
3. Sidebar invalidates MCP cache, re-queries new agent's tools
4. MCP section updates to reflect which servers have tools enabled for the new agent

### Very long session title

1. Session title exceeds available width in top bar
2. Truncate with `ansi.Truncate()` like LSP names, adding `…` suffix

### No active session

1. Top bar renders without session name — show blank or "No session" in muted text
2. CWD and project name still display (they're session-independent)

## Resolved Questions

1. ~~**Should the top bar show "OpenCode" branding when no session is active?**~~
   - **Resolved**: (c) Leave session widget blank. The initial screen already shows branding in the messages area. Top bar stays purely informational.

2. ~~**Should the sidebar show MCP server type (stdio/sse/http)?**~~
   - **Resolved**: (b) Just show command/URL. The command/URL already implies the type; a badge would clutter the narrow sidebar.

3. ~~**Should the initial welcome screen also show MCP servers (alongside LSP)?**~~
   - **Resolved**: (a) Yes — add MCP section to `initialScreen()` for immediate feedback that MCP is configured.

4. ~~**How to pass agent tool info to the sidebar?**~~
   - **Resolved**: (a) Pass `*app.App` to sidebar constructor, query `app.ActiveAgent().Tools()` at render time. Consistent with existing `session.Service` and `history.Service` dependencies.

## Success Criteria

- [ ] No blank line at the top of the window — top bar fills the first row
- [ ] Top bar shows session name (left), cwd (center), project name (right) with status-bar-level styling
- [ ] Sidebar no longer shows logo, cwd, or project section
- [ ] MCP section appears in sidebar when MCP servers are configured
- [ ] MCP servers show `●` (active) or `○` (inactive) based on loaded tools for current agent
- [ ] Switching agents updates MCP active indicators
- [ ] Theme changes re-render both top bar and MCP section correctly
- [ ] No layout overflow or widget truncation at common terminal widths (80, 120, 200 cols)

## References

- `internal/tui/tui.go` — Root layout composition, `View()`, `WindowSizeMsg` height math
- `internal/tui/components/core/status.go` — Status bar pattern to mirror for top bar
- `internal/tui/components/chat/sidebar.go` — Sidebar sections to remove/replace
- `internal/tui/components/chat/chat.go` — `header()`, `lspsConfigured()`, `logo()`, `cwd()` functions
- `internal/tui/components/chat/list.go` — `initialScreen()` welcome view
- `internal/tui/page/chat.go` — Chat page layout, sidebar container creation
- `internal/llm/agent/mcp-tool.go` — MCP tool naming convention (`<server>_<tool>`)
- `internal/llm/agent/tools.go` — Tool set resolution, `resolveTools()`, `OrderTools()`
- `internal/config/config.go` — `MCPServers map[string]MCPServer` config struct
- `internal/db/project.go` — `GetProjectID()` for project name
