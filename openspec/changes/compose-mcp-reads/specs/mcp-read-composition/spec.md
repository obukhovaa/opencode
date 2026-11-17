## Purpose

Compose a small set of authorized MCP reads within one invocation while exposing only selected facts, provenance and explicit incompleteness to the model.

## ADDED Requirements

### Requirement: Explicit pilot eligibility

The system SHALL expose `mcp_compose` only when the agent grants that exact tool name and `mcpComposition.readTools` contains operator-reviewed, exact MCP tool names. Implicit or wildcard grants SHALL NOT enable composition. Each child SHALL also belong to the current agent's resolved MCP tools, remain eligible under current permissions and have no conflicting effective-name registration. Server annotations alone SHALL NOT authorize an endpoint; an explicitly false read-only hint SHALL make it ineligible. A deferred child SHALL already be activated in the current session. Composition SHALL NOT modify discovery state, the tool catalog, or tool serialization order.

#### Scenario: Default configuration
- **WHEN** composition is not explicitly enabled
- **THEN** provider tools, their order, system prompt and cache markers are unchanged

#### Scenario: Named endpoint is unavailable
- **WHEN** a plan names an endpoint absent from either eligibility list or agent tools, or a deferred endpoint not activated in this session
- **THEN** the plan is rejected before its first child call

### Requirement: Bounded declarative input

The tool SHALL accept `steps` and `return` only. Each step SHALL contain a unique `id`, exact `tool`, literal object `arguments`, required `select` and optional `bindings`. A binding SHALL contain `target`, an earlier `step` and `pointer`; it SHALL replace an existing argument location with that earlier projected value. Targets SHALL be distinct, nonoverlapping RFC6901 pointers below the argument root. Selection SHALL contain `fields`, or `rows`, `fields` and `limit`: 1–32 named RFC6901 field pointers and, for rows, an array pointer and limit of 1–100. Row pointers SHALL be relative to each row. Missing values SHALL fail; null SHALL remain valid. Numeric JSON IDs SHALL retain their exact value.

The tool SHALL reject unknown keys, duplicate JSON keys, invalid references, invalid pointers and invalid selection shapes before the first call. The tool SHALL NOT execute expressions, dynamic tool names, loops, fanout or automatic pagination.

#### Scenario: Dependent read
- **WHEN** a later step binds its existing `/id` argument to an earlier projected `/id`
- **THEN** it receives that JSON value without an intervening model turn or string interpolation

#### Scenario: Invalid plan
- **WHEN** a plan contains a future reference, duplicate step ID, overlapping binding targets or unknown field
- **THEN** no child call runs and the response identifies invalid input

#### Scenario: Missing field differs from null
- **WHEN** a selected field is absent
- **THEN** projection fails explicitly rather than substituting null
- **AND** an explicitly present null value remains selectable

### Requirement: Ordinary execution controls remain effective

Every child SHALL retain ordinary per-tool grants, permission evaluation on hook-mutated arguments, pre/post hooks, ownership checks, identity context, timeout and cancellation behavior. Approval of the composition tool SHALL NOT approve its children. Human rejection SHALL retain the ordinary stop outcome. Repeated-call and denial protections SHALL apply across composition invocations. Composition SHALL add no transport retries.

#### Scenario: Child denied after argument rewrite
- **WHEN** a child pre-hook changes arguments and the resulting call is denied by policy
- **THEN** the child does not reach its server and no subsequent step runs

#### Scenario: Outer approval does not grant child access
- **WHEN** a hook approves composition but a child still requires approval
- **THEN** that child follows its own permission policy

#### Scenario: Cancellation during a read
- **WHEN** the parent is cancelled, loses flow ownership, or a child exceeds its applicable deadline
- **THEN** existing cancellation and transport cleanup behavior applies and no later child starts

### Requirement: Only authoritative filtered JSON is projectable

MCP error flags SHALL remain errors even when their text is valid JSON. Composition SHALL accept only complete text-only results whose post-hook text is one valid JSON value. Hook blocks, structured-only results, nontext content, capped output and invalid JSON SHALL stop the plan. Raw structured content, raw pre-hook output, metadata and spill files SHALL NOT be fallback data sources. Hook context SHALL remain visible separately from projected data on success and failure.

