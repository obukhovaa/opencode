# Auto-Approve Permissions Mode

**Date**: 2026-03-26
**Status**: Draft
**Author**: AI-assisted

## Overview

Add a per-session auto-approve mode that lets the active agent run freely with whatever permissions it already has, skipping the interactive approval dialog for `ask`-resolved permissions. A built-in `/auto-approve` slash command toggles the mode on and off. The status bar reflects the current state.

## Motivation

### Current State

```go
// permission/permission.go — every "ask" permission blocks the agent until the user clicks allow/deny
func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) bool {
    if s.IsAutoApproveSession(opts.SessionID) {
        return true // only set for non-interactive and flow sessions
    }
    // ... publish event, block on channel, wait for user
}
```

```go
// Non-interactive mode (cmd/flow.go) sets auto-approve on the session
a.Permissions.AutoApproveSession(sess.ID)
```

```go
// The agent registry already enforces deny/disable before the permission service is consulted
func (r *registry) EvaluatePermission(agentID, toolName, input string) permission.Action {
    if !permission.IsToolEnabled(toolName, a.Tools) {
        return permission.ActionDeny // hard deny — never reaches Request()
    }
    return permission.EvaluateToolPermission(toolName, input, a.Permission, r.globalPerms)
}
```

In TUI interactive sessions, every `ask`-resolved tool call shows a permission dialog. There is no way to tell the agent "I trust you, just go" without restarting in non-interactive mode or configuring every permission rule to `allow` in `.opencode.json`.

Problems:

1. **Friction during trusted sessions**: When iterating quickly on a codebase the user already trusts, clicking "Allow" on every bash command, file write, and skill invocation slows the workflow to a crawl. Users want a way to say "run freely" without leaving the TUI.
2. **No runtime toggle**: The `AutoApproveSession` mechanism exists but is only wired to non-interactive mode and flows. Interactive TUI sessions have no way to opt in.
3. **Claude Code has this, we don't**: Claude Code offers `Shift+Tab` to cycle through permission modes including auto-approve variants. OpenCode has no equivalent runtime control.

### Desired State

```
# In TUI, user types /auto-approve → agent runs freely
# Status bar shows: "auto-approve" indicator
# Type /auto-approve again → back to normal ask mode
# Status bar indicator disappears
```

The existing permission evaluation hierarchy stays intact. `deny` rules and disabled tools still block. Only `ask` decisions are promoted to `allow` for the current session.

## Research Findings

### Claude Code Permission Modes

Claude Code supports 6 permission modes cycled via `Shift+Tab`:

| Mode | Behavior | Equivalent in OpenCode |
|------|----------|----------------------|
| `default` | Read files only | Default TUI behavior |
| `acceptEdits` | Read + edit files | N/A — we bake this into agent definitions |
| `plan` | Read only, no edits/commands | `hivemind` agent (read-only tools) |
| `auto` | All actions with classifier checks | No equivalent |
| `dontAsk` | Only pre-approved tools | Granular permission config |
| `bypassPermissions` | Everything, no checks | `--dangerously-skip-permissions` / non-interactive |

**Key finding**: Claude Code layers permission modes *on top* of a single agent. OpenCode takes a different approach — permission behavior is *baked into agent definitions*. Each agent carries its own tool set and permission configuration. Switching from "plan mode" to "code mode" in OpenCode means switching from `hivemind` to `coder`, not toggling a permission flag on the same agent.

**Implication**: Auto-approve in OpenCode should not change *which* permissions an agent has. It should only change *how* `ask` decisions are resolved — from "block and prompt" to "approve silently". The agent's configured `deny` rules and tool restrictions remain enforced. This is philosophically consistent: the agent already carries the right permission model; auto-approve just says "I trust the configuration, stop asking me."

### Existing Auto-Approve Infrastructure

The `permissionService` already has full support for per-session auto-approve:

| Component | Location | Status |
|-----------|----------|--------|
| `AutoApproveSession(sessionID)` | `permission/permission.go:119` | Exists, stores in `sync.Map` |
| `IsAutoApproveSession(sessionID)` | `permission/permission.go:123` | Exists, checked at top of `Request()` |
| Subagent inheritance | `agent/agent-tool.go:146` | Exists, propagates to task sessions |
| Agent-level deny enforcement | `agent/registry.go:145-156` | Exists, checked *before* `Request()` |
| Tool disabling | `agent/registry.go:151` | Exists, hard-denies disabled tools |

