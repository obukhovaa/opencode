# struct-output-schema-delivery (delta)

Delta spec for the `struct-output-schema-message-delivery` change. This capability
is new; every requirement below is ADDED.

## ADDED Requirements

### Requirement: The struct_output tool definition is invariant across schemas

The `struct_output` tool's serialized definition — name, description, parameter
schema, and required list — SHALL be byte-identical for every agent, every flow
step, and every output schema for which message delivery is in effect. No value
derived from the step's `output.schema` may reach `ToolInfo`.

The parameter surface SHALL be a single `output` parameter of type `object`,
declared required.

This is what keeps the provider's cached prefix (`tools` → `system` → `messages`)
stable across consecutive steps of one agent, so the first request of a step can
read the previous step's cache entry instead of writing a new one.

#### Scenario: Two steps with different schemas ship identical tool blocks

- **WHEN** two `struct_output` tools are built from two different output schemas in `message` mode
- **THEN** their `ToolInfo` values MUST be equal — same name, same description, same parameters, same required list

#### Scenario: Provider prefix is unchanged across schemas

- **WHEN** an Anthropic request is built twice from the same agent and tool set, differing only in the step's output schema
- **THEN** the serialized `tools` block and the `system` block MUST be byte-identical between the two requests
- **AND** the cache breakpoint MUST fall on the same tool position in both

#### Scenario: Tool delivery mode preserves the previous surface

- **WHEN** the delivery mode is `tool`
- **THEN** the tool's parameters MUST be the schema's own properties and required list, exactly as before this change

### Requirement: Message delivery applies only where it can be faithfully expressed

Message delivery SHALL be used only for schemas and models that the invariant
`{"output": {"type": "object"}}` surface can represent. The runtime SHALL fall
back to `tool` delivery, without erroring, when:

- the schema's root is not an object with properties (e.g. `{"type": "array"}`),
  since the invariant parameter declares an object and no payload could satisfy
  both it and the schema; or
- the schema declares a property named `output`, since the wrapper key would be
  ambiguous with a real field; or
- the agent's model is served by a request builder that cannot express an
  object-typed parameter with no declared properties.

Behavior for every such schema SHALL be identical to the behavior before message
delivery existed.

#### Scenario: Non-object root schema keeps the legacy surface

- **WHEN** an agent's output schema is `{"type": "array", "items": {...}}` and the configured delivery is `message`
- **THEN** the tool's parameters MUST be the legacy single-`output` wrapper carrying the schema
- **AND** a call of `{"output": [...]}` MUST be accepted

#### Scenario: Schema declaring its own output property keeps the legacy surface

- **WHEN** an agent's output schema declares a property named `output`
- **THEN** message delivery MUST NOT be used, and a flat call MUST retain every sibling field

#### Scenario: Model that cannot express the invariant parameter keeps the legacy surface

- **WHEN** an agent's model is served by a builder that rejects an object-typed parameter with no declared properties
- **THEN** the schema MUST stay in the tool parameters and no envelope is injected

### Requirement: The output schema is delivered in the message tail

When the delivery mode is `message` and the `struct_output` tool is enabled for
the agent, the agent SHALL inject the output schema into the message history
as a synthetic user message containing a `<struct_output_schema>` envelope. The
envelope SHALL be appended after the run's user turn, and SHALL carry a
fingerprint of the schema and a statement that it supersedes any schema given
earlier in the conversation.

The envelope MUST NOT be written into the system prompt or into any tool
definition.

#### Scenario: Schema-bearing run injects the envelope

- **WHEN** a run starts on a session whose history contains no envelope for the current schema
- **THEN** a synthetic user message carrying the envelope MUST be appended to the history sent upstream
- **AND** it MUST appear after the run's user message

#### Scenario: Run with no output schema injects nothing

- **WHEN** an agent has no output schema configured
- **THEN** no envelope message is created and the history sent upstream is unchanged

#### Scenario: Agent with struct_output disabled injects nothing

- **WHEN** an agent declares an output schema but disables the `struct_output` tool
- **THEN** no envelope message is created — the agent is documented to run with free-form output and must not be told to call a tool it does not have

#### Scenario: The envelope precedes the run's user turn

- **WHEN** the envelope is injected on a run that has a user turn
- **THEN** it MUST be ordered before that user turn, so provider heuristics that read the last message still see the caller's prompt

#### Scenario: Auto-resume run with no user turn still carries the schema

