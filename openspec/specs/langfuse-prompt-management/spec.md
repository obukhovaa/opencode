# langfuse-prompt-management Specification

## Purpose
Lets the text of a prompt live in Langfuse Prompt Management rather than in
code. A flow step or an agent type may reference a stored prompt by path and
label instead of carrying its text, so a wording change ships from the Langfuse
UI with no file edit, no image build and no deploy. Structure — which steps
exist, which agent runs them, what routes where — stays in code.

Defines how a reference is declared, when it is resolved, how resolution is
cached, and what happens when Langfuse is unreachable.
## Requirements
### Requirement: Prompt management is configured independently of tracing

The system SHALL expose a `telemetry.langfuse.prompts` configuration object
with fields `enabled` (bool, default false), `label` (string, default
`production`), `cacheTTL` (Go duration string, default `60s`), `timeout` (Go
duration string, default `10s`) and `warmup` (bool, default true). It SHALL
resolve credentials and base URL from the surrounding `telemetry.langfuse`
block, including its `env:VAR_NAME` indirection and `LANGFUSE_*` environment
fallbacks.

Prompt management SHALL be gated on `prompts.enabled` alone and SHALL NOT
require `telemetry.langfuse.enabled`; tracing and prompt management are
independent capabilities over shared credentials. When prompts are enabled,
configuration load SHALL fail if the credentials do not resolve, or if
`cacheTTL` or `timeout` is unparseable or non-positive. A disabled prompts
block SHALL NOT be validated. The fields SHALL appear in the generated
configuration JSON schema.

#### Scenario: Prompts enabled without tracing

- **WHEN** `telemetry.langfuse.prompts.enabled` is true, credentials resolve, and `telemetry.langfuse.enabled` is false
- **THEN** configuration load succeeds and prompt references resolve

#### Scenario: Prompts enabled without credentials

- **WHEN** `telemetry.langfuse.prompts.enabled` is true and neither config nor environment supplies a public and secret key
- **THEN** configuration load fails with an error naming `telemetry.langfuse.prompts`

#### Scenario: Unparseable duration is caught at load

- **WHEN** an enabled prompts block sets `cacheTTL` to a value that is not a positive Go duration
- **THEN** configuration load fails with an error naming `cacheTTL`

#### Scenario: A disabled block is not validated

- **WHEN** a prompts block has `enabled` false and an invalid `cacheTTL`
- **THEN** configuration load succeeds

#### Scenario: Fields present in schema

- **WHEN** the configuration JSON schema is generated
- **THEN** `telemetry.langfuse.prompts` is present with its five fields, and `langfusePromptPath` / `langfusePromptLabel` are present on an `agents` entry

### Requirement: A flow step declares exactly one prompt source

A flow step SHALL accept `langfusePromptPath` (string) and
`langfusePromptLabel` (string, optional) as an alternative to `prompt`.
Exactly one of `prompt` / `langfusePromptPath` MUST be present: declaring both,
or neither, SHALL be a load error naming the step, with no precedence rule
applied. Neither an inline `prompt` nor a `langfusePromptPath` consisting only
of whitespace SHALL count as a declared source, and the runtime SHALL agree
with validation on that. `langfusePromptLabel` SHALL be a load error unless
`langfusePromptPath` is also present.

The check SHALL run after `extends` template merging, so that a template and a
step that between them supply both sources are rejected rather than one
silently winning, and the error SHALL point at the `extends` list because the
step's own YAML may show only one key.

Both keys SHALL be inheritable through `extends`, but NOT independently: a step
that declares either source SHALL inherit none of the competing source's keys,
so a declared prompt source replaces the SOURCE rather than colliding with it.
`langfusePromptLabel` SHALL remain independently inheritable, so a step may
declare only a label and take the path from its template.

#### Scenario: Reference in place of an inline prompt

- **WHEN** a step declares `langfusePromptPath` and no `prompt`
- **THEN** the flow loads

#### Scenario: Both sources on one step

- **WHEN** a step declares both `prompt` and `langfusePromptPath`
- **THEN** loading fails naming the step and both keys, and neither source is preferred

#### Scenario: Neither source on one step

- **WHEN** a step declares neither `prompt` nor `langfusePromptPath`
- **THEN** loading fails naming the step

#### Scenario: A declared source replaces an inherited one

- **WHEN** a step template supplies `prompt` and a step extending it supplies `langfusePromptPath`
- **THEN** the flow loads with the step's reference and no inline prompt, because the step's declared source suppresses inheritance of the competing one
- **AND WHEN** a template supplies `langfusePromptPath` plus `langfusePromptLabel` and the step supplies `prompt`
- **THEN** the flow loads with the step's inline prompt and neither Langfuse key, so no orphaned label survives

