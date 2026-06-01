# API Improvements — OpenRouter Compatibility + Low-Hanging SDK Parity

**Date**: 2026-06-01
**Status**: Implemented (pending end-to-end verification against openwork router)
**Author**: AI-assisted

## Overview

Two related groups of changes to our HTTP API:

1. **OpenRouter compatibility** — the openwork `opencode-router` bridge (`apps/opencode-router/src/bridge.ts`) issues SDK calls that either drop fields silently or hit endpoints we never implemented. Fix the calls it actually makes so the bridge works against our fork instead of upstream dax only.
2. **Low-hanging SDK parity** — a handful of endpoints from the dax `@opencode-ai/sdk/v2` surface that we can stub or implement in a few lines each, because the backing service already exists. These unblock other potential SDK consumers without committing to full parity.

**Explicitly out of scope:** per-message `model` / `agent` overrides on prompt (deferred — has broader cache-prefix and session-history implications and conflicts with how our agent registry binds a model per session).

## Motivation

### Current State — what the openwork router actually calls

Tracing `apps/opencode-router/src/bridge.ts` against the SDK in `node_modules/@opencode-ai/sdk/dist/v2/gen/sdk.gen.js`:

| Bridge call | Wire endpoint | Fork status |
|---|---|---|
| `global.health()` | `GET /global/health` | ✅ ok |
| `event.subscribe()` | `GET /event` (SSE) | ✅ ok |
| `session.create({ title, permission })` | `POST /session` body `{title, permission, ...}` | ⚠️ `permission` silently dropped |
| `session.prompt({ sessionID, parts, model?, agent? })` | `POST /session/{id}/message` | ⚠️ `model`/`agent` silently dropped (deferred — see Non-Goals) |
| `permission.respond({ sessionID, permissionID, response })` | `POST /session/{sessionID}/permissions/{permissionID}` body `{response: "once"\|"always"\|"reject"}` | ❌ endpoint missing |
| `question.reply` / `question.reject` | `POST /question/{id}/reply` / `/reject` | ✅ ok |

The endpoint we DO have — `POST /permission/{requestID}/reply` with body `{allow: bool}` — is shaped differently from any current SDK call. The SDK's `permission.reply(...)` uses the same URL but body `{reply: "once"\|"always"\|"reject", message?}`, and the SDK's (deprecated but still-used) `permission.respond(...)` uses the session-scoped URL `/session/{sessionID}/permissions/{permissionID}`.

#### Consequence

- In `permissionMode: "deny"` (router config), the bridge subscribes to `permission.asked` events and calls `client.permission.respond({response: "reject"})`. We return 404. The agent run hangs until the permission times out or the user kills the session.
- In `permissionMode: "allow"` (router config), the bridge passes `permission: [{permission:"*", pattern:"*", action:"allow"}]` to `session.create`. We drop it. Then the bridge still relies on the `permission.respond` path with `response: "always"` as a fallback if any permission ever leaks through — same 404.

### What is missing from our fork relative to dax SDK

Comparing `internal/api/server.go` routes against `packages/sdk/openapi.json`. The dax surface has 100+ endpoints. We deliberately skipped most of them in the original server-API spec (see `20260517T120000-server-api-and-acp.md`). A small subset is cheap because the backing service exists:

| Endpoint | Backing | Why cheap |
|---|---|---|
| `POST /global/dispose` | none | Alias of `/instance/dispose` (already a no-op stub) |
| `GET /session/{sessionID}/children` | `Sessions.ListChildren` | Already exists |
| `GET /skill` | `skill.All()` | Already exists |
| `POST /log` | `logging` package | Trivial passthrough |
| `PATCH /session/{sessionID}` accepts `permission` | reuses #1 below | Same code path as session create |

## Goals