#### Scenario: Post-hook redaction
- **WHEN** a hook removes a secret field from otherwise valid JSON
- **THEN** neither projection nor a later binding can recover that field from the raw response

#### Scenario: Server returns an error object
- **WHEN** an MCP result sets its error flag and contains valid JSON
- **THEN** the step fails and the object cannot become a later step's input

#### Scenario: Unsupported output
- **WHEN** a result is structured-only, contains media/resources, is capped, or a hook rewrites its text into non-JSON
- **THEN** the response identifies unsupported output and does not return an unprojected substitute

### Requirement: Resource bounds and invocation isolation

The tool SHALL enforce at most 8 steps/calls, 32 KiB UTF-8 input, nesting depth 64, 256 KiB post-hook text per child, 1 MiB cumulative accepted text, 16 KiB serialized projected values across all steps and 120 seconds active execution excluding permission waits. The parent deadline and applicable child deadlines SHALL remain effective. Input-bound violations SHALL fail before execution; execution-bound violations SHALL stop subsequent calls. These bounds SHALL NOT be model-overridable. Intermediate state SHALL be invocation-local and SHALL NOT be exposed through conversation history, raw child result events or telemetry payloads.

#### Scenario: Large projection
- **WHEN** projected values exceed the byte budget
- **THEN** the plan fails explicitly without returning the full original result or launching another child

#### Scenario: Concurrent sessions
- **WHEN** two sessions execute composition concurrently
- **THEN** neither can reference or receive the other's intermediate data

### Requirement: Results expose provenance and incompleteness

The result SHALL include `complete`, requested completed `results`, and ordered `steps` with each step's `id`, `tool` and `status` (`ok`, `failed`, `skipped`). Row selections SHALL include `totalRows`, `returnedRows` and `limited`. Selection beyond the row limit SHALL return the first N rows in server order, set `limited:true` and `complete:false`, without constituting a tool failure. Other failures SHALL stop the plan, mark remaining steps skipped, include an error `code`, `step` and bounded message, and set the outer error flag. Human rejection and cancellation SHALL retain their ordinary control outcomes. Hook context SHALL remain visible under existing output bounds, separately from the projected-value budget.

#### Scenario: Partial rows
- **WHEN** a selection requests 10 rows from an array of 12
- **THEN** it returns the first 10, reports both counts and marks the result incomplete

#### Scenario: Second step fails
- **WHEN** the second of three steps fails
- **THEN** the response identifies that failure, marks the third skipped, and includes only requested projections successfully completed before the failure

### Requirement: Deterministic evidence precedes pilot rollout

Before any pilot opt-in, at least three recorded or fake workflows SHALL demonstrate exact child-request and required-answer-field equivalence against ordinary dispatch, including provenance, numeric IDs and nulls. Fixtures SHALL cover list-to-detail, status reads and nested JSON with irrelevant data, with responses below ordinary output caps. Permission, hook, cancellation, unsupported-output and bound scenarios SHALL pass. Default-off provider request fixtures SHALL remain byte-identical apart from existing nondeterministic IDs.

For each positive workflow, composed result-content UTF-8 bytes including envelope and hook context SHALL be at most 20 percent of ordinary result-content bytes. The report SHALL also compare full serialized Claude requests before the scripted final answer, including plan arguments and tool schemas; aggregate full-request bytes SHALL decrease. Reports SHALL identify fixture scope and SHALL NOT claim measured production token, cost, latency or task-quality improvements from these deterministic checks alone.

#### Scenario: Savings gate fails
- **WHEN** request/fact parity fails or any positive workflow misses the result-byte threshold
- **THEN** the implementation remains disabled and the failure is recorded without enabling a pilot agent

#### Scenario: Successful fixture report
- **WHEN** all correctness and volume gates pass
- **THEN** the report records exact byte totals, full-request deltas and scripted intermediate decisions avoided, while limiting rollout eligibility to one explicitly configured pilot agent
