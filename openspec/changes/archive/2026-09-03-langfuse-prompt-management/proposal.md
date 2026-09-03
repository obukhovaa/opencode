# Langfuse-Managed Prompts for Flow Steps and Agent Types

## Why

A prompt is authored in exactly two places today, and both of them are code:
a flow step's `prompt` key in YAML, and an agent type's system prompt (a
markdown agent's body, or the `prompt` field in `.opencode.json`). Changing a
word in either means editing a file, building an image, and rolling a
deployment — for a change that alters no behaviour the build system can
verify.

That cost falls hardest on exactly the people who should be iterating: the
prompt is the part a non-engineer can meaningfully improve, and it is the part
locked behind the longest feedback loop. In practice prompts are tuned by
whoever can run the release, not by whoever knows what the prompt should say.

Langfuse is already wired into opencode as a trace destination, and its Prompt
Management feature is the other half of the same product: prompts are stored,
versioned, labelled and edited in a UI that non-engineers already have access
to. Nothing in opencode reads it.

This change adds the read side. A flow step or an agent type may reference a
Langfuse prompt by path instead of carrying its text, and the resolved prompt
is fetched at run time. A wording change becomes a save in the Langfuse UI,
picked up by running processes within the cache TTL. Structure — which steps
exist, which agent runs them, what routes where — stays in code, where it
belongs and where review catches it.

## What Changes

- New `internal/langfuse/prompts.go`: a prompt-management client resolving
  `path` + `label` to text over `GET /api/public/v2/prompts/{name}`, with a
  TTL cache, per-entry single-flight, and serve-stale-on-error. Independent of
  the tracing `Client` — they share credentials and nothing else.
- New config block `telemetry.langfuse.prompts` (`enabled`, `label`,
  `cacheTTL`, `timeout`, `warmup`), gated separately from
  `telemetry.langfuse.enabled` so prompt management and tracing can be used
  without each other.
- Flow steps accept `langfusePromptPath` / `langfusePromptLabel` in place of
  `prompt`. Resolution happens in `runStep` before substitution, so a managed
  prompt goes through the identical downstream pipeline; failures route
  through `handleStepError` so `fallback.retry` applies.
- Agent types accept the same two keys — frontmatter keys on a markdown agent
  (whose body must then be empty), or fields in the `.opencode.json` `agents`
  block. Resolution happens at agent construction, not registry load, so a UI
  edit reaches the next run without a restart.
- New `prompt.AgentPromptOptions.BasePrompt`, plumbed through
  `providerOptions`, because the prompt builder re-fetches the registry entry
  by name rather than reading the per-call `AgentInfo` the factory resolved
  onto — the same trap `HasOutputSchema` already documents.
- Exactly-one-prompt-source validation at load time in both places, with no
  precedence rule.
- Best-effort startup warm-up of every referenced prompt, bounded and never
  fatal.
- Config JSON schema, `docs/flows.md`, `docs/telemetry.md` and `README.md`
  gain the new surface.

## Capabilities

### New Capabilities

- `langfuse-prompt-management`: prompts for flow steps and agent types may be
  stored in Langfuse and referenced by path + label instead of inlined, with
  cached resolution at run time, exactly-one-source validation, and defined
  behaviour when Langfuse is unreachable.

### Modified Capabilities

<!-- none — no existing spec covers where a prompt's text comes from. The
     flow-api capability's include/extends requirement already declares every
     step key inheritable by default, which covers the two new keys without
     amendment. -->

## Impact

- Added: `internal/langfuse/prompts.go`, `cmd/langfuse_prompts.go`.
- Modified: `internal/config/config.go` (`LangfusePromptsConfig`,
  `Agent.LangfusePrompt*`, `ValidateAgentPromptSource`, telemetry validation),
  `internal/flow/flow.go` + `registry.go` + `service.go` (step keys,
  `validateStepPromptSource`, `PromptResolver`, `resolveStepPrompt`),
  `internal/agent/registry.go` (`AgentInfo.LangfusePrompt*`, merge
  exclusivity, markdown validation), `internal/llm/agent/factory.go` +
  `agent.go` (resolution + `withBasePrompt`), `internal/llm/prompt/prompt.go`
  (`BasePrompt`), `cmd/root.go` + `cmd/serve.go`, `cmd/schema/main.go` +
  regenerated `opencode-schema.json`, `docs/flows.md`, `docs/telemetry.md`,
  `README.md`.
- `.opencode.json` public contract: additive
  `telemetry.langfuse.prompts` object and additive
  `agents.<name>.langfusePromptPath` / `langfusePromptLabel`.
- Flow YAML contract: additive `langfusePromptPath` / `langfusePromptLabel`
  step keys.
- **Behaviour change:** a flow step declaring neither a `prompt` nor a
  `langfusePromptPath` is now a load error. It previously loaded and sent an
  empty string to the model. No flow in this repository relies on it; two
  tests that pinned template-merge semantics *through* an empty prompt were
  re-based onto other fields.
- No new runtime dependencies — the client is plain `net/http`, as there is no
  official Langfuse Go SDK.
