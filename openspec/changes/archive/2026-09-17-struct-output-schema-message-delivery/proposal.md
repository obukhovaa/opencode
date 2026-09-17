# Proposal: struct-output-schema-message-delivery

## Why

The first LLM call of every flow step is a prompt-cache miss.

Anthropic renders the cacheable prefix in the order `tools` → `system` → `messages`,
and a single changed byte anywhere in that prefix invalidates everything after it.
The `struct_output` tool's `input_schema` is built from the *step's* `output.schema`
(`buildParamsFromSchema`, `internal/llm/tools/struct_output.go`), so two consecutive
steps driven by the same agent ship byte-different tool blocks. That one differing
tool definition sits at position 0 of the prefix and takes the whole prefix down
with it — the tool list, the system prompt, and the entire message history.

The cost lands hardest exactly where it should be cheapest. A step with
`session.fork: true` copies the previous step's messages verbatim; every one of
those bytes was already sent to the model moments earlier, and every one of them is
re-processed at full input price because the tool block ahead of them changed.

Steps were designed before session forking existed, when each step really was a
fresh session and a cold prefix was unavoidable. Forking removed that constraint;
the schema-in-the-tool-block did not.

## What Changes

- **The `struct_output` tool definition becomes step-invariant.** Its name,
  description, and parameter surface are identical for every agent, every step,
  and every schema: one `output` parameter of type `object`, required. Nothing
  derived from the step's schema reaches the tool block.
- **The per-step JSON Schema moves into the message tail.** It is injected as a
  `<struct_output_schema>` envelope in a synthetic user message appended after the
  step's prompt — after the last cache breakpoint of the prefix, where new bytes
  cost what new bytes should cost.
- **The envelope is injected once per session, not once per turn.** It carries a
  fingerprint of the schema; a run that finds that fingerprint already in the
  history it is about to send injects nothing. This mirrors the existing
  `injectDeferredDelta` contract exactly.
- **Compaction re-arms it.** The presence check runs against the post-summary
  message list, so a compaction that drops the envelope causes the next run to
  re-inject it rather than leaving the model to guess the shape.
- **`struct_output` validates against the real schema as it already did.** The
  tool keeps the full schema in memory; `Run` unwraps the `output` argument,
  materializes declared defaults, and rejects a payload missing required fields
  with the same error-result-and-retry behavior as today.
- **An escape hatch ships with it.** `structOutputSchemaDelivery` (`"message"` by
  default, `"tool"` for the previous behavior) is settable globally and per agent,
  so a regression is revertible by config rather than by rebuild.

## Non-goals

- Changing when or how often `struct_output` is called, or the flow runner's
  existing missing/rejected-`struct_output` re-prompt.
- Changing what the emitted document looks like to any downstream consumer. The
  tool result is the bare schema-conformant object, exactly as today, so flow
  routing (`${args.<field>}`), the TUI renderer, and the retry path are untouched.
- Cache breakpoint placement. The breakpoints stay where they are; this change is
  about keeping the bytes ahead of them stable.
- Mid-conversation `role: "system"` messages. They are the doctrinally cleaner
  channel for an operator instruction like this one, but they are model-gated
  (Opus 5 / 4.8 and the Fable family; not Sonnet 5) and the provider layer has no
  representation for them. A user-role envelope has the same caching profile and
  works on every provider we ship.

## Impact

- `internal/llm/tools/struct_output.go` — delivery mode, static tool surface,
  `output` unwrapping, schema fingerprint, envelope rendering.
- `internal/llm/agent/agent.go`, `internal/llm/agent/tools.go` — envelope
  injection on the run path; delivery mode threaded to the tool constructor.
- `internal/config/config.go`, `cmd/schema/main.go`, `opencode-schema.json` —
  the new config field and its regenerated schema.
- `docs/flows.md`, `docs/agents.md`, `AGENTS.md` — user-facing documentation.

No database, API, or flow-YAML changes. A flow that declares no `output.schema`
is byte-for-byte unaffected.
