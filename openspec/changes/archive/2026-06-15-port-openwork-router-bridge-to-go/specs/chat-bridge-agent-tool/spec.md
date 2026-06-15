## ADDED Requirements

### Requirement: router_send tool conditionally registered

The opencode tool registry SHALL register a `router_send` tool iff BOTH conditions are met at startup:

1. The agent being constructed has `mode: "agent"` (NOT `mode: "subagent"`).
2. `cfg.Router != nil` AND at least one channel has at least one enabled identity.

If either condition fails, the tool MUST NOT appear in the agent's tool list — there must be no possibility for the agent to "try and fail" on a misconfigured environment.

The conditional registration runs once at agent construction time. If the config changes mid-flight (e.g., a new identity is added via `POST /router/identities/slack`), already-running agents do NOT gain the tool — it appears only in agents constructed after the config change. This matches the way other tool-registration decisions work in opencode.

#### Scenario: Agent mode with router configured

- **WHEN** an agent with `mode: "agent"` is constructed in a process where `cfg.Router.Channels.Slack.Apps` has one enabled app
- **THEN** the agent's tool list includes `router_send`

#### Scenario: Subagent never gets the tool

- **WHEN** a subagent with `mode: "subagent"` is constructed in the same process
- **THEN** the agent's tool list does NOT include `router_send`, even if the router is configured

#### Scenario: Router not configured

- **WHEN** `cfg.Router == nil` or all channels are disabled
- **THEN** no agent in this process gets `router_send` in its tool list

### Requirement: Tool description enumerates configured channels

The `router_send` tool's description SHALL be built at registration time from a snapshot of `cfg.Router`. The description MUST enumerate:

- Each configured channel (`telegram`, `slack`, `mattermost`).
- For each channel, each enabled identity by ID.
- For each identity, the platform-specific peer-id format (numeric `chat_id` for Telegram; `D<id>` / `U<id>` / `C<id>[|<ts>]` for Slack; `<channelId>[|<rootPostId>]` for Mattermost).
- The list of currently-bound peers from `bridge_sessions` (snapshot at registration time) so the agent knows which conversations it can address.

The description format MUST be agent-friendly natural text, not a JSON dump — the agent reads the description as part of its system context.

#### Scenario: Description reflects two channels and two identities

- **WHEN** the tool is registered in a process where Slack has identities `default` and `secondary` (both enabled) and Telegram has identity `default` (enabled)
- **THEN** the tool description lists all three (slack:default, slack:secondary, telegram:default) with their peer-id format

#### Scenario: Description lists currently bound peers

- **WHEN** the tool is registered and `bridge_sessions` contains three rows for the current process's session-bindings
- **THEN** the description includes those bound peers under "currently bound" so the agent knows where it can address messages without learning a new peer ID

### Requirement: router_send tool input schema

The `router_send` tool SHALL accept the following input parameters:

| Field | Type | Required | Notes |
|---|---|---|---|
| `channel` | string | yes | `"telegram"` \| `"slack"` \| `"mattermost"` |
| `identity` | string | yes | matches an enabled identity ID for the channel |
| `peerId` | string | yes | platform-specific format (see adapters spec) |
| `text` | string | yes | message body |
| `mention` | string | no | optional ping handle prepended to the first message |
| `files` | array of paths | no | local file paths to attach; subject to per-platform size limits |

The tool MUST reject input with 4xx-equivalent errors (returned as tool error responses) for: unknown channel, unknown identity, malformed peerId, oversize file, or empty text.

#### Scenario: Valid call delivers to peer

- **WHEN** the agent calls `router_send` with `{channel: "slack", identity: "default", peerId: "C0DEF456", text: "Build green"}`
- **THEN** the bridge delivers the message to the Slack channel; the tool's response reports `{delivered: true}`

#### Scenario: Unknown identity rejected

- **WHEN** the agent calls `router_send` with `{channel: "slack", identity: "ghost", ...}`
- **THEN** the tool returns an error response (no message is sent) listing the available identities for the channel

#### Scenario: Oversize file rejected before upload

- **WHEN** the agent calls `router_send` with a 100 MB file via the Telegram adapter (50 MB platform limit)
- **THEN** the tool returns an error response without attempting the upload

### Requirement: In-process bridge.Service.Send invocation

The `router_send` tool implementation MUST call `bridge.Service.Send(...)` directly in-process. It MUST NOT issue an HTTP request to `/router/send` (no self-loopback). The two paths converge at the same `bridge.Service.Send` method — only the call site differs.

This is symmetric with how other in-process opencode services are called from tools (e.g., `permission.Service`, `question.Service`).

#### Scenario: No HTTP loopback observable

- **WHEN** the `router_send` tool is invoked
- **THEN** no inbound HTTP request appears in the API server's request log; the bridge's adapter goroutine is invoked directly

#### Scenario: External /router/send call uses same code path

- **WHEN** an external orchestrator calls `POST /router/send`
- **THEN** the API handler converts the request to a `bridge.Service.Send` call, exercising the same delivery code path as the in-process tool

### Requirement: Tool response includes per-peer delivery status

When the request targets a single peer the response is `{delivered: bool, error?: string}`. The tool MUST NOT report partial success — single-peer delivery is binary. The agent's prompt can rely on `delivered: true` meaning "the message reached the platform's API".

(Note: this differs from the `POST /router/send` external API which always reports an array — internal-tool path simplifies for the single-peer common case. Multi-peer delivery within a single tool call is NOT in scope for v1; agents wanting fan-out should make multiple tool calls or trigger a session binding via the orchestrator.)

#### Scenario: Successful delivery

- **WHEN** the tool delivers successfully
- **THEN** the response is `{"delivered": true}`

#### Scenario: Delivery failure surfaces error

- **WHEN** the platform API returns an error (DM closed, channel deleted)
- **THEN** the response is `{"delivered": false, "error": "<redacted reason>"}` — the error string is safe to include in agent context (no tokens, no peerIds beyond what the agent already knows)

### Requirement: No allowlist enforcement in v1

The tool MUST NOT enforce a peer allowlist in v1. The peer `cfg.Router.AgentPeerAllowlist` field MAY be defined in the config schema as a forward-compat hook but MUST NOT be checked by the tool implementation in this change. c2-agent's single-tenant k8s pattern places the trust boundary at the orchestrator level (which pods get which `cfg.Router` content), not at tool dispatch.

#### Scenario: Agent sends to a non-bound peer

- **WHEN** the agent calls `router_send` with a configured channel/identity but a peerId that is NOT in `bridge_sessions` (never been bound)
- **THEN** the tool dispatches normally; the bridge attempts delivery; the result is whatever the platform reports (success if the peer ID is valid; failure if not)
