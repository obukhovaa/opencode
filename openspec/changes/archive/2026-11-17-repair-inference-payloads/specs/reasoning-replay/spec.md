## Purpose

Preserve supported provider reasoning across tool-use turns without altering incompatible model protocols or native Claude replay.

## ADDED Requirements

### Requirement: Supported reasoning survives the request lifecycle
Supported OpenAI-compatible scalar reasoning SHALL be captured from streaming and non-streaming responses, retained in persisted assistant messages, and replayed in its supported provider field on subsequent compatible requests. Tool-call identifiers, order, results and ordinary assistant content SHALL be preserved.

#### Scenario: Streamed reasoning and tool use
- **WHEN** a compatible response emits reasoning and a tool call, the assistant message is persisted and reloaded, and the tool result is added
- **THEN** the next compatible request retains the original reasoning and the corresponding tool exchange

#### Scenario: Non-streaming reasoning
- **WHEN** a supported non-streaming response carries scalar reasoning
- **THEN** the returned and persisted assistant reasoning remains available for subsequent compatible replay

### Requirement: Replay respects provider and model boundaries
The runtime SHALL NOT fabricate reasoning or replay incompatible signed, opaque or provider-specific reasoning across model or provider changes. Ordinary requests without supported compatible reasoning SHALL remain unchanged. Native Claude signed and redacted reasoning SHALL retain its existing replay behavior.

#### Scenario: Incompatible model or provider switch
- **WHEN** stored reasoning belongs to an incompatible source model, provider or format
- **THEN** it is not sent as provider-specific replay content to the new destination

#### Scenario: Legacy and ordinary first-party messages
- **WHEN** a legacy message lacks adequate replay provenance or an ordinary first-party request contains no compatible reasoning
- **THEN** no speculative reasoning field is injected and the message remains readable

#### Scenario: Native Claude continuation
- **WHEN** a Claude tool-use conversation continues through its native adapter
- **THEN** its signed/redacted thinking and cache-related request structure are preserved
