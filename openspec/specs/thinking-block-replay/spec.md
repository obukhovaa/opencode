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
