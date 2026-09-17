# Design: struct-output-schema-message-delivery

## Research

### What the provider actually sends

`anthropicClient.preparedMessages` builds one request with `Tools`, `System`, and
`Messages`, and places cache breakpoints in three places:

| Position | Site | Breakpoint |
|---|---|---|
| `tools[]` | `convertTools` (`internal/llm/provider/anthropic.go`) | on the last non-deferred tool |
| `system` | `preparedMessages` | on the single system text block |
| `messages[]` | `convertMessages` | on the last content block of each of the last two messages |

Anthropic's documented rule: the cached prefix renders as `tools` → `system` →
`messages`, and any byte change invalidates everything after it. Breakpoints
placed after a changed byte can never hit — they are read points into one prefix,
not independent caches. So a differing tool block does not cost us the tool block;
it costs us the whole request.

### Where the differing bytes come from

`NewStructOutputTool(schema)` stores the step's schema and `buildParamsFromSchema`
splays its `properties` straight into `ToolInfo.Parameters`, with `required`
alongside. `internal/llm/agent/tools.go` constructs it from
`info.Output.Schema`, which `agentFactory.NewAgent` sets per call from the flow
step's `output.schema`:

```go
if outputSchema != nil {
    infoCopy.Output = &agentregistry.Output{Schema: outputSchema}
}
```

Two steps, same agent, different `output.schema` → different `Parameters` →
different `tools[]` bytes → cold prefix. Nothing else in the prefix varies between
consecutive steps of one agent: the system prompt's structured-output appendix is
a fixed string (`structuredOutputPrompt`) that mentions no schema, tool ordering is
deterministic (`OrderTools`: baseline in construction order, external sorted by
name), and the rest of the tool set is identical.

This also explains why the symptom is "the *first* call of each step": within a
step the tool block is stable, so turns 2..N hit the cache normally.

### Why this is safe to move — the finding that de-risks the whole change

**No provider in this repo sets `strict: true`.** Confirmed in all three tool
converters (`anthropic.go`, `openai.go`, `gemini.go`): none emit a strict flag,
none set `additionalProperties: false`. Without strict mode the `input_schema` is
*advisory guidance to the model*, not a decoder constraint — the API does not
reject a tool call whose input violates it, and nothing validates it but our own
`Run`.

So the schema is already "just text the model reads". Moving it from the tool
block to the message tail changes *where the model reads it*, not whether anything
enforces it. The enforcement that exists today — `applyDefaults` +
`missingRequiredFields` in `struct_output.Run`, returning an error tool result that
re-enters the agent loop — is untouched and keeps working on the full schema.

### Precedent in this codebase

`injectDeferredDelta` already does exactly this shape of thing: a synthetic
`message.User` message, wrapped in `<system-reminder>`, appended to `msgHistory`
after the user turn, deduped against history by a marker string, injected once per
session rather than once per turn. Consecutive user messages are therefore already
in production on every provider we ship, which removes the only real unknown about
message placement.

## Options considered

### A. Defer `struct_output` via `deferredTools` (the first idea on the thread)

Mark `struct_output` deferrable so its schema leaves the prefix.

Rejected. On the native path the tool is stripped from the prefix until the model
searches for it, which buys a discovery round-trip on the single most important
tool of every step — and on the fallback path an activated tool is appended *after*
the tools breakpoint, so `system` and `messages` still never cache. It also
inverts a deliberate invariant: `toolsearch` and `struct_output` are hard
exclusions from deferral (`openspec/specs/deferred-tools/spec.md`), precisely
because a step that cannot find its output tool cannot finish. The thread's own
framing — "moving all tool description to deferred could reduce effectiveness of
this important tool" — is correct.

### B. Move the tools breakpoint before `struct_output`

Order `struct_output` last and put the breakpoint on the tool before it.

Rejected. The tools block would cache; `system` and every message after it would
not, because they sit after the changed bytes in the same prefix. On a forked step
the message history *is* the bulk of the tokens, so this fixes the cheap part and
leaves the expensive part cold.

### C. Structured outputs (`output_config.format`)

Rejected. Constrains the final assistant text, not a tool call, and does not
compose with an agentic loop that must keep calling other tools. A much larger
change for a narrower result.

### D. Static tool + schema in the message tail — **chosen**

