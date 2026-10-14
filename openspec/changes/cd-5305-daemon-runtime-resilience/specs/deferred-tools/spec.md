## ADDED Requirements

### Requirement: Replayed tool references are limited to the declared tools

When it replays server-side tool searches, the Anthropic client SHALL drop every `tool_reference` that names a tool the request does not declare in `tools[]` (deferred `defer_loading` entries and activated tools count as declared). A turn that lost any reference SHALL be replayed without its reasoning blocks; a search left with no references and no error code SHALL be removed together with its `server_tool_use`. Plain `tool_use` blocks for undeclared tools SHALL be kept. One WARN per request SHALL name the dropped tools and the turns. The `count_tokens` request SHALL apply the same filter. A history whose references are all declared SHALL be sent unchanged.

#### Scenario: Session replayed under an agent without the found tools

- **WHEN** a session whose earlier turn found `datadog_search_logs` by tool search is replayed under an agent that does not declare it
- **THEN** the request carries no reference to `datadog_search_logs`, that turn carries no thinking blocks, and the request is accepted

#### Scenario: Partially declared search

- **WHEN** a search found two tools and only one is declared
- **THEN** the search is replayed with the declared reference only

### Requirement: An open tool loop that lost a reference goes out without thinking

When the turn that lost a reference is the last assistant turn, ends in `tool_use` and is followed only by tool results, that single request SHALL be sent with thinking disabled: no thinking parameter, no interleaved-thinking beta, and no thinking blocks anywhere in its history. The next request after a new assistant turn SHALL enable thinking again.

#### Scenario: Restart in the middle of a tool loop

- **WHEN** the pod restarted while the last turn's tool calls were answered and that turn lost a reference
- **THEN** the next request has no thinking config and no thinking blocks
- **AND** the request after the model's answer has thinking again
