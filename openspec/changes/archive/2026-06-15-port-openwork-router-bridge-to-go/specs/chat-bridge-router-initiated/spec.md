## ADDED Requirements

### Requirement: Bridge.Service.Bind in-process API

The `internal/bridge` package SHALL expose a `Service.Bind(ctx, sessionId, peers []PeerRef) error` method that associates one or more peers with an opencode session. The method MUST be callable both from in-process callers (the flow engine, the in-process question/permission flows) and indirectly via the `POST /router/bind` HTTP endpoint (which calls this method internally).

`Bind` MUST:

1. Validate every `PeerRef` (recognized channel, configured identity, syntactically plausible peerId).
2. Resolve user-id forms to DM channels (Slack `U*` → `conversations.open`; Mattermost user ID → `channels/direct`) BEFORE persisting the row.
3. Upsert each `(project_id, channel, identity_id, peer_id)` row in `bridge_sessions` with `session_id` set to the provided session. If the row exists with a different `session_id`, the operation MUST replace it (re-binding is supported, e.g., for escalation).
4. Start the per-`sessionId` dispatcher goroutine if it isn't already running.
5. Return aggregated per-peer resolution results so the caller can log which peers were resolved.

#### Scenario: In-process bind from flow engine

- **WHEN** the flow engine calls `bridge.Service.Bind(ctx, "S", [PeerRef{slack, default, D1}])` on step entry
- **THEN** the row is upserted, the dispatcher goroutine starts, and the call returns before the agent runs its first turn

#### Scenario: Re-bind moves a peer to a different session

- **WHEN** session `S1` is bound to peer `D012345` and `Bind(ctx, "S2", [{..., peerId: "D012345"}])` is called
- **THEN** the row's `session_id` is updated to `S2`; subsequent inbound from `D012345` resolves to `S2`; `S1`'s dispatcher learns its binding count for that peer dropped (may exit if no peers remain)

#### Scenario: Bind fails on unknown channel

- **WHEN** `Bind` is called with a `PeerRef.Channel` value not in `{"telegram", "slack", "mattermost"}`
- **THEN** the call returns an error before any row is written

### Requirement: Bridge.Service.Unbind in-process API

The `internal/bridge` package SHALL expose `Service.Unbind(ctx, sessionId, peers ...PeerRef) error`. When `peers` is empty, all rows with `session_id == sessionId` are deleted and the dispatcher exits. When `peers` is non-empty, only the matching rows are deleted; the dispatcher continues if other bindings remain.

`Unbind` is called automatically by the flow engine when an interactive step completes, and is exposed via the `POST /router/unbind` HTTP endpoint for external orchestrators.

#### Scenario: Full unbind tears down dispatcher

- **WHEN** `Unbind(ctx, "S")` is called (no peers specified)
- **THEN** all `bridge_sessions` rows for `S` are deleted; the per-`S` dispatcher goroutine exits cleanly; any in-flight `agent.Run` for `S` is allowed to complete (no forced cancellation) but its output has nowhere to go and is logged at debug level

#### Scenario: Partial unbind preserves dispatcher

- **WHEN** `Unbind(ctx, "S", PeerRef{slack, default, D1})` is called while `S` is bound to three peers
- **THEN** only the matching row is deleted; the dispatcher continues running for the other two bindings

### Requirement: Auto-bind on entering an interactive flow step

The flow engine SHALL call `bridge.Service.Bind` synchronously **before** invoking the step's agent for any step with `interactive: true`. The peer reference(s) come from the resolved `interaction.target` flow-YAML field (single `PeerRef` or array). The bind MUST complete before the agent's first turn begins, so the first turn's output naturally fans out via the binding.

If `bridge.Service.Bind` returns an error (channel disabled, peer resolution failed, all peers invalid), the step MUST fail fast — the agent's first turn MUST NOT execute, the flow status transitions to `failed` for that step, and `flow.step.failed` is emitted.

#### Scenario: Auto-bind precedes agent run

- **WHEN** an `interactive: true` step starts with `args.reviewer` set
- **THEN** `bridge.Service.Bind` is invoked synchronously, the bridge row exists, and the dispatcher is started before `agent.Run` is called for the step

#### Scenario: Resolution failure prevents agent execution

- **WHEN** `interaction.target.peerId` is `"U99NOTREAL"` (a Slack user ID that no longer exists)
- **THEN** the bridge's `conversations.open` call fails; `Bind` returns an error; the step fails fast with that error in `flow.step.failed`; the agent does not run

### Requirement: First message creates threads where needed (binding mutation)

For Slack channel peers (`C<id>` without thread-ts) and Mattermost channel peers (without `<rootPostId>`), the first outbound message creates a thread root. The bridge MUST capture the returned thread identifier (`ts` for Slack, `rootPostId` for Mattermost) and MUST mutate the `bridge_sessions.peer_id` value transactionally so that:

- Subsequent outbound fan-out for the same session replies in the same thread (uses the mutated `peer_id` form).
- Subsequent inbound from that thread (e.g., reviewer reply in the new thread) resolves correctly via the mutated key.

The mutation MUST happen in the per-peer outbound goroutine, committed before that goroutine signals "delivery complete" for the message. If the mutation transaction fails, the binding is left at the pre-mutation peer ID; the next outbound for this session will create yet another thread (logged at warn level, acceptable degradation).