#### Scenario: A label alone re-labels an inherited path

- **WHEN** a step template supplies `langfusePromptPath` and the step extending it declares only `langfusePromptLabel`
- **THEN** the flow loads with the template's path and the step's label

#### Scenario: Two templates supply both sources

- **WHEN** a step extends one template supplying `prompt` and another supplying `langfusePromptPath`, declaring neither itself
- **THEN** loading fails naming the step and pointing at its `extends` templates

#### Scenario: Label without a path

- **WHEN** a step declares `langfusePromptLabel` and neither it nor its templates supply `langfusePromptPath`
- **THEN** loading fails naming the step

#### Scenario: A postponed step does not resolve its prompt

- **WHEN** a step whose prompt comes from Langfuse is parked by a `postpone: true` rule
- **THEN** no prompt fetch is attempted, and an unreachable Langfuse SHALL NOT turn the park into a step failure; the attempt that actually runs resolves it

### Requirement: An agent type declares exactly one prompt source

An agent type SHALL accept `langfusePromptPath` and `langfusePromptLabel` as an
alternative to its inline system prompt, in a markdown agent's YAML frontmatter
and in the `.opencode.json` `agents` block. For a markdown agent the file body
IS the inline prompt, so a non-empty body together with `langfusePromptPath`
SHALL be a load error. Declaring neither source SHALL remain legal and SHALL
leave the agent on its built-in prompt.

Where definition layers merge, a declared prompt source SHALL replace the
inherited source wholly: a later inline prompt SHALL clear an inherited
reference, and a later reference SHALL clear an inherited inline prompt, so
that a registry entry never holds both.

Because each layer is a PARTIAL override, `langfusePromptLabel` without a path
SHALL NOT be a per-layer load error: a higher-priority layer may declare only a
label to re-label a path a lower layer supplied. A label that has no path on the
MERGED entry SHALL be dropped with a warning rather than failing the load — it
selects a version of nothing, and refusing to boot over a stray key on an
otherwise valid agent is worse than ignoring it loudly. A `langfusePromptPath`
SHALL be trimmed on the merged entry, so a whitespace-only value is treated as
absent by every consumer and not just by validation.

#### Scenario: Markdown agent referencing a managed prompt

- **WHEN** a markdown agent declares `langfusePromptPath` in frontmatter and has an empty body
- **THEN** the agent loads carrying the reference and no inline prompt

#### Scenario: Markdown body alongside a reference

- **WHEN** a markdown agent declares `langfusePromptPath` in frontmatter and has a non-empty body
- **THEN** loading fails naming the agent and the conflict

#### Scenario: Config reference replaces an inherited body

- **WHEN** a markdown agent supplies an inline prompt and the `.opencode.json` entry for the same agent supplies `langfusePromptPath`
- **THEN** the registry entry carries the reference and its inline prompt is cleared

#### Scenario: Config prompt replaces an inherited reference

- **WHEN** a definition supplies `langfusePromptPath` and a higher-priority layer supplies an inline `prompt`
- **THEN** the registry entry carries the inline prompt and the reference is cleared

#### Scenario: Neither source is legal for an agent

- **WHEN** an agent definition declares neither an inline prompt nor a reference
- **THEN** it loads and uses the built-in prompt for its name

### Requirement: A reference resolves by path and label, defaulting to production

The system SHALL resolve a reference by requesting the prompt at the given path
under the given label from the Langfuse prompts API, sending the configured
credentials as HTTP Basic auth. A prompt path MAY contain slashes — they are
part of the name and render as folders in the Langfuse UI — and SHALL be
escaped as a single path SEGMENT so that the request addresses one name rather
than a sub-path. The escaping SHALL be lossless for every character legal in a
Langfuse prompt name: in particular a space SHALL be encoded such that the
server decodes it back to a space, not to `+`.

A reference that names no label SHALL resolve under the configured default
label, which SHALL itself default to `production`. The default SHALL NOT be
`latest`.

Both text prompts and chat prompts SHALL be accepted. A chat prompt SHALL be
flattened to text by joining its blocks as `role: content` in source order,
with blocks carrying no role contributing their content alone. The payload's
JSON shape SHALL decide how it is read, with any declared type treated only as
a hint.

Blocks dropped during flattening SHALL be logged with both the number seen and
the number kept, so a silently discarded block — Langfuse's `placeholder`, for
which opencode has nothing to substitute — is visible rather than inferred. A
chat payload whose blocks carry neither a role nor text SHALL be reported as an
unsupported shape naming the payload type, NOT as an empty prompt: those are
different mistakes with different remedies.

#### Scenario: Slash-bearing path is one escaped name

