---
name: sync-server-api
description: >
  Audit our Go fork's HTTP REST API and ACP implementation against the dax opencode
  TypeScript reference (anomalyco/opencode). Detects missing endpoints, type
  mismatches, and behavioural gaps. Use when adding features, reviewing PRs, or
  periodically confirming compatibility. Pass an optional focus area as argument
  (e.g., "session", "acp", "events").
user-invocable: true
argument-hint: "[focus-area]"
metadata:
  category: compatibility
  upstream: https://github.com/anomalyco/opencode
---

# Sync Server API & ACP with Dax OpenCode

## Purpose

Keep our Go fork's server (`internal/api/`) and ACP (`internal/acp/`) feature-compatible
with the dax opencode TypeScript reference. The rule is **extend, never break**: new
features are welcome, but existing endpoint contracts, field names, and event shapes
must stay compatible with `@opencode-ai/sdk/v2` clients and ACP clients like AionUI.

## When to use

- Before merging server/ACP changes — run a quick audit
- After upstream (dax opencode) gains new endpoints or events
- When a connected UI (OpenWork, AionUI) reports incompatibilities
- Periodically to catch drift

## Focus areas

If an argument is provided (`$ARGUMENTS`), narrow the audit to that area:
- `session` — session CRUD, status, fork, abort
- `message` — message listing, prompt sync/async, summarize
- `event` — SSE event types and payload shapes
- `config` — config/providers endpoints
- `permission` — permission request/reply flow
- `acp` — ACP JSON-RPC methods and notifications
- `types` — API response type shapes and JSON field names
- (empty) — full audit of everything

## Reference locations

### Our Go implementation

| Component | Path |
|-----------|------|
| HTTP server & routes | `internal/api/server.go` |
| Middleware | `internal/api/middleware.go` |
| Session handlers | `internal/api/handler_session.go` |
| Message/prompt handlers | `internal/api/handler_message.go` |
| SSE event handler | `internal/api/handler_event.go` |
| Config handler | `internal/api/handler_config.go` |
| Permission handler | `internal/api/handler_permission.go` |
| Agent handler | `internal/api/handler_agent.go` |
| Health handler | `internal/api/handler_health.go` |
| API response types | `internal/api/types.go` |
| Session converter | `internal/api/convert_session.go` |
| Message converter | `internal/api/convert_message.go` |
| Provider converter | `internal/api/convert_provider.go` |
| Permission converter | `internal/api/convert_permission.go` |
| Error helpers | `internal/api/errors.go` |
| ACP transport (NDJSON) | `internal/acp/transport.go` |
| ACP server loop | `internal/acp/server.go` |
| ACP protocol handler | `internal/acp/handler.go` |
| ACP protocol types | `internal/acp/types.go` |
| CLI serve command | `cmd/serve.go` |
| CLI acp command | `cmd/acp.go` |

### Dax opencode TypeScript reference

The upstream repo (`https://github.com/anomalyco/opencode`) must be available locally.
The shell block below auto-detects its path by searching sibling directories. If it
is not found, clone it next to this project (e.g., `git clone https://github.com/anomalyco/opencode ../opencode-dax`).

| Component | Path (under opencode-dax/) |
|-----------|---------------------------|
| OpenAPI spec (all endpoints) | `packages/sdk/openapi.json` |
| SDK v2 client | `packages/sdk/js/src/v2/client.ts` |
| HTTP server | `packages/opencode/src/server/server.ts` |
| HTTP handlers | `packages/opencode/src/server/routes/instance/httpapi/handlers/` |
| v2 handlers | `packages/opencode/src/server/routes/instance/httpapi/handlers/v2/` |
| SSE event handler | `packages/opencode/src/server/routes/instance/httpapi/handlers/event.ts` |
| ACP agent (full impl) | `packages/opencode/src/acp/agent.ts` |
| ACP session manager | `packages/opencode/src/acp/session.ts` |
| ACP types | `packages/opencode/src/acp/types.ts` |
| ACP runtime | `packages/opencode/src/acp/runtime.ts` |

## Audit procedure

### Step 1: Identify the scope

If `$ARGUMENTS` is provided, focus only on that area. Otherwise, audit all areas.

### Step 2: Compare endpoints

Read the dax opencode OpenAPI spec (`packages/sdk/openapi.json`) and compare against
our route registrations in `internal/api/server.go:registerRoutes()`.

For each endpoint in the SDK spec, classify as:

| Status | Meaning |
|--------|---------|
| **Implemented** | We have this endpoint and it matches the SDK contract |
| **Partial** | We have the endpoint but response shape or behaviour differs |
| **Missing (MVP)** | SDK clients actively call this; should be added |
| **Missing (Nice-to-have)** | Exists in dax but not used by OpenWork/AionUI |
| **Not applicable** | Workspace management, PTY, TUI control — out of scope |

### Step 3: Compare response types

For each implemented endpoint, verify our Go API types (`internal/api/types.go`)
match the SDK's expected response shapes:

