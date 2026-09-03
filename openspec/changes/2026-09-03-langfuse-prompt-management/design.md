# Design — Langfuse-managed prompts

## Context

Langfuse is already a dependency, but only outbound: `internal/langfuse` is an
OTLP/HTTP span exporter with a thin convenience layer. Prompt Management is a
different surface of the same product — a REST API over stored, versioned,
labelled prompt text — and reading it means writing a client, because there is
no official Langfuse Go SDK.

Two consumers want it, and they differ in ways that shape the design:

- A **flow step** prompt is a user message, resolved once per step run, on a
  path that already has a context and an error channel (`handleStepError`).
- An **agent type** prompt is a system message, resolved once per agent
  construction, on a path that is deliberately context-free and whose
  consumers re-read the agent registry rather than the object being built.

## Decision 1 — a separate client, not an extension of the tracer

`PromptClient` is its own type with its own global, initialised next to
`langfuse.Init` but gated on its own config switch.

The two halves share credentials and a base URL and nothing else: one pushes
batched spans over OTLP, the other pulls text over REST with its own cache and
timeout. Folding prompts into `Client` would have coupled two lifecycles that
fail independently — a tracer whose exporter failed to construct returns a
disabled client, which would then silently disable prompt resolution too.

It also lets `telemetry.langfuse.prompts.enabled` be independent of
`telemetry.langfuse.enabled`. That is not a hypothetical: a deployment may
manage prompts in Langfuse while shipping traces to a different backend
entirely, and the reverse (tracing without prompt management) is the default
for every existing installation.

Alternative considered: put prompts under a new top-level `prompts:` config
key rather than under `telemetry`. Rejected — the credentials and `baseURL`
already resolve under `telemetry.langfuse`, and a second copy of them is a
second way for a deployment to end up half-configured. The cost is a genuine
naming wart, since fetching prompts is not observability; it is called out in
the docs and in the config comment rather than papered over.

## Decision 2 — a TTL cache that serves stale, not a fetch per use

Every resolution goes through an in-process cache keyed on `(path, label)`,
with a 60s default TTL. Three properties fall out of that choice, and each was
the point:

**Freshness is bounded and predictable.** The TTL *is* the answer to "how long
after I hit save does this take effect", which is the first question a prompt
author asks. A fetch-per-use design would answer "immediately" but would put a
network round trip in front of every step and every agent construction.

**A cold fan-out costs one request.** Each cache entry carries its own mutex,
held across the fetch, so N concurrent resolutions of one reference collapse
onto a single upstream call. A flow that fans out at process start would
otherwise issue one request per step for the same prompt.

**An outage does not stop work.** When a re-fetch fails and a copy is cached,
the cached copy is served with a warning. This is the deliberate asymmetry of
the design: an agent running last minute's prompt is strictly better than an
agent that cannot run, and prompt management is not on the critical path of
anything else in the process. The error propagates only on a cold miss, where
there is nothing to fall back to.

Alternative considered: a background refresher that keeps entries warm and
never blocks a caller. Rejected as premature — it adds a goroutine lifecycle
and a staleness window that is harder to reason about than a TTL, for a
latency win that only shows up on the first use after expiry.

## Decision 3 — no inline fallback, ever

A step or agent that declares a reference has no inline prompt to fall back to;
`validateStepPromptSource` and `ValidateAgentPromptSource` guarantee it. So a
cold resolution failure is terminal: the step fails (through `handleStepError`,
so `fallback.retry` still applies) or agent construction fails, naming the path
and the HTTP status.

The alternative — proceeding with an empty prompt — is worse than failing in a
way that is easy to underestimate. The agent would still run, still burn a
model call, and still produce output; that output would then route the flow
somewhere arbitrary. A loud failure at the reference is recoverable; a quiet
one produces plausible garbage several steps downstream.

For the same reason an empty or whitespace-only *resolved* prompt is a
resolution error rather than an accepted value: a prompt someone emptied in the
UI by accident should not silently become a no-op instruction.