#### Scenario: Slack channel binding mutates to channel+thread

- **WHEN** session `S` is bound to peer `C0DEF456` (Slack channel, no thread) and the agent emits its first message
- **THEN** the bridge posts to channel `C0DEF456`, the API returns `ts: "1700000123.000200"`, and the `bridge_sessions` row is updated so `peer_id == "C0DEF456|1700000123.000200"`

#### Scenario: Subsequent reply lands in the mutated thread

- **WHEN** after the binding mutation above the agent emits a second message
- **THEN** the outbound posts with `thread_ts: "1700000123.000200"` (replies in-thread, not as a new channel message)

#### Scenario: Inbound from the new thread routes correctly

- **WHEN** a reviewer replies in the newly created thread
- **THEN** the inbound's peerId is `"C0DEF456|1700000123.000200"` and matches the mutated binding row; the dispatcher for session `S` receives the attributed inbound

#### Scenario: Mutation failure logs without breaking the conversation

- **WHEN** the transactional UPDATE of `peer_id` fails (e.g., DB connection blip)
- **THEN** the message is still delivered to the thread; a warn-level log is emitted; the next outbound for this session may create a duplicate thread root (acceptable; operator can manually clean up via `/router/unbind`+`/router/bind`)

### Requirement: User-ID peer resolution to DM channels

The bridge SHALL accept user-ID forms on `PeerRef.PeerID` and resolve them to DM channels before persistence:

- **Slack**: `peerId` starting with `U` is a user ID. The bridge calls `conversations.open` (with `users: [peerId]`) to obtain a DM channel ID, substitutes it into the row before insert/upsert.
- **Mattermost**: `peerId` matching the Mattermost user-ID format (26-char lowercase alphanumeric) is treated as a user ID. The bridge calls `channels/direct` (POST `/api/v4/channels/direct` with `[botUserId, userId]`) to obtain a DM channel, substitutes it.
- **Telegram**: there is no user-ID form — `chat_id` IS the destination. No resolution needed.

The resolution result MUST be reported back to the caller (HTTP response or in-process return value) so the caller learns the actual peer_id used.

#### Scenario: Slack U-id resolved before bind

- **WHEN** `POST /router/bind` is called with `peers: [{channel: "slack", identity: "default", peerId: "U01ABC123"}]`
- **THEN** the bridge calls `conversations.open`, obtains `D012345`, and the persisted row has `peer_id == "D012345"`; the HTTP response reports `{resolved: "D012345"}` for this peer

#### Scenario: Mattermost user-ID resolved before bind

- **WHEN** `Bind` is called with a Mattermost peer whose `peerId` is a 26-char user-id-shaped string
- **THEN** the bridge calls `channels/direct`, obtains the DM channel ID, and persists that as `peer_id`

#### Scenario: Slack D-form passed through

- **WHEN** `Bind` is called with `peerId: "D012345"` (already a DM channel form)
- **THEN** no resolution is attempted; the value is persisted as-is

### Requirement: Router-initiated conversations don't require prior inbound

The bridge MUST NOT require an inbound message from a peer before that peer can be bound to a session. Router-initiated conversation is the **primary** flow for c2-agent's interactive flow steps. Binding via `/router/bind` (or in-process `Service.Bind`) is sufficient to enable:

- Outbound delivery to the peer (next agent turn for the bound session fans out to it).
- Inbound routing from the peer (the bridge has the binding row before the peer's first reply).

#### Scenario: First message is from opencode, not from user

- **WHEN** the flow engine binds session `S` to peer `D012345` and the agent's first turn produces output
- **THEN** the bridge delivers that output to `D012345` as the first message in the conversation; the peer (user) has never messaged the bot before

#### Scenario: User's first reply routes to the pre-bound session

- **WHEN** after the above, the user replies in their Slack DM
- **THEN** the bridge resolves `D012345` to session `S` via the binding row (not via session-creation-on-inbound); the reply lands as an attributed inbound on `S`'s dispatcher

### Requirement: Mention prefix on first outbound only

When a binding has a non-null `mention_handle` (or `interaction.mention` from the flow YAML), the bridge SHALL prepend the mention to the **first** outbound message for that binding only. Subsequent outbound messages within the same conversation do NOT repeat the mention prefix — thread notifications on Slack/Mattermost cover the role of pinging the user; on Telegram the bot is already in the user's DM context.

The "first outbound" determination is per-binding, persisted via a `mention_consumed_at` column on `bridge_sessions` (NULLABLE timestamp; set to current time on first successful outbound). Once set, the prefix is not added again.

#### Scenario: Mention prefixed on first message

- **WHEN** a binding with `mention_handle == "<@U01ABC>"` is freshly created and the agent emits its first turn
- **THEN** the outbound text is `<@U01ABC> <agent text>`; the `mention_consumed_at` column is set

#### Scenario: Mention not repeated on second message

- **WHEN** the agent emits its second turn for the same binding
- **THEN** the outbound text is `<agent text>` (no prefix); the `mention_consumed_at` column is unchanged

#### Scenario: Re-bind resets mention semantics

- **WHEN** `Unbind` removes a peer's row and a subsequent `Bind` re-adds it with a mention
- **THEN** the new row's `mention_consumed_at` is NULL; the next outbound uses the prefix again