- **WHEN** a reference names the path `flows/react-on-jira/prepare-plan`
- **THEN** the request addresses that name as a single escaped path segment, not as nested path segments

#### Scenario: Unlabelled reference uses the default label

- **WHEN** a reference names no label and no default is configured
- **THEN** the prompt is requested under label `production`

#### Scenario: Configured default label applies

- **WHEN** `telemetry.langfuse.prompts.label` is set and a reference names no label
- **THEN** the prompt is requested under the configured label

#### Scenario: Chat prompt is flattened

- **WHEN** a resolved prompt is a chat prompt with a system block and a user block
- **THEN** the resolved text contains both blocks as `role: content` in source order

#### Scenario: Text payload with a chat type hint

- **WHEN** a payload declares type `chat` but carries a plain string prompt
- **THEN** it resolves as text

### Requirement: Resolved prompts are cached and served stale on failure

The system SHALL cache resolved prompts in process, keyed on path and label
together, for the configured `cacheTTL`. A resolution within the TTL SHALL be
served from cache without contacting Langfuse. Concurrent resolutions of the
same reference on a cold cache SHALL result in a single upstream request.

When a re-fetch fails and a cached copy exists, the system SHALL serve the
cached copy and log a warning rather than returning an error.

A failed fetch SHALL be remembered for a bounded retry floor, and resolutions
within that floor SHALL replay its outcome — the cached copy where one exists,
the cached error otherwise — without issuing another request. A failed fetch
cannot advance the entry's freshness, so without this the entry stays stale for
the whole outage and every caller re-attempts; because resolution of one
reference is serialised, N callers would each pay the full fetch timeout in
turn. The cost of a sustained outage SHALL therefore be one request per
reference per floor, not one per caller.

A resolution whose caller context is already cancelled SHALL return that
cancellation rather than a cached copy: a caller can wait on the per-reference
lock for longer than its own deadline, and serving stale text with no error
would report a cancelled run as a successful resolution.

#### Scenario: Repeat resolution inside the TTL

- **WHEN** the same reference is resolved three times within `cacheTTL`
- **THEN** exactly one upstream request is made

#### Scenario: Label is part of the cache key

- **WHEN** the same path is resolved under two different labels
- **THEN** each label is fetched separately

#### Scenario: Concurrent cold resolutions collapse

- **WHEN** several callers resolve the same reference simultaneously with nothing cached
- **THEN** exactly one upstream request is made and every caller receives the result

#### Scenario: Outage after a successful fetch

- **WHEN** a reference has been resolved successfully and a later re-fetch fails
- **THEN** the previously resolved text is returned and a warning is logged

#### Scenario: A sustained outage costs one request, not one per caller

- **WHEN** a reference with a cached copy is resolved many times while Langfuse is failing, within the retry floor
- **THEN** exactly one upstream request is made and every caller receives the cached copy
- **AND WHEN** the same happens with nothing cached
- **THEN** exactly one upstream request is made and every caller receives the same error

#### Scenario: The floor expires

- **WHEN** the retry floor elapses after a failure and Langfuse has recovered
- **THEN** the next resolution re-fetches and returns the new text

#### Scenario: A cancelled caller is not served stale text

- **WHEN** a reference with a cached copy is resolved with an already-cancelled context
- **THEN** the cancellation is returned, not the cached copy

### Requirement: A cold resolution failure is terminal, and no agent runs on an empty prompt

When a reference cannot be resolved and no cached copy exists, the system SHALL
fail the caller with an error naming the prompt path, and SHALL NOT fall back to
an inline default, an empty prompt, or a built-in prompt. A missing prompt, a
missing label and rejected credentials SHALL be distinguishable in the returned
error. A prompt that resolves to empty or whitespace-only text SHALL be treated
as a resolution failure. Resolving a reference while prompt management is
disabled SHALL fail rather than silently yielding no prompt.

For a flow step the failure SHALL be routed through the step's normal error
path, so that a step's `fallback` handling applies to it as it does to any other
step failure. For an agent type the failure SHALL fail agent construction.

#### Scenario: Cold miss propagates

- **WHEN** a reference is resolved with nothing cached and the request fails
- **THEN** an error is returned naming the path

#### Scenario: Missing prompt is distinguishable

- **WHEN** Langfuse reports the prompt or label does not exist
- **THEN** the error identifies it as a not-found condition, distinctly from an authorization failure

#### Scenario: Empty resolved text is an error

- **WHEN** a prompt resolves to whitespace only
- **THEN** an error is returned and no prompt text is produced

#### Scenario: Step failure carries the step and path

- **WHEN** a flow step's reference cannot be resolved
- **THEN** the step fails through the normal step-error path with an error naming both the step and the prompt path