## Decision 4 — exactly one prompt source, checked at load

Both surfaces reject declaring both sources, and the flow surface also rejects
declaring neither.

There is deliberately no precedence rule. "Inline wins over the reference"
would be defensible and is the kind of rule that reads fine in documentation,
but the failure it produces is the worst shape available: the step runs, the
output is plausible, and the only symptom is that edits in the Langfuse UI
appear to do nothing. Nobody debugs that quickly. A load error names the step
and both keys.

The flow check runs in `validateFlow`, after `parseFlowFile` has merged
`extends` templates, so a template supplying one key and a step supplying the
other collide rather than one silently winning. That ordering is already the
contract for every other step validation.

Rejecting "neither" on a step is a real tightening: an empty prompt previously
loaded and sent an empty string to the model. It is justified by the same
argument — now that a missing prompt could equally mean "the reference key is
misspelled", the silent path has a new and more likely cause. Agent types keep
"neither" legal, because there it means something real: fall back to the
built-in prompt for that name.

## Decision 5 — where resolution happens, per consumer

**Flow steps: in `runStep`, before substitution.** The resolved text then flows
through `substituteScoped`, shell-markup expansion, previous-step-output
prefixing and struct-output handling exactly as an inline prompt would, so
nothing downstream can tell the difference and no existing behaviour needs a
managed-prompt branch.

**Agent types: in `AgentFactory.NewAgent`, not at registry load.** Resolving at
registry load would be simpler and would give warm-up for free, but it would
also freeze the prompt for the process lifetime — a UI edit would need a
restart, which defeats the purpose. Resolving per construction means the cache
TTL governs freshness for agents exactly as it does for steps.

The fetch deliberately uses `context.Background()` rather than the caller's
ctx. Agent construction is context-free by design in this codebase (the
factory's doc comment says so: the toolset resolves under registry-owned
lifetimes so a request-scoped cancellation cannot half-build an agent), and the
prompt client's own timeout bounds the call.

## Decision 6 — `BasePrompt` must be plumbed explicitly

`getAgentPromptInternal` selects the base prompt by re-fetching the registry
entry via `reg.Get(agentName)`. For a reference-declaring agent that entry's
`Prompt` is empty by construction — the resolved text lives on the per-call
`AgentInfo` copy the factory built, which the prompt builder never sees.

Without intervention the agent would fall back to the compiled-in default
prompt for its name. That is a bad failure: no error, no warning, and a symptom
("Langfuse seems to be ignored") that points at the wrong subsystem.

This is precisely the trap `AgentPromptOptions.HasOutputSchema` was added for,
and it is solved the same way: a new `BasePrompt` option, carried through
`providerOptions.basePrompt` via `withBasePrompt(agentInfo.Prompt)`, taking
precedence over the registry lookup when non-empty. Empty — every agent whose
prompt is inline or built in — changes nothing. A test pins the fallback
failure mode directly rather than only the happy path.

Alternative considered: have `getAgentPromptInternal` resolve the reference
itself. Rejected — it is a pure, ctx-free function called from prompt
composition; putting a network fetch inside it would make every prompt build a
potentially blocking, potentially failing operation.

## Decision 7 — opencode's template dialect, not Langfuse's

Prompt bodies stored in Langfuse use `${args.*}` / `${step.*}` — opencode's own
placeholders — and Langfuse's `{{variable}}` syntax passes through verbatim as
literal text.

One dialect for authors, and no new template code: the resolved string enters
`substituteScoped` unchanged. The cost is real and worth stating: the Langfuse
playground will not preview variables in these prompts. Compiling `{{var}}`
client-side is additive and can be done later if authors ask for it; doing it
now would mean two substitution passes with different escaping rules over the
same string.

## Decision 8 — chat prompts are flattened, not rejected

Langfuse prompts are either text or a list of role/content blocks. Both
consumers here take a single string — a flow step submits one user message, an
agent type supplies one system message — so a chat prompt is flattened by
joining its blocks as `role: content` in source order, with an info log.

