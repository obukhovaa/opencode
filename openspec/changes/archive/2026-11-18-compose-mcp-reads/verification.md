# Verification

## Scope

The implementation remains default-off. It adds the bounded MCP read plan and its configuration, eligibility, dispatch, projection and diagnostics contracts. No workspace or daemon is enabled by this change. Background compaction, cache warming and a general scripting VM remain excluded.

## Repository and review checks

- `GOTOOLCHAIN=go1.25.8 make test` passed with vet and repository-wide race tests, using an isolated home and bounded build parallelism.
- Focused engine/config/schema, agent/tools/permission race suites passed. Generated `opencode-schema.json` matches its generator.
- Independent reviews approved the final engine, configuration, eligibility and dispatch changes. Review findings were fixed: outer composition policy checks; concrete builtin identity instead of name-based exemptions; enabled-name collision rejection; and sticky shared-client diagnostic restrictions for raw MCP stderr and connection errors.
- Additional tests cover post-hook JSON blocks, exact numeric values through hooks/MCP and Anthropic history, inherited approval, repeated calls and denial accounting, nested permission waits, cancellation with partial envelopes, and real stdio error sentinels. Canonical raw JSON avoids the Anthropic SDK treating `json.Number` as a string.

## Real-client fixture acceptance

`scripts/test/mcp_composition.sh` runs a real OpenCode server against a local scripted Anthropic endpoint, stdio MCP tools and hooks. It inspects actual requests, session history, SSE, logs and captured OTLP telemetry with input/output recording enabled and redaction disabled. Negative cases verify that the absence of hidden data is not caused by disabled capture.

| Fixture | Ordinary result bytes | Composed result bytes | Ordinary request bytes | Composed request bytes | Scripted intermediate decisions avoided |
|---|---:|---:|---:|---:|---:|
| List/detail | 27,133 | 254 | 30,510 | 6,022 | 1 |
| Independent status reads | 27,152 | 324 | 30,689 | 6,174 | 0 |
| Nested JSON plus large hook context | 119,323 | 19,548 | 123,900 | 26,551 | 1 |

Each result satisfies the 20% gate. Aggregate final-request bytes fall from 185,099 to 38,747, including tool schema, plan input and hook context. The ordinary status baseline batches independent calls; it is not artificially serialized. Child requests, required facts, numeric types and hook context match. Eight negative/incomplete cases pass, including policy denial, error JSON, post-hook redaction/blocking, private bound arguments, partial context and row limits.

Default-off requests are byte-identical without normalization when comparing absent composition configuration with configured endpoints lacking the exact agent grant, using the same corrected binary. This is not a historical-binary comparison. Intentional prerequisite fixes to MCP error propagation and numeric fidelity also apply to ordinary calls.

## Live Kimi smoke

A fresh Go 1.25.8 binary ran against an operator-configured native Kimi provider and a local read-only MCP fixture. Two successful model requests drove exactly two child reads, preserving dependent ID `9007199254740993`. SQLite contained one outer composition call/result, no tool errors, the correct hidden nonce and no raw child canary. Captured provider requests also excluded the canary. Credential-bearing temporary configuration/logs were removed and the relay stopped.

Binary SHA256: `fb8350ddb0eeba1b73aa93a31e380e1f399e63e50afb31c215c73347c18d585f`. Subsequent edits were documentation/spec changes only.

## Limits of the evidence

These tests establish protocol correctness, control preservation and context-volume reduction on selected fixtures. They do not measure production token cost, latency, or general model task quality. A separately authorized single-agent pilot and representative paired evaluations are still required before wider enablement. The existing hook-context and MCP transport-allocation limitations remain documented in the MCP guide.
