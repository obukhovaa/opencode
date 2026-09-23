# Redact Secrets from Telemetry Before Export

## Why

A 90-day scan of the Hetzner Langfuse (`langfuse.de-prod.cxense.com`, project `cmmvv6oi3000bui07fzm1qx7f` "C2 Agent") found **13 distinct live-shaped credentials stored in plaintext** — 5 GitLab PATs (600 occurrences), 5 Slack bot tokens, 2 AWS access key ids, 2 unclassified `sk-` keys, and the Langfuse secret key that the ingestion pipeline itself writes with ([GENAI-360](https://tinypass.atlassian.net/browse/GENAI-360)). The exposure window ran 2026-06-24 → 2026-09-22 and is still open; 44 humans can read the project, and all 3 project API keys never expire.

Two of those vectors reach **beyond** Langfuse: 300 occurrences are a PAT embedded in a `webfetch` URL, which also lands in proxy, CDN and server access logs; and one AWS key came out of `gitlab_get_file_contents`, meaning it is *also* committed in GitLab.

This is our code doing exactly what it was built to do. `internal/langfuse` attaches tool and generation input/output to every span, and the original design treated **truncation as the mitigation for sensitivity** — "tool input/output truncated to 10KB". A `.env`, a kubeconfig or an `aws configure` dump is far under 10KB and passes through whole. That assumption is the root cause.

LiteLLM's Langfuse integration force-redacts prompt content correctly (28,633/28,633 observations on the `piano-agent-*` lane read `redacted-by-litellm`), but that invariant is enforced at the *inference* boundary and Langfuse has a second ingestion door: our OTLP exporter posts straight to `/api/public/otel/v1/traces`. No amount of work on the LiteLLM side closes this. Langfuse offers no way to restrict who can see input/output within a project, so the only control that actually holds is client-side, before the span leaves the process.

## What Changes

- New `internal/redact` package: a dependency-free, compiled-once secret scanner that rewrites matched credentials in a string. Detectors are a table of `(name, regexp, capture group)` plus per-detector acceptance gates, so built-ins and user rules run through identical machinery.
- **Twelve built-in detectors, on by default**, covering every credential class the GENAI-360 scan counts ([comment 937343](https://tinypass.atlassian.net/browse/GENAI-360?focusedCommentId=937343) carries the queries): `gitlab-pat`, `slack-token`, `aws-access-key-id`, `langfuse-key`, `anthropic-key`, `github-token`, `jwt`, `private-key`, `generic-sk` (gated, see design), and three *shape-agnostic* nets — `url-userinfo`, `auth-header` and `secret-assignment` — that catch credentials whose format we have never seen. The shape-agnostic three are the highest-value detectors: in the backtest they caught `AWS_SECRET_ACCESS_KEY` and a bare Bearer key without knowing either format, and `url-userinfo` alone covers the 300-occurrence `webfetch` case.
- **Redaction runs at a single choke point** inside `internal/langfuse`, applied to every content-bearing attribute: tool input/output, generation input/output, trace input/output, span metadata values, and `langfuse.observation.status_message` — error strings leak too (a failed `git clone` reports the PAT-bearing URL it tried).
- **Redaction happens before truncation.** Truncating first can cut a token in half and leave an unmatched high-entropy fragment in the span.
- Matches are replaced with `[REDACTED:<detector>:<fingerprint>]`, where the fingerprint is the first 6 hex of the SHA-256 of the secret. That preserves the one property incident response actually needs — "this same token appears in 600 spans" — without exposing the value. Langfuse's own `display_secret_key` uses the last 3 characters for this; a hash is strictly safer and equally correlatable.
- New `telemetry.redaction` config object: `enabled` (default **true**), `mode`, `disableBuiltins`, an opt-in `pii` detector group, a user `rules` **array** of `{name, pattern, replacement, group}`, and an `allowlist` for known-safe literals. Custom rules are an array, not a map: viper case-folds map keys, so a regex or rule name used as a key would be silently mangled.
- Detectors are validated at config load. A malformed user regex fails startup with a named error rather than silently disabling redaction.
- Redaction is **re-entrant**: a payload that already contains a redaction marker is left alone rather than re-wrapped, so a marker emitted by a subagent survives into the parent's span with its detector name and fingerprint intact.
- A committed backtest corpus (`internal/redact/testdata/`) derived from the real GENAI-360 findings, asserting both directions: every known leak shape is redacted, and every known false positive is left alone.
- Docs: a `docs/telemetry.md` section on the redaction model, its defaults, and how to add a rule; `opencode-schema.json` regenerated.

### Not in scope

- PII detectors are **built but off by default**, shipped as an opt-in `pii` group. Every credential in the incident is a secret; agent telemetry meanwhile is full of *legitimate* emails (Jira assignees, git authors) and IPs (pod addresses), so redacting them by default would mangle ordinary telemetry and teach reviewers to distrust the filter.
- Redaction of `internal/logging` output, the chat bridge, and session storage. The same `internal/redact` package will serve them, but each is a separate surface with its own trust boundary.
- Rotating the leaked credentials, purging the AWS key from GitLab history, and scoping Langfuse org access — operational work tracked on GENAI-360 itself.
- Server-side masking in the `langfuse/` fork (GENAI-360 option (d)). Client-side redaction is the control we own; a server-side backstop is a worthwhile follow-up, not a substitute.

## Capabilities

### New Capabilities

- `telemetry-redaction`: client-side detection and replacement of secrets in telemetry payloads, covering the built-in detector set, the configuration surface for enabling/disabling/extending it, the single choke point that guarantees no content-bearing attribute bypasses it, and the ordering guarantee against truncation.

### Modified Capabilities

<!-- none — no existing spec covers telemetry payload capture; `telemetry.tools` / `telemetry.generations` are documented in docs/telemetry.md but have no spec file under openspec/specs/ -->

## Impact

- New: `internal/redact/` (detectors, compiler, scanner, corpus fixtures).
- Modified: `internal/langfuse/{client,span,context}.go` — every attribute write routed through one redacting helper; `internal/langfuse/policy.go` gains the redaction policy read.
- Modified: `internal/config/config.go` (`RedactionConfig` + validation), `cmd/schema/main.go` + regenerated `opencode-schema.json`, `docs/telemetry.md`.
- `.opencode.json` public contract: additive `telemetry.redaction` object.
- **Behavior change**: spans that previously carried credentials in cleartext now carry `[REDACTED:…]`. Deployments that want the old behavior set `telemetry.redaction.enabled: false`. Because telemetry capture is itself already opt-in (`telemetry.tools.enabled` / `telemetry.generations.enabled` both default false), turning redaction on by default costs nothing to anyone not already capturing content.
- Performance: measured, not assumed. The naive form (12 patterns each scanned over the whole payload) cost 106ms on a 400KB payload; candidate-position scanning brings it to **5.5ms at 400KB and 0.19ms at 10KB**. Benchmarks ship with the change.
- No new runtime dependencies — Go's `regexp` (RE2, linear time, no catastrophic backtracking) and `crypto/sha256` only.
