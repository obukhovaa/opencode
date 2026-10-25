## Context

The native Kimi provider uses the shared Anthropic client; model capability flags control request shaping. Existing K3 config resolution pins unset effort to max. See proposal.md for motivation.

## Goals / Non-Goals

Goals: register both native models using the existing transport and official prices. No Bedrock mirrors while AWS does not list these variants; no changes to older model prices or defaults.

## Decisions

- Add two KimiModels entries and include them in the K3 effort-family predicate. Keep existing provider defaults to avoid silently switching existing installations.
- Store default 5-minute cache-write prices in CostPer1MInCached and read prices in CostPer1MOutCached, following CalculateCost's established field semantics. The client does not request one-hour TTL.
- Reuse adaptive thinking and vision handling. Verify registry, resolved effort, HTTP request shape, and cost accounting; smoke-test both native API model IDs using existing credentials held only in memory.

## Risks / Trade-offs

- Compatibility endpoint behavior may differ from Chat Completions → exercise real requests and preserve reasoning replay.
- Official rates change → record sources and verification date (2026-10-25): https://platform.kimi.ai/ and https://platform.kimi.ai/docs/guide/kimi-k3-1-pro-quickstart . AWS absence checked at https://docs.aws.amazon.com/bedrock/latest/userguide/model-cards-moonshot-ai.html .

## Verification limitation

Live attempts with the existing private Kimi Code subscription credentials reached the configured endpoint, which rejected both paid API model IDs and required its `k3` alias. The official Moonshot native endpoint rejected those credentials with HTTP 401. No credentials or private endpoint details were recorded. The user explicitly accepted finishing with hermetic mock e2e and documenting this limitation. `scripts/test/config_env.sh` verifies both model IDs through config.Load, Viper, actual native-provider HTTP requests, effort/defaults, and writeback without external services.
