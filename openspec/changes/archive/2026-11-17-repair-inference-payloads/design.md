## Context

See GENAI-671 and proposal.md. The compatible adapter already emits display thinking deltas, while the message store supports authoritative reasoning parts on completion. Bash already saves oversized output, but its preview ignores the byte limit. The current production Claude path must retain its signed-thinking and cache behavior.

## Goals / Non-Goals

Repair the two reproduced contracts and add permanent regressions. No model changes, new thinking budgets, global reasoning enablement, background compaction, cache warming, or MCP execution engine in this change.

## Decisions

- Reuse persisted reasoning parts rather than embedding reasoning into normal assistant text. Capture supported JSON string reasoning for both stream and nonstream responses; retain the observed field plus provider, API model and route provenance. A changed endpoint under the same alias is a route change. Do not coerce arrays/objects/null, flatten opaque/signed formats, or fabricate missing reasoning.
- Send compatible reasoning only where its original format and origin are supported. Ordinary first-party requests without compatible reasoning retain their existing body. Native Claude signing/redaction and cache serialization remain unchanged.
- Keep JSON message storage backward-readable; use optional metadata rather than a database migration. Legacy records without adequate provenance remain readable and must not be replayed speculatively as signed/provider-specific data.
- Keep Bash's existing spill path, line clipping and exit metadata. Apply a byte bound to each stdout/stderr excerpt as well as the line bound; the fixed-size truncation notice/path is separate overhead, as for existing MCP previews. Preserve UTF-8 boundaries and clearly report failed or capped persistence.
- Test the real request boundary after storage round trips, not only conversion helpers. Use a local fake provider so tests are deterministic and do not incur model charges.

## Risks / Trade-offs

- Reasoning replay can add input tokens → verify correctness first; measure net cost including reduced retries/output before claiming savings.
- Provider fields and proof formats vary → constrain replay by provenance and cover first-party GPT, incompatible model/provider switches, and native Claude regressions.
- Byte clipping removes visible data → keep the full spill available within the existing persistence cap and expose accurate retrieval instructions.

## Migration Plan

Ship a reviewable PR with optional backward-readable message metadata and no configuration rollout. No production change is applied by opening the PR. MCP composition and its measured quality gate remain a separate GENAI-671 wave.