Rejecting them outright was considered and dropped: the type is chosen in the
Langfuse UI, often by whoever created the prompt rather than whoever wired it,
and a hard failure there is a confusing dead end for a prompt whose text is
perfectly usable.

The payload's actual JSON shape decides, with `type` treated as a hint, because
older text prompts omit the field entirely.

## Decision 9 — warm-up is best-effort and never fatal

Startup pre-fetches every reference found in the flow and agent registries,
bounded in parallelism and under a 30s overall deadline. Every failure is
logged and swallowed.

The rule is that boot is never the right place to fail on a prompt. A reference
that cannot be pre-fetched is simply fetched at use time, where it fails loudly
and names itself; failing the boot instead would take down a process whose
other flows and agents are perfectly runnable, over a subsystem that is
optional by design.

## Decision 10 — a failed fetch is remembered, so an outage costs one request

Decision 2 serves a stale copy when a re-fetch fails. What it left out is that
a failed fetch has nothing to advance `fetchedAt` with, so the entry stays
stale for the whole outage — and since the fetch runs under the entry's
single-flight lock, every caller then queues up to pay the full HTTP timeout in
turn. Measured against a failing test server: 20 callers on one warm reference
issued 20 requests, serialised. At the shipped 10s default that is minutes of
stall for a prompt whose cached copy was sitting right there, and on the agent
path it is uninterruptible (resolution is deliberately on
`context.Background()`, and a mutex wait ignores ctx regardless).

The fix is a retry floor: a failure stamps `lastAttempt` and stores `lastErr`,
and a resolution inside the floor replays that outcome — cached copy where one
exists, cached error otherwise — with no request. One request per reference per
floor, not one per caller.

Considered and rejected: moving the fetch out from under the lock (loses the
single-flight guarantee that Decision 2 wants), and `x/sync/singleflight`
(correct, but it only collapses concurrent callers — sequential ones during an
outage would still each pay a timeout, so the floor is needed either way, and
with the floor the extra dependency buys nothing).

The floor is not configurable. It is a property of "how fast can a backend
plausibly recover", not of a deployment's freshness appetite, and `cacheTTL`
already covers the latter.

## Decision 11 — a declared prompt source replaces the source, in flows too

Decision 4 says there is no precedence rule between the two sources, and that
the check runs on the merged step so a template and a step cannot each supply
one. Both still hold for a genuine conflict. But applied to `extends` merging
as originally written it produced two errors that read as loader bugs rather
than author mistakes: overriding a template's `prompt` with your own
`langfusePromptPath` collided on "declares both" even though the step's YAML
shows exactly one key, and overriding an inherited path with an inline prompt
left the template's `langfusePromptLabel` stranded, failing with "label without
path" for a key the step never wrote. The only escape was declaring the other
key as an explicit empty string — a raw-key trick nothing documented.

So `mergeTemplates` now suppresses inheritance of the competing source when the
step declares one. This is the same rule the agent registry already applied in
`applyConfigOverrides` / `mergeMarkdownIntoExisting`, and it is what the spec's
"a declared prompt source SHALL replace the source" was always describing; the
flow half simply hadn't implemented it.

It is deliberately the ONE place `mergeTemplates` knows a field name, against
that function's own "a field added to Step needs no code here" rule. The
exception is justified by the fields being mutually exclusive: independent
per-key merging is only correct for independent keys.

Directional rather than one three-key group, so `langfusePromptLabel` stays
independently inheritable and a step can declare just a label to run a shared
template prompt against a different label. Two templates that between them
supply both sources still collide — no author intent to honour there — and the
error now says to check the `extends` list, since the step's own file looks
fine.

## Decision 12 — a prompt source is judged per layer, a label on the merged entry