1. Make the openwork router work end-to-end against our fork in both `permissionMode: "allow"` and `"deny"`.
2. Honor `permission` on session create as a coarse-grained allow/ask mode (binary, mapped to the existing `AutoApproveSession`).
3. Add the deprecated `POST /session/{sessionID}/permissions/{permissionID}` endpoint alongside our existing `POST /permission/{requestID}/reply` so both SDK code paths work.
4. Implement the cheap parity endpoints listed above.

## Non-Goals

- **Per-prompt `model` / `agent` override on `POST /session/{id}/message`.** Our agent loop binds a single model per agent instance (`agent.go:1561`), and switching models mid-session would (a) invalidate the prompt cache prefix, (b) require threading override state through `agent.Run(...)` and downstream, (c) conflict with how our session history is laid down. Defer until we have a real use case beyond router slash commands.
- **Rich permission rule evaluation** (`{permission, pattern, action}` matching à la dax). Our permission service is binary per session. We map only the two cases the router actually emits: full-allow (`AutoApproveSession`) and full-deny (no-op — router rejects via events). Real rule evaluation is out of scope.
- **Multi-workspace, share, fork, revert, init, shell, pty, sync, tui/control, vcs, find, file/content, lsp, formatter.** Same reasoning as in the original server-API spec — TUI/IDE-extension surface, no router need.

## Design

### Endpoint-by-endpoint changes

#### 1. `POST /session` — accept `permission` rules

Extend `APISessionCreateRequest` in `internal/api/types.go`:

```go
type APISessionCreateRequest struct {
    Title      string               `json:"title"`
    Permission []APIPermissionRule  `json:"permission,omitempty"`
}

type APIPermissionRule struct {
    Permission string `json:"permission"` // e.g. "*", "bash", "edit"
    Pattern    string `json:"pattern"`    // e.g. "*", "git *"
    Action     string `json:"action"`     // "allow" | "deny" | "ask"
}
```

In `handler_session.go:handleSessionCreate`, after `Sessions.Create(...)`:

```go
if shouldAutoApprove(req.Permission) {
    s.app.Permissions.AutoApproveSession(session.ID)
}
```

Where `shouldAutoApprove` returns true iff the ruleset contains a wildcard allow (`permission == "*" && pattern == "*" && action == "allow"`). Any other shape is silently ignored — matches our binary permission model.

**Why not full rule matching:** the permission service operates on opaque `PermissionRequest` (tool name + path + action), and there is no rule engine. Adding one is a larger change with no current consumer.

#### 2. `POST /session/{sessionID}/permissions/{permissionID}` — new endpoint

Add to `handler_permission.go`:

```go
type APIPermissionRespond struct {
    Response string `json:"response"` // "once" | "always" | "reject"
}

func (s *Server) handlePermissionRespond(w http.ResponseWriter, r *http.Request) {
    permissionID := r.PathValue("permissionID")
    var req APIPermissionRespond
    if err := readJSON(r, &req); err != nil { ... }

    pr := permission.PermissionRequest{ID: permissionID}
    switch req.Response {
    case "always", "once":
        // We use Grant for both. GrantPersistant appends to sessionPermissions
        // for future dedup matching, but our PermissionRequest has only the ID
        // populated here (we don't have ToolName/Action/SessionID/Path at the
        // reply endpoint) — so a persistent entry would never match anything
        // and would leak memory on long-running servers. Treat both as one-shot.
        s.app.Permissions.Grant(pr)
    case "reject":
        s.app.Permissions.Deny(pr)
    default:
        writeError(w, http.StatusBadRequest, "invalid response")
        return
    }
    writeJSON(w, http.StatusOK, true)
}
```

Wired in `server.go`:

```go
mux.HandleFunc("POST /session/{sessionID}/permissions/{permissionID}", s.handlePermissionRespond)
```

