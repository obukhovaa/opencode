# Thinking Block Replay

## Purpose

Defines how provider reasoning ("thinking") blocks are persisted on assistant messages and replayed on subsequent requests. Anthropic-dialect APIs are stateless and sign each thinking block; echoing blocks back byte-exact preserves the model's working reasoning across tool boundaries (the documented multi-turn pattern), while dropped blocks silently degrade multi-step tool use. The message model stores reasoning per block (text + signature, or an opaque redacted payload); the anthropic provider extracts finalized blocks from completed responses and re-emits them verbatim when converting history. Providers sharing the anthropic client (native Anthropic, Bedrock, Vertex, Anthropic-compatible endpoints such as Moonshot's) inherit the behavior; unsigned reasoning (legacy rows, streamed previews, non-Anthropic sources) is display-only and never replayed.
## Requirements
### Requirement: Reasoning blocks are persisted per block with signatures
The message model SHALL store assistant reasoning as a sequence of discrete reasoning parts, one per provider content block, preserving each block's text and cryptographic signature exactly as produced by the provider. Redacted reasoning blocks SHALL be stored with their opaque payload preserved verbatim.

#### Scenario: Anthropic response with a signed thinking block
- **WHEN** an assistant turn completes and the provider produced a thinking block with text `T` and signature `S`
- **THEN** the persisted assistant message contains a reasoning part with thinking `T` and signature `S`, byte-identical to the provider values

#### Scenario: Response with multiple thinking blocks
- **WHEN** a single assistant turn produces more than one thinking block
- **THEN** each block is persisted as its own reasoning part, in emission order, each with its own signature

#### Scenario: Redacted thinking block
- **WHEN** the provider returns a `redacted_thinking` block with opaque data `D`
- **THEN** a reasoning part marked redacted with payload `D` is persisted, and no plaintext is rendered for it

#### Scenario: Legacy rows remain valid
- **WHEN** an assistant message persisted before this capability (reasoning text only, no signature) is loaded
- **THEN** it deserializes without error and its reasoning is treated as non-replayable

### Requirement: Live streaming display is unaffected by block finalization
Thinking deltas SHALL continue to stream to the UI incrementally while a response is in flight; at completion the streamed preview parts SHALL be replaced by the authoritative per-block reasoning parts without altering the concatenated thinking text shown to the user.

#### Scenario: Streaming then finalization
- **WHEN** a response streams thinking deltas and then completes
- **THEN** thinking text is visible incrementally during the stream, and after completion the stored message carries the authoritative signed blocks whose concatenated text equals what was displayed

### Requirement: Stored reasoning blocks are replayed verbatim to Anthropic-dialect providers
When converting conversation history for an Anthropic-dialect request (native Anthropic, Bedrock, Vertex, or an Anthropic-compatible base URL), assistant messages SHALL emit their stored reasoning blocks before other content blocks: signed reasoning parts as `thinking` blocks (text + signature unmodified) and redacted parts as `redacted_thinking` blocks (payload unmodified).

#### Scenario: Tool-use loop replays reasoning
- **WHEN** an assistant message with a signed thinking block and a tool_use block is resent as history together with its tool_result
- **THEN** the request's assistant turn begins with the original thinking block (same text, same signature) followed by the tool_use block

#### Scenario: Unsigned reasoning is not replayed
- **WHEN** an assistant message's reasoning part has no signature and is not redacted (legacy row or non-Anthropic source)
- **THEN** no thinking block is emitted for it and the request remains valid

#### Scenario: Cache breakpoints unchanged
- **WHEN** history messages carry cache-control markers on their last content block
- **THEN** replayed reasoning blocks never carry cache-control and the marker still lands on the message's last text/tool_use/tool_result block

### Requirement: Reasoning-only assistant turns are not sent
An assistant message whose only content is reasoning (no text, no tool calls) SHALL be excluded from provider requests.

#### Scenario: Aborted turn with only thinking
- **WHEN** an assistant message contains reasoning parts but no text content and no tool calls
- **THEN** message cleaning drops it before conversion and no empty assistant turn reaches the provider

### Requirement: Claude adaptive thinking asks for summarized display
When the anthropic client requests adaptive thinking, it SHALL set `thinking.display` to `summarized` for every model it serves except Kimi models (provider `kimi`), whose request SHALL keep the bare `{type: adaptive}` config. Requests that do not enable thinking (a forced tool choice, a turn whose replay disabled thinking) SHALL carry no thinking config, as before.

#### Scenario: Claude model on Bedrock
- **WHEN** a user turn goes to `bedrock.eu-claude-sonnet-5-5` with adaptive thinking
- **THEN** the request body's `thinking` is `{"type":"adaptive","display":"summarized"}`

#### Scenario: Kimi over Moonshot's Anthropic-compatible endpoint
- **WHEN** a user turn goes to a Kimi model through the anthropic client
- **THEN** the request's `thinking` carries no `display` field

#### Scenario: Summarized block replays verbatim
- **WHEN** a summarized thinking block with text `T` and signature `S` is resent as history in a tool-use loop
- **THEN** the replayed block carries `T` and `S` unmodified, and the request is accepted

### Requirement: Streamed thinking deltas are coalesced
The anthropic stream SHALL emit the first thinking delta of each content block immediately and buffer the following ones, emitting the buffer when it reaches 256 bytes, at every content block stop, and before the response completes (a truncated stream included). The concatenation of the emitted deltas SHALL equal the block's streamed thinking text.

#### Scenario: Long summarized block
- **WHEN** a thinking block streams 60 deltas of 10 bytes
- **THEN** four thinking deltas are emitted, of 10, 260, 260 and 70 bytes, and the completed response's reasoning part holds the whole 600-byte text with its signature