Every agent definition layer is a PARTIAL override: `.opencode.json` may change
only a markdown agent's model, or only its color. Validating
"langfusePromptLabel requires langfusePromptPath" per layer broke that — a JSON
entry re-labelling a markdown agent's path to `staging` refused to boot with a
message contradicting what the user had written, and the label would have been
dropped by the merge even if it had passed.

Mutual exclusion is still per layer: one layer declaring both an inline prompt
and a reference is unambiguously wrong wherever it appears. But the label
question can only be answered on the merged entry, so it moved there
(`normalisePromptSources`), and it is a warning rather than a failure: an
orphaned label selects a version of nothing, and refusing to boot over a stray
key on an otherwise valid agent is worse than ignoring it loudly.

The same pass trims the path. Validation compared it trimmed while every
consumer compared it raw, so `"langfusePromptPath": "   "` validated as "no
reference" and then failed agent construction as one.

## Decision 13 — the resolved prompt must reach EVERY prompt-building path

Decision 6 covers why `BasePrompt` has to be plumbed explicitly. What it
under-stated is how many paths need it. Three did not have it:

- `summarizer` and `descriptor`, whose providers `newAgent` builds by NAME
  rather than through the factory. A `langfusePromptPath` on either was
  silently ignored while an inline `prompt` on the same agent worked, so
  compaction and title generation kept running the compiled-in prompt.
- `agent.Update`, the in-process model switch, which rebuilt the provider with
  no options at all — swapping a managed system prompt for the built-in one for
  the rest of the process.

Hence `resolveManagedPrompt` / `resolveRegisteredPrompt` as shared helpers
rather than the resolution living inline in `NewAgent`, and `agent.basePrompt`
holding the constructed text for `Update` to re-supply. `Update` re-supplies
rather than re-resolving on purpose: re-resolving would make a model switch
fail when Langfuse is down, and a switch is not the moment to introduce a new
network dependency.

This is the failure mode the whole `BasePrompt` mechanism exists to prevent, so
it is worth naming why it recurred: the prompt builder re-reads the registry by
name, which makes "forgot to pass it" fail SILENTLY and plausibly. Nothing in
the test suite could see it — the existing test drove
`GetAgentPromptWithOptions` directly with a hand-supplied `BasePrompt`, so
deleting the plumbing left the suite green.

## Decision 14 — the prompt client is not optional per entry point

Tracing is initialised in `cmd/root.go` and `cmd/serve.go` but not `cmd/acp.go`,
and that is survivable: ACP loses traces. Prompt management is not analogous. A
referencing agent cannot be CONSTRUCTED without the client, and
`InitPrimaryAgents` logs and continues on a construction error — so the agent
silently disappears from ACP rather than degrading, or boot fails outright if it
was the only primary agent. The client therefore initialises in every entry
point that builds agents, and `initLangfusePrompts` says so.

The same asymmetry is why the warm-up fetch moved to a goroutine. It was
synchronous inside the TUI's startup spinner with a 30s bound, so an
unreachable Langfuse was a 30s boot hang — for a pass whose entire contract is
"best-effort, nothing waits on this". Reference collection stays inline (it
only walks the already-lazy registries); a use-time fetch that races the
warm-up collapses onto it through the same per-entry single flight.

## Risks

- **A silent config half-configuration.** `prompts.enabled` with unresolvable
  credentials disables the client, and every reference then fails at use time
  with `ErrPromptsDisabled`. Mitigated by validating credentials at config load
  when prompts are enabled, and by a startup warning if the client still comes
  up disabled.
- **Prompt text becomes an ungated deploy path.** Anyone who can edit the
  `production` label in Langfuse changes what running agents do, with no code
  review and no rollback through git. That is the feature, but it moves a
  review boundary; the `label` default of `production` (never `latest`) exists
  so that saving a draft is not itself a deploy.
- **API version drift.** The client targets `/api/public/v2/prompts`. A
  self-hosted Langfuse old enough to expose only v1 will 404 every reference.
  Not handled speculatively — the failure names the path and status, and a v1
  fallback can be added if an installation actually needs it.