Keeps the tool's identity in the prefix (so the model always knows the tool exists,
what it is for, and that it must be called) and moves only the volatile part — the
schema — past the last breakpoint. This is the textbook shape for this problem:
stable content first, volatile content after the last breakpoint.

It also fixes the non-forked case for free. A fresh step session shares `tools` and
`system` with the previous step's session; today those differ, so even a fresh
session pays a cold prefix. After this change it hits.

## Decisions

**D1 — The static parameter surface is `{"output": {"type": "object"}}`, required.**

Two shapes were available. An empty `properties: {}` object would preserve today's
flat call shape and need no unwrapping, but it declares "this tool takes no
arguments" while the envelope says the opposite — a direct contradiction in the
model's context, and the one thing most likely to cause the regression this change
must not cause. The `output` wrapper declares one object argument and lets the
envelope describe its shape; it is also a shape `buildParamsFromSchema` already
produces today for non-object schemas, so it is not a new path for the provider
converters.

`Run` unwraps `output` before returning, so the tool *result* — and therefore every
downstream consumer: flow routing on `${args.<field>}`, `IsStructOutput` persistence,
the TUI renderer, `captureStructOutput` — sees the same bare object it sees today.
`additionalProperties` is left open; a model that emits the document flat instead
of wrapped is accepted rather than failed (see D5).

**D2 — The envelope is a synthetic user message, injected after the user turn.**

Mirrors `injectDeferredDelta` exactly: `message.User`, `Synthetic: true`,
`<system-reminder>`-wrapped, appended to `msgHistory` after `createUserMessage`.
Appending after the user turn (rather than concatenating into the prompt text)
means one code path covers the auto-resume case where there is no user turn at all,
and puts the schema at the very tail where it is most salient and cheapest.

**D3 — Dedup is by schema fingerprint, against the history actually being sent.**

The envelope opens with `<struct_output_schema fingerprint="<12 hex>">`. A run
scans the message list it is about to send for that exact fingerprint token and
injects only when absent. Two consequences fall out of "the history actually being
sent" rather than "the session's whole history":

- On a **forked** step the copied history carries the *previous* step's envelope
  with a *different* fingerprint, so the new one is injected. The envelope text
  states that it supersedes any earlier schema, so the stale one is unambiguous.
- After **compaction** `filterMessagesFromSummary` drops the envelope; the next run
  sees it missing and re-injects. Without this a compacted step would lose its
  schema entirely — a bug the current design cannot have, and one this design must
  not introduce.

The fingerprint is `sha256` over `json.Marshal` of the schema map, truncated to 12
hex characters. Go's `encoding/json` sorts map keys, so the encoding is canonical
without extra work.

**D4 — Delivery mode is config, defaulting to `message`.**

`structOutputSchemaDelivery: "message" | "tool"`, top-level and per-agent
(agent wins). `"tool"` restores the previous behavior exactly. The gate on this
change is "no regression in the model's ability to produce proper step output";
a config-level revert is what makes that gate enforceable in production rather
than only in tests.

**D5 — Unwrapping is lenient.**

If `output` is present and is an object, that is the document. If `output` is
absent and the top-level payload has the schema's own shape, the payload *is* the
document (the model emitted flat). Only a payload that is neither produces an error
result — with a message naming the expected shape — which re-enters the loop for a
retry exactly as a missing-required-field rejection does today. Leniency here is
cheap and removes the sharpest edge of the change.

## Risks

| Risk | Mitigation |
|---|---|
| Model adherence drops with the schema in the tail | No provider sets `strict`, so the schema was already advisory; `Run` still validates and rejects, the flow runner still re-prompts, and `structOutputSchemaDelivery: "tool"` reverts |
| Model wraps when it should not, or vice versa | D5 accepts both shapes |
| Forked history carries a stale envelope | Fingerprint dedup injects the new one; envelope text declares it supersedes earlier schemas |
| Compaction drops the envelope | Presence check runs post-summary, so the next run re-injects |
| Envelope re-injected every turn, wasting tokens | Fingerprint dedup; injected once per session per distinct schema |

## Verification

Cache behavior is verified structurally rather than by hitting the API: a test
builds the provider request for two steps whose schemas differ and asserts the
serialized `tools` block and `system` block are byte-identical, and that the
breakpoint positions are unchanged. That is the property that makes the cache hit;
asserting it directly is both cheaper and more durable than asserting a
`cache_read_input_tokens` figure from a live call.