#### Scenario: Resolution while disabled

- **WHEN** a reference is resolved and prompt management is not enabled or has no credentials
- **THEN** an error is returned rather than an empty prompt

### Requirement: Resolution timing makes UI edits reach running processes

A flow step's prompt SHALL be resolved when the step runs, before argument
substitution, so that the resolved text passes through the identical downstream
pipeline as an inline prompt — argument and step-variable substitution, shell
markup expansion, previous-step-output prefixing and structured-output handling
all behave the same.

A step that is about to be postponed SHALL NOT resolve its prompt, since it
returns before the prompt is used.

An agent type's prompt SHALL be resolved when the agent is constructed, not when
the agent registry is loaded, so that an edit in Langfuse reaches the next agent
built from that definition within the cache TTL rather than requiring a process
restart. This holds for agents built per use — subagents spawned by the `task`
tool and flow-step agents. A primary agent (`mode: agent`) is built once at
startup and held for the process lifetime, so its resolved text is pinned until
restart; documentation SHALL state this rather than claim TTL-bounded freshness
for every agent.

The resolved text SHALL reach the prompt builder even though the builder
consults the agent registry by name, so that a referencing agent never falls
back to a built-in prompt. This SHALL hold on EVERY path that builds a system
prompt from an agent definition, including the built-in `summarizer` and
`descriptor` agents — whose providers are constructed by name rather than
through the agent factory — and including a provider rebuilt by an in-process
model switch. A path that omits the resolved text would silently run the
built-in prompt, which reads as "Langfuse is being ignored" rather than as an
error, and is the failure mode this requirement exists to prevent.

#### Scenario: Managed step prompt is substituted like an inline one

- **WHEN** a step resolves a managed prompt containing `${args.*}` placeholders
- **THEN** the placeholders are substituted exactly as they would be in an inline prompt

#### Scenario: Inline step prompt bypasses resolution

- **WHEN** a step declares an inline prompt
- **THEN** no prompt resolution is attempted and the inline text is used verbatim

#### Scenario: Managed agent prompt reaches the model

- **WHEN** an agent whose definition declares only `langfusePromptPath` is constructed and its system prompt is built
- **THEN** the system prompt is based on the resolved text, not on the built-in prompt for that agent's name

#### Scenario: A managed prompt on a built-in helper agent is honoured

- **WHEN** the `summarizer` or `descriptor` agent declares `langfusePromptPath` and a primary agent is constructed
- **THEN** that helper agent's provider is built with the resolved text, exactly as an inline `prompt` on the same agent would be

#### Scenario: A model switch keeps the managed prompt

- **WHEN** a running agent whose prompt came from Langfuse has its model changed in process
- **THEN** the rebuilt provider keeps the same resolved system prompt instead of reverting to the built-in one

### Requirement: Startup pre-fetch is best-effort and never fails the boot

When `warmup` is enabled, the system SHALL pre-fetch every prompt referenced by
the discovered flows and the registered agent types at startup, de-duplicating
references that resolve to the same path and label. The pass SHALL be bounded in
parallelism and in total duration. Every pre-fetch failure SHALL be logged and
SHALL NOT prevent startup; such a reference SHALL simply be fetched at use time.

The fetching SHALL NOT delay startup either: nothing downstream waits on its
result, and a slow or unreachable Langfuse would otherwise show up as a boot
hang for the pass's full duration.

Separately, the prompt client SHALL be initialised in EVERY entry point that
constructs agents, not only those that also start tracing. Tracing is optional
per entry point — its absence loses traces — whereas a referencing agent cannot
be constructed at all without the client, so omitting it makes the agent vanish
from that entry point rather than degrade.

#### Scenario: Warm-up populates the cache

- **WHEN** startup pre-fetches a reference and it is resolved shortly afterwards
- **THEN** the resolution is served from cache with no further upstream request

#### Scenario: Duplicate references are fetched once

- **WHEN** two definitions reference the same path, one naming the default label explicitly and one omitting it
- **THEN** the pre-fetch issues a single request for them

#### Scenario: An unresolvable reference does not fail startup

- **WHEN** one referenced prompt cannot be pre-fetched
- **THEN** the failure is logged, startup continues, and other references are still pre-fetched

#### Scenario: An unreachable Langfuse does not delay startup

- **WHEN** warm-up is enabled and Langfuse does not respond
- **THEN** startup proceeds without waiting for the pass to finish or time out

#### Scenario: Every agent-constructing entry point can resolve references

- **WHEN** an agent declaring `langfusePromptPath` is used under any entry point that builds agents, including the ACP server
- **THEN** the reference resolves, rather than failing with prompt management not being enabled

