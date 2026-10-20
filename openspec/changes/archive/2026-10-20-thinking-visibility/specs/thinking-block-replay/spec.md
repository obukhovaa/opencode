## ADDED Requirements

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