The `sessionID` path segment is accepted but unused — our permission service identifies requests by `ID` alone (the SDK includes both for routing on the dax side; we don't need it).

#### 3. `POST /permission/{requestID}/reply` — accept richer body

Change `APIPermissionReply` to accept either the old `{allow: bool}` or the new SDK shape `{reply: "once"|"always"|"reject", message?: string}`. Keep backward compat for the existing OpenWork integration that still sends `allow`.

```go
type APIPermissionReply struct {
    Allow   *bool   `json:"allow,omitempty"`   // legacy
    Reply   string  `json:"reply,omitempty"`   // new SDK
    Message string  `json:"message,omitempty"` // optional, not used today
}
```

Handler: if `Reply` is set, map via the same switch as #2. Otherwise fall back to `Allow`. Both reach the same `permission.Service` methods.

#### 4. `PATCH /session/{sessionID}` — accept `permission`

Same field on `APISessionUpdateRequest`. Same `shouldAutoApprove` call. Lets clients flip a session into auto-approve after creation. Cheap because the logic is shared with #1.

**Important — don't clobber `Title`:** the current handler unconditionally does `session.Title = req.Title` and then calls `Sessions.Save`. A permission-only PATCH would wipe the title. Change `Title` to `*string` and only assign / save when non-nil:

```go
type APISessionUpdateRequest struct {
    Title      *string              `json:"title,omitempty"`
    Permission []APIPermissionRule  `json:"permission,omitempty"`
}

// handler:
if req.Title != nil {
    session.Title = *req.Title
    if _, err := s.app.Sessions.Save(r.Context(), session); err != nil { ... }
}
if shouldAutoApprove(req.Permission) {
    s.app.Permissions.AutoApproveSession(sessionID)
}
```

If neither field is set, the handler is a no-op that returns the existing session — same observable behavior as a redundant PATCH.

#### 5. `POST /global/dispose` — alias of `/instance/dispose`

In `server.go`:

```go
mux.HandleFunc("POST /global/dispose", s.handleInstanceDispose)
```

No new handler — point both routes at the existing stub. The SDK v2 calls `/global/dispose`; we already documented in `handler_health.go:21` that `/instance/dispose` is the dax-compat path. Both reach the same no-op.

#### 6. `GET /session/{sessionID}/children`

In `handler_session.go`:

```go
func (s *Server) handleSessionChildren(w http.ResponseWriter, r *http.Request) {
    sessionID := r.PathValue("sessionID")
    children, err := s.app.Sessions.ListChildren(r.Context(), sessionID)
    if err != nil { ... }
    writeJSON(w, http.StatusOK, ConvertSessionsWithDir(children, resolveDirectory(r)))
}
```

`Sessions.ListChildren` already exists (`internal/session/session.go:42`).

#### 7. `GET /skill`

New `handler_skill.go`:

```go
type APISkill struct {
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Location    string `json:"location"`
    Content     string `json:"content"`
}

func (s *Server) handleSkillList(w http.ResponseWriter, r *http.Request) {
    skills := skill.All()
    out := make([]APISkill, 0, len(skills))
    for _, sk := range skills {
        out = append(out, APISkill{
            Name:        sk.Name,
            Description: sk.Description,
            Location:    sk.Location,
            Content:     sk.Content,
        })
    }
    writeJSON(w, http.StatusOK, out)
}
```

Matches dax's `/skill` response shape (`name`, `description`, `location`, `content`).

#### 8. `POST /log`

New `handler_log.go`:

```go
type APILogRequest struct {
    Service string         `json:"service"`
    Level   string         `json:"level"` // "debug" | "info" | "warn" | "error"
    Message string         `json:"message"`
    Extra   map[string]any `json:"extra,omitempty"`
}

func (s *Server) handleLogWrite(w http.ResponseWriter, r *http.Request) {
    var req APILogRequest
    if err := readJSON(r, &req); err != nil { ... }
    args := []any{"service", req.Service}
    for k, v := range req.Extra { args = append(args, k, v) }
    switch req.Level {
    case "debug": logging.Debug(req.Message, args...)
    case "info":  logging.Info(req.Message, args...)
    case "warn":  logging.Warn(req.Message, args...)
    case "error": logging.Error(req.Message, args...)
    default:      logging.Info(req.Message, args...)
    }
    writeJSON(w, http.StatusOK, true)
}
```

## Implementation Plan

Single-PR scope. Two phases:

- **Phase A — openrouter-critical** (#1, #2). Without these, the bridge hangs or silently drops permission state. Land these first.
- **Phase B — parity / forward-compat** (#3–#8). Cheap and useful; not blocking the bridge today. `#3` was originally grouped with the unblocking trio, but on re-check the bridge only calls `permission.respond` (→ #2), never `permission.reply` (→ #3). So #3 is forward-parity for future SDK consumers, not openrouter-critical.

### Phase A — openrouter-critical

- [x] **#1 Session create honors `permission`** — `types.go` (`APIPermissionRule` added; `APISessionCreateRequest.Permission`), `handler_session.go` (`shouldAutoApprove` helper + call in `handleSessionCreate`). Covered by `TestShouldAutoApprove` in `handler_session_test.go`.
- [x] **#2 New `POST /session/{sessionID}/permissions/{permissionID}` endpoint** — `handler_permission.go` (`applyPermissionAction` helper + `handlePermissionRespond`), route wired in `server.go`. Covered by `TestApplyPermissionAction_*` in `handler_permission_test.go`.

### Phase B — parity / forward-compat

- [x] **#3 `POST /permission/{requestID}/reply` accepts `{reply}`** — `APIPermissionReply` widened (`Allow *bool`, `Reply`, `Message`); `handlePermissionReply` prefers `Reply` when set, falls back to `Allow`, 400s when neither is present. Same `applyPermissionAction` tests cover the verb mapping.
- [x] **#4 `PATCH /session/{sessionID}` accepts `permission`** — `APISessionUpdateRequest.Title` is now `*string`; `handleSessionUpdate` only calls `Sessions.Save` when `Title != nil`, so permission-only PATCH preserves the existing title.
- [x] **#5 `POST /global/dispose` route alias** — added in `server.go` next to `/instance/dispose`, both pointing at `handleInstanceDispose`.
- [x] **#6 `GET /session/{sessionID}/children`** — `handleSessionChildren` wraps `Sessions.ListChildren`; route wired.
- [x] **#7 `GET /skill`** — new `handler_skill.go` with `APISkill` + `handleSkillList` wrapping `skill.All()`; route wired.
- [x] **#8 `POST /log`** — new `handler_log.go`, level-dispatches to `logging.Debug/Info/Warn/Error`, flattens `extra` map into structured-log args.

### Verification

- [x] **`make test`** — green (full suite, 19.3% coverage; new tests pass: `TestShouldAutoApprove`, `TestApplyPermissionAction_*`).
- [x] **`go build ./...`** — clean compile.
- [ ] **End-to-end verification against openwork router** — run the bridge against our fork with `permissionMode: "allow"`, confirm a prompt completes; with `permissionMode: "deny"`, confirm a tool-using prompt fails cleanly via the new `/session/.../permissions/...` endpoint and does not hang. **Deferred — manual step; not blocking the merge of the API changes themselves.**
- [ ] **Regenerate `opencode-schema.json`** — N/A, no new config knobs were added (only HTTP request/response types).

## Technical Considerations

### Permission ID lookup vs session-scoped path

Dax's `/session/{sessionID}/permissions/{permissionID}` exists because their permission service is per-session. Ours is global-keyed-by-ID. Accepting `sessionID` in the path and ignoring it is the simplest compatibility shim. If we ever shard permissions by session, this is the right place to add the lookup.

### Backward compatibility of `/permission/{requestID}/reply`

The OpenWork app currently in production calls this with `{allow: bool}`. Making `Allow` a `*bool` and adding `Reply` keeps both shapes working — important because we don't control the OpenWork release cadence. New SDK consumers get the canonical `{reply}` body; older ones keep working.

### `shouldAutoApprove` is intentionally narrow

We only honor `[{permission:"*", pattern:"*", action:"allow"}]` — anything more specific is silently ignored. This is documented in the handler comment as "binary auto-approve; rule matching is not implemented." Callers that need granular control fall through to the normal ask flow and respond via events. Real rule evaluation is a separate, larger spec.

### `POST /log` does not enforce auth shape

Our auth middleware already gates everything except `/global/health`. `/log` accepts whatever JSON the client sends (within `readJSON` size limits) and writes it to the server log. We do NOT sanitize `extra` — anything serializable goes into the structured log. Acceptable for a same-trust deployment; document the risk in the handler comment.

### No new dependencies

Everything reuses existing services: `app.Sessions`, `app.Permissions`, `skill.All()`, `logging.*`. No new packages or generated code.

## Open Questions

- Should `shouldAutoApprove` also honor `[{permission:"*", pattern:"*", action:"deny"}]` by adding a `RejectAllSession(sessionID)` to the permission service? Today the router enforces deny by intercepting `permission.asked` events and replying `reject`, so the server side never needs to know. Leaving this out for now; revisit if a non-router consumer asks for it.
- Do we want `POST /log` to publish a server-sent event so dashboards can tail it? Not in this spec — adds machinery for a single hypothetical consumer.

## Review / Summary of Changes

**Files touched:**

- `internal/api/types.go` — added `APIPermissionRule`, `APIPermissionRespond`; widened `APIPermissionReply` and `APISessionCreateRequest`/`APISessionUpdateRequest`.
- `internal/api/handler_session.go` — `shouldAutoApprove` helper, `handleSessionChildren`, `handleSessionCreate` honors `Permission`, `handleSessionUpdate` no longer clobbers `Title` and honors `Permission`.
- `internal/api/handler_permission.go` — `applyPermissionAction` helper, `handlePermissionRespond`, dual-shape body in `handlePermissionReply`.
- `internal/api/handler_skill.go` — new file; `APISkill` + `handleSkillList`.
- `internal/api/handler_log.go` — new file; `APILogRequest` + `handleLogWrite`.
- `internal/api/server.go` — wired six new/aliased routes: `GET /session/{id}/children`, `POST /session/{id}/permissions/{permissionID}`, `POST /global/dispose`, `GET /skill`, `POST /log`.
- `internal/api/handler_session_test.go` — new; `TestShouldAutoApprove` table test (7 cases).
- `internal/api/handler_permission_test.go` — new; `TestApplyPermissionAction_*` exercise the live `permission.Service` round-trip for `once`/`always`/`reject`/invalid.

**What did NOT change (and why):**

- `permission.Service` interface — unchanged. We chose to map `always` to `Grant` rather than `GrantPersistant` (see #2 design note) precisely to avoid widening the interface or introducing memory leaks.
- `session.Service` interface — unchanged. `ListChildren` already existed.
- `agent.Service.Run` — unchanged. Per-prompt `model`/`agent` override is a Non-Goal of this spec.
- Auth middleware — unchanged. New routes inherit the same posture as everything else (gated when `OPENCODE_SERVER_PASSWORD` is set, open otherwise).

**Notable deviations from the original spec text:**

- Spec originally proposed `GrantPersistant` for `response: "always"`; review caught that with only `ID` populated the entry would leak into `sessionPermissions` and never match anything. Final implementation uses `Grant` for both `once` and `always`. Spec was patched before implementation.
- Spec originally grouped #3 with the openrouter-critical trio; review confirmed the bridge only calls `permission.respond` (→ #2), never `permission.reply` (→ #3). Spec re-organized into Phase A (#1, #2) and Phase B (#3–#8). All phases shipped together in this PR; the phase split documents priority, not separate PRs.