1. Read the SDK client type definitions or the OpenAPI spec schemas
2. Read our Go struct tags and converter functions
3. Flag any differences in:
   - Field names (must be camelCase matching SDK)
   - Missing fields that SDK clients read
   - Extra fields (OK — extending is fine, removing is not)
   - Type mismatches (string vs number, etc.)

### Step 4: Compare SSE events

Read the dax event handler and compare against our `internal/api/handler_event.go`:

1. Event type strings (e.g., `message.created`, `session.updated`, `permission.asked`)
2. Event payload shapes
3. Special event types we might be missing (e.g., `message.part.delta`, `message.part.updated`)
4. Heartbeat mechanism

### Step 5: Compare ACP protocol

Read the dax ACP agent (`packages/opencode/src/acp/agent.ts`) and compare against
our `internal/acp/handler.go`:

1. JSON-RPC methods — which ones does dax implement that we don't?
2. Session update notification types — are we sending all the ones ACP clients expect?
3. Permission flow — does our flow match dax's?
4. Message framing — confirm we use NDJSON (newline-delimited), not Content-Length

### Step 6: Report findings

Produce a structured report:

```markdown
## Sync Audit: [focus area or "Full"]

### Endpoints
| Endpoint | Status | Notes |
|----------|--------|-------|
| ... | ... | ... |

### Type Mismatches
- [ ] `APIFoo.bar` — our type: X, SDK expects: Y

### Missing SSE Events
- [ ] `event.type` — description

### Missing ACP Methods
- [ ] `method/name` — description

### Recommendations
1. ...
2. ...
```

### Step 7: Implement fixes (if asked)

If the user asks to fix issues found in the audit:

1. **Only fix compatibility issues** — do not refactor unrelated code
2. **Extend, never break** — add missing fields/endpoints, don't change existing ones
3. **Match SDK field names exactly** — camelCase JSON tags must match
4. **Run `go build ./...`** after changes
5. **Run `make test`** to verify nothing broke

## Current implementation status

!`DAX=$(find "$(dirname "$(dirname "$(pwd)")")" -maxdepth 5 -name openapi.json -path "*/packages/sdk/*" 2>/dev/null | head -1 | sed 's|/packages/sdk/openapi.json||') && echo "=== Dax repo ===" && if [ -n "$DAX" ]; then echo "Found at: $DAX"; else echo "NOT FOUND — clone https://github.com/anomalyco/opencode as a sibling of this project"; fi && echo "" && echo "=== Our routes ===" && grep 'mux.HandleFunc' internal/api/server.go 2>/dev/null | sed 's/.*HandleFunc("//' | sed 's/".*//' && echo "" && echo "=== Our ACP methods ===" && grep 'case "' internal/acp/server.go 2>/dev/null | sed 's/.*case "//' | sed 's/".*//' && echo "" && echo "=== Dax SDK endpoint count ===" && if [ -n "$DAX" ]; then jq '[.paths | to_entries[] | .value | keys[] | select(. == "get" or . == "post" or . == "put" or . == "delete" or . == "patch")] | length' "$DAX/packages/sdk/openapi.json" 2>/dev/null | xargs -I{} echo "{} endpoints in SDK spec" || echo "(jq not available)"; else echo "(dax repo not available locally)"; fi`

## Known gaps (as of initial implementation)

These are documented gaps from the original implementation — they are intentional
trade-offs, not bugs. This list should be updated as gaps are closed:

1. **Message part deltas**: dax emits `message.part.delta` and `message.part.updated`
   SSE events for streaming. Our SSE emits full `message.updated` events from the
   pubsub broker. SDK clients that rely on part-level deltas won't get them.

2. **Per-message token counts**: our `APIMessageTokens` fields are zero-valued because
   the Go message service doesn't store per-message token breakdowns. Session-level
   totals are accurate.

3. **Session fork**: dax supports `POST /session/{id}/fork`. We don't implement this
   yet. ACP `forkSession` method is also not implemented.

4. **Config update**: dax supports `PATCH /config`. We only have `GET /config`.

5. **Permission list**: dax has `GET /permission` to list pending permissions. We only
   have the reply endpoint.

6. **Command/skill endpoints**: dax has `GET /command` and `GET /skill`. We don't
   expose these via the API yet.

7. **MCP management**: dax has `POST /mcp`, `DELETE /mcp/{name}`, etc. We don't
   expose MCP management via the API.

8. **ACP setSessionModel / setSessionMode / setSessionConfigOption**: dax implements
   these ACP methods for model/mode switching during a session. We don't yet.

## Rules

- NEVER remove or rename existing API fields, endpoints, or event types
- NEVER change the JSON serialization format of existing response types
- New endpoints or fields are always safe to add
- When in doubt about an SDK contract, read the OpenAPI spec — it is the source of truth
- When in doubt about ACP behaviour, read `packages/opencode/src/acp/agent.ts` in dax
- Always verify changes compile: `go build ./...`
- Always run tests after changes: `make test`