**Key finding**: The only missing piece is (a) a way for the TUI to toggle auto-approve on the current session, (b) a way to remove auto-approve (the `sync.Map` has no delete path), and (c) a status bar indicator.

### Permission Flow With Auto-Approve

```
Tool Call
  ↓
1. registry.EvaluatePermission(agentID, toolName, input)
   ├─ Tool disabled? → ActionDeny (UNCHANGED — auto-approve cannot override)
   ├─ Agent permission = "deny"? → ActionDeny (UNCHANGED)
   ├─ Global permission = "deny"? → ActionDeny (UNCHANGED)
   ├─ Any rule = "allow"? → ActionAllow (UNCHANGED)
   └─ Falls through to "ask"? → ActionAsk
  ↓
2. Tool calls permission.Request()
   ├─ IsAutoApproveSession? → return true (AUTO-APPROVED)
   └─ Otherwise → show dialog, wait for user
```

**Important**: `ActionDeny` is returned from `registry.EvaluatePermission()` *before* `Request()` is ever called. Tools that receive `ActionDeny` return `ErrorPermissionDenied` immediately — they never reach the permission service. This means auto-approve cannot override deny rules. This is already the correct behavior by construction.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Activation mechanism | Built-in `/auto-approve` slash command | Consistent with existing command pattern (`/compact`, `/agents`). Easier to discover than a keybinding. TUI-only — non-interactive mode already has auto-approve. |
| Toggle behavior | On/off toggle on the same command | Simpler than separate `/auto-approve-on` and `/auto-approve-off`. Status bar makes current state obvious. |
| Scope | Current session only | Resets when user creates a new session. Mirrors non-interactive behavior. No persistence across sessions. |
| Subagent inheritance | Yes, inherit auto-approve | Already implemented in `agent-tool.go:146`. If the parent session is auto-approved, subagent task sessions are too. |
| Deny override | No | By construction. `ActionDeny` from registry never reaches `Request()`. No code change needed. |
| Status bar indicator | Show "auto-approve" badge when active | Follows the pattern of scroll-lock and agent name indicators. |
| CLI flag | `--auto-approve` for TUI startup | Lets users start the TUI with auto-approve pre-enabled on the first session. Non-interactive mode already auto-approves, so the flag only affects interactive TUI. |
| Tool call badge | Small "auto" badge on auto-approved calls | Gives visual feedback in the message stream so users can distinguish auto-approved calls from pre-allowed ones. |
| Command name | `/auto-approve` | Descriptive, clear, discoverable. Safety-relevant toggles benefit from explicit naming over brevity. |

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│ /auto-approve command (TUI)                              │
│  └── Toggles auto-approve on current session ID          │
└──────────────────────────────────────────────────────────┘
        │
        ▼  calls
┌──────────────────────────────────────────────────────────┐
│ permission.Service                                       │
│  ├── AutoApproveSession(sessionID)  [existing]           │
│  ├── RemoveAutoApproveSession(sessionID)  [NEW]          │
│  └── IsAutoApproveSession(sessionID)  [existing]         │
└──────────────────────────────────────────────────────────┘
        │
        ▼  checked by
┌──────────────────────────────────────────────────────────┐
│ Request(ctx, opts)                                       │
│  ├── IsAutoApproveSession? → return true                 │
│  └── Otherwise → show dialog                             │
└──────────────────────────────────────────────────────────┘

Status Bar:
┌─────────────────────────────────────────────────────────────────────────┐
│ ctrl+h help │ tab agents │ auto-approve │ ctx: 12K, cst: $0.42 │ ... │
└─────────────────────────────────────────────────────────────────────────┘
                             ^^^^^^^^^^^^^^
                             NEW — shown only when active
```

TOGGLE FLOW:

```
Step 1: User types /auto-approve
────────────────────────────────
  → chatPage.resolveInlineSlash() matches command
  → Handler checks permission.IsAutoApproveSession(session.ID)
  → If NOT active: call permission.AutoApproveSession(session.ID)
  → If active: call permission.RemoveAutoApproveSession(session.ID)
  → Publish AutoApproveChangedMsg{Active: bool} to TUI
  → Status bar updates indicator
  → Info message: "Auto-approve enabled" / "Auto-approve disabled"