- **WHEN** a run is entered with no content and no attachments (a background-task auto-resume) and the history carries no envelope for the current schema
- **THEN** the envelope MUST still be injected

### Requirement: The envelope is injected once per session per schema

The agent SHALL inject the envelope only when the history it is about to send
carries no envelope bearing the current schema's fingerprint. A run that finds a
matching fingerprint MUST inject nothing.

The presence check SHALL run against the message list actually being sent — after
summary filtering — so that a compaction which drops the envelope causes it to be
re-injected. This SHALL hold both for a later run and for a compaction that
happens part-way through a run, after which the remainder of that run MUST still
carry the schema.

#### Scenario: Second turn of the same step does not re-inject

- **WHEN** a run starts on a session whose history already contains an envelope with the current schema's fingerprint
- **THEN** no new envelope message is created

#### Scenario: Forked step with a different schema injects the new envelope

- **WHEN** a step's session was forked from a previous step whose envelope carries a different fingerprint
- **THEN** the new schema's envelope MUST be injected
- **AND** the stale envelope is left in the copied history untouched

#### Scenario: Compaction re-arms injection

- **WHEN** a session has been compacted and the summary boundary excludes the envelope message
- **THEN** the next run MUST re-inject the envelope

#### Scenario: Mid-run compaction re-injects before the run continues

- **WHEN** auto-compaction fires inside the agentic loop and rebuilds the history from the summary boundary
- **THEN** the envelope MUST be re-injected into that rebuilt history
- **AND** the remainder of the run, including any forced `struct_output` wrap-up, MUST still see the schema

### Requirement: struct_output validates the payload against the full schema

The `struct_output` tool SHALL retain the complete output schema in memory and
validate every call against it, independent of how the schema was delivered to the
model. Validation SHALL materialize declared defaults for absent properties and
SHALL reject a payload still missing a required field with an error tool result
that names the missing fields, so the agent loop can retry.

The tool result content SHALL be the bare schema-conformant document — never the
`output` wrapper — so downstream consumers of the step output are unaffected by the
delivery mode.

#### Scenario: Wrapped payload is unwrapped before it is returned

- **WHEN** the model calls `struct_output` with `{"output": {...document...}}`
- **THEN** the tool result content MUST be the document alone

#### Scenario: Flat payload is accepted

- **WHEN** the model calls `struct_output` with the document's fields at the top level, omitting the `output` wrapper
- **THEN** the payload MUST be treated as the document rather than rejected

#### Scenario: Half-wrapped payload loses nothing

- **WHEN** the model puts some fields inside `output` and leaves others beside it
- **THEN** the siblings the schema declares MUST be folded into the returned document rather than dropped
- **AND** a dropped field MUST NOT be silently replaced by its declared default

#### Scenario: An empty payload is resolved by defaults and required fields

- **WHEN** the model calls `struct_output` with `{}`
- **THEN** the outcome MUST match `tool` delivery: declared defaults are materialized, and the call is rejected only if a required field remains absent

#### Scenario: Neither shape is rejected with a retryable error

- **WHEN** the payload carries neither an object-valued `output` nor a recognizable document
- **THEN** the tool MUST return an error result naming the expected shape, and MUST NOT terminate the step

#### Scenario: Defaults and required fields are still enforced

- **WHEN** a call omits a property that declares a `default`
- **THEN** the default MUST be materialized into the returned document
- **AND** a required field that is still absent MUST produce an error result listing it

### Requirement: Schema delivery mode is configurable

The delivery mode SHALL be configurable as `structOutputSchemaDelivery` with the
values `message` and `tool`, settable at the top level of `.opencode.json` and per
agent. A per-agent value SHALL override the top-level value. The effective default
SHALL be `message`.

An unrecognized value SHALL fall back to the default and log a warning rather than
fail the run. Matching SHALL be exact — case-sensitive and without whitespace
trimming — so the set the runtime accepts equals the published JSON Schema enum.

#### Scenario: Per-agent value overrides the global value

- **WHEN** the top level sets `message` and an agent sets `tool`
- **THEN** that agent's `struct_output` carries the schema in its tool parameters while other agents' do not

#### Scenario: Unset config uses message delivery

- **WHEN** neither the top level nor the agent declares the field
- **THEN** the effective mode is `message`

#### Scenario: Unrecognized value fails safe

- **WHEN** the field is set to a value that is neither `message` nor `tool` — including a case or whitespace variant such as `Message` or `" message "`
- **THEN** the effective mode is `message` and a warning is logged