Step 2: Agent makes tool call (while active)
────────────────────────────────────────────
  → registry.EvaluatePermission() → ActionDeny or ActionAsk
  → If ActionDeny: blocked as usual (agent config respected)
  → If ActionAsk: Request() → IsAutoApproveSession → true → approved silently

Step 3: User creates new session
────────────────────────────────
  → New session ID, not in autoApproveSessions map
  → Auto-approve is off for the new session
  → Status bar indicator disappears
```

## Implementation Plan

### Phase 1: Permission Service Extension

- [ ] **1.1** Add `RemoveAutoApproveSession(sessionID string)` to `permission.Service` interface — deletes from `autoApproveSessions` sync.Map
- [ ] **1.2** Implement `RemoveAutoApproveSession` in `permissionService` — call `s.autoApproveSessions.Delete(sessionID)`
- [ ] **1.3** Add unit test: toggle on → `IsAutoApproveSession` returns true → toggle off → returns false

### Phase 2: Built-in Slash Command

- [ ] **2.1** Add `/auto-approve` to `buildCommands()` in `tui.go` with a Handler that toggles auto-approve on the current session
- [ ] **2.2** Add `"auto-approve"` to `tuiOnlyCommands` map in `slashcmd/resolve.go`
- [ ] **2.3** Define `AutoApproveChangedMsg{Active bool}` message type (in `core/status.go` or a shared location)
- [ ] **2.4** The handler needs access to `app.Permissions` and the current session ID — wire through the command handler or use a closure over `app`

### Phase 3: Status Bar Indicator

- [ ] **3.1** Add `autoApproveActive bool` field to `statusCmp`
- [ ] **3.2** Handle `AutoApproveChangedMsg` in `statusCmp.Update()` — set the flag
- [ ] **3.3** In `statusCmp.View()`, render an "auto-approve" widget (styled distinctively, e.g., warning color) between the agent hint and scroll lock widgets when active
- [ ] **3.4** Account for the widget width in the `availableWidth` calculation

### Phase 4: Session Change Handling

- [ ] **4.1** When a new session is selected or created (`SessionSelectedMsg`, `SessionClearedMsg`), check `IsAutoApproveSession` for the new session and update the status bar flag accordingly
- [ ] **4.2** Verify subagent inheritance still works — write a test that enables auto-approve on parent session, spawns a task session, and confirms the child is also auto-approved (this should already pass given existing code in `agent-tool.go:146`)

### Phase 5: Auto-Approved Tool Call Badge

- [ ] **5.1** When `Request()` auto-approves (returns true via `IsAutoApproveSession`), annotate the permission response or tool call context so the TUI knows this call was auto-approved rather than pre-allowed
- [ ] **5.2** In the message/tool-call rendering component, check for the auto-approved annotation and render a small badge (e.g., "auto" in a muted/warning style) next to the tool call header
- [ ] **5.3** Ensure the badge does not appear for `ActionAllow` calls (those were explicitly configured, not auto-approved)

### Phase 6: CLI Flag

- [ ] **6.1** Add `--auto-approve` flag to the root command in `cmd/root.go` (or wherever CLI flags are defined)
- [ ] **6.2** When the flag is set and the TUI starts, call `app.Permissions.AutoApproveSession(sess.ID)` on the initial session and emit `AutoApproveChangedMsg{Active: true}` so the status bar reflects it immediately
- [ ] **6.3** Ensure the flag has no effect in non-interactive mode (already auto-approved) — either ignore silently or log a debug message
- [ ] **6.4** Update README and `--help` text to document the flag

### Phase 7: Documentation

- [ ] **7.1** Add `/auto-approve` to the list of built-in commands in docs
- [ ] **7.2** Document the `--auto-approve` CLI flag in README and CLI help
- [ ] **7.3** Add a section on auto-approve behavior in the permissions documentation, explaining: what it does, what it doesn't override (deny rules, disabled tools), scope (per-session), and how to activate (slash command or CLI flag)

## Edge Cases

### Agent-Level Deny Still Enforced

1. User enables auto-approve on session
2. Agent config has `permission.bash.{"rm -rf *": "deny"}`
3. Agent attempts `rm -rf /`
4. `registry.EvaluatePermission()` returns `ActionDeny`
5. Tool returns `ErrorPermissionDenied` — never reaches `Request()`
6. Expected: command is blocked despite auto-approve

### Disabled Tool Still Blocked

1. User enables auto-approve
2. Agent has `tools.bash = false`
3. Agent somehow produces a bash tool call
4. `IsToolEnabled()` returns false → `ActionDeny`
5. Expected: tool call is blocked

### Session Switch While Active

1. User enables auto-approve on session A
2. User creates or switches to session B
3. Session B is a new ID — not in `autoApproveSessions`
4. Expected: auto-approve is off for session B, status bar reflects this
5. User switches back to session A
6. Expected: auto-approve is still on for session A, status bar reflects this

### Subagent Inheritance

1. User enables auto-approve on session
2. Agent spawns a subagent task → creates child session
3. `agent-tool.go:146` checks parent session and propagates auto-approve
4. Expected: child session is also auto-approved
5. This already works — no new code needed

### Compact Session

1. User enables auto-approve on session A
2. User runs `/compact` → summarization runs in-place on the same session
3. Session ID does not change — `autoApproveSessions` map still has the entry
4. Expected: auto-approve survives compaction naturally, no propagation needed
5. Same behavior in non-interactive mode (`performSynchronousCompaction` also keeps the session ID)

## Resolved Questions

1. **Session compact carry-over**: Non-issue. Compaction operates in-place on the same session ID (both TUI `Summarize()` and non-interactive `performSynchronousCompaction()`). The `autoApproveSessions` map entry survives naturally.

2. **Visual distinction for auto-approved calls**: Yes — add a small "auto" badge on tool calls that were auto-approved. Helps users see what ran without prompting.

3. **CLI flag**: Yes — add `--auto-approve` flag. When passed, the TUI starts with auto-approve enabled on the first session. Update docs (README, help text) accordingly.

4. **Naming**: `/auto-approve` — descriptive and clear. Safety-relevant toggles benefit from explicit naming.

## Success Criteria

- [ ] `/auto-approve` toggles auto-approve on the current TUI session
- [ ] Status bar shows an indicator when auto-approve is active
- [ ] `deny` rules in agent config are still enforced (write a test)
- [ ] Disabled tools (`tools.bash = false`) are still blocked (write a test)
- [ ] Subagent task sessions inherit auto-approve from parent
- [ ] New sessions start without auto-approve
- [ ] `/auto-approve` in non-interactive mode returns `ErrTUIOnly`
- [ ] Toggling off actually stops auto-approving (the `RemoveAutoApproveSession` path)
- [ ] Auto-approved tool calls show a small "auto" badge in the message stream
- [ ] `opencode --auto-approve` starts TUI with auto-approve active on first session
- [ ] README and CLI help document both `/auto-approve` and `--auto-approve`

## References

- `internal/permission/permission.go` — Permission service, `AutoApproveSession`, `IsAutoApproveSession`, `Request()`
- `internal/permission/evaluate.go` — `EvaluateToolPermission`, `IsToolEnabled`, `ActionDeny`/`ActionAsk`/`ActionAllow`
- `internal/agent/registry.go:145` — `EvaluatePermission()` — deny/disable checked before `Request()`
- `internal/llm/agent/agent-tool.go:146` — Subagent auto-approve inheritance
- `internal/llm/agent/agent.go:1058` — `performSynchronousCompaction()` — in-place compaction, session ID preserved
- `internal/llm/agent/agent.go:1139` — `Summarize()` — TUI compaction, also in-place
- `internal/tui/components/core/status.go` — Status bar component, message handling, widget rendering
- `internal/tui/tui.go:981` — `buildCommands()` — where built-in slash commands are registered
- `internal/slashcmd/resolve.go:36` — `tuiOnlyCommands` map
- `internal/tui/page/chat.go:463` — `resolveInlineSlash()` — command execution flow
- `internal/app/app.go` — `App` struct, `Permissions` field accessible from TUI
- `cmd/root.go` — CLI flag definitions, where `--auto-approve` will be added
