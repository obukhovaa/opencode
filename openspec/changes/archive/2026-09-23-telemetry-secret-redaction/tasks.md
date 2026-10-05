## 1. Backtest corpus first

- [x] 1.1 Create `internal/redact/testdata/corpus.md` from the GENAI-360 synthetic corpus, partitioned into three sections: bare must-redact values, must-redact values embedded in realistic tool output (URL, env dump, nested JSON, escaped JSON, curl header, line-wrapped token), and must-not-redact false positives. Header must state the values are synthetic and safe to commit.
- [x] 1.1b Defuse every must-redact value before committing it, preserving length, character class and segment structure exactly (design D0). Embed `EXAMPLE` where the character class allows letters — `AKIAIOSFODNN7EXAMPLE` is still `AKIA` + 16, which is why AWS uses it in its own docs — and a constant filler such as `deadbeef-0000-4000-8000-000000000000` where it does not. Re-check `sk-_FGTcQOmXr4Kd9Wb2Nv7Lp` specifically: its replacement must keep an uppercase letter and no lowercase-alpha segment of length ≥2, or it silently stops exercising the `generic-sk` gate. Leave section 3 verbatim.
- [x] 1.2 Write the corpus loader plus the two-directional table test (`corpus_test.go`) that asserts every must-redact entry is replaced and every must-not-redact entry is returned byte-identical. It fails on an empty detector set — write it before the detectors exist so it is red first.
- [x] 1.3 Record in the corpus file that `glpat-7Fq2MxVn8KpLdR4T` is 22 characters (16-char body) despite the upstream "26ch" label, so nobody "fixes" the fixture to match its comment.

## 2. `internal/redact` core

- [x] 2.1 Define `Detector` (name, compiled `*regexp.Regexp`, capture group, newline-tolerant flag, optional accept func) and `Redactor` (ordered detectors, allowlist set, mode). No dependency on `internal/config` or `internal/langfuse` — the package compiles from a plain options struct so it stays independently testable.
- [x] 2.2 Implement the scan/merge/replace pass: collect all candidate spans, resolve overlaps by longest-span-wins with ties broken by detector declaration order, merge rather than nest, emit one marker per resolved span. Add a test asserting `SLACK_BOT_TOKEN=xoxb-…` yields exactly one marker.
- [x] 2.3 Implement marker formatting for all three modes — `fingerprint` (default, `[REDACTED:<name>:<first 6 hex of SHA-256>]`), `strict` (`[REDACTED:<name>]`), `remove` (empty). Test that the same secret yields a stable fingerprint across calls and two secrets differ.
- [x] 2.4 Implement the allowlist check, applied after matching and before replacement.
- [x] 2.5 Implement protected regions: pre-scan the payload for existing `[REDACTED:…]` markers and drop any candidate span overlapping one. Add the idempotence test (`redact(redact(x)) == redact(x)`) plus a case where a marker sits in a URL's userinfo — without the guard `url-userinfo` re-wraps it and destroys the inner detector name and fingerprint (design D6b).

## 3. Built-in detectors

- [x] 3.1 Add the prefix-shape detectors: `gitlab-pat` (body `{16,}` per design D4), `slack-token`, `aws-access-key-id` (all documented AWS prefixes, not just AKIA/ASIA), `langfuse-key` (both `sk-lf-` and `pk-lf-`), `anthropic-key`, `github-token`. Order them specific-before-generic — GENAI-360 comment 937343 records the scan hitting this: a generic `sk-` placed first swallows Langfuse keys.
- [x] 3.1b Add `jwt` and `private-key`, the two classes from the scan's Q1 that the first draft missed. `jwt` requires the **second** segment to also start `eyJ` (a real JWT's payload is base64url JSON) — the scan's looser pattern matches ordinary text like `eyJust-a-word.and-another-thing`, which is acceptable in a scan a human reviews and not in a filter that rewrites silently. `private-key` uses `(?s)` and requires the END marker so a lone BEGIN marker in prose is not redacted.
- [x] 3.2 Add the three positional detectors, each redacting a capture group only: `url-userinfo`, `auth-header`, `secret-assignment`. These must use capture groups rather than lookaround — RE2 supports neither, and the natural `(?<=://)…(?=@)` form does not compile (design D5b). Verify each catches a credential whose own shape matches no other detector.
- [x] 3.3 Add `generic-sk` with the acceptance gate from design D3: accept iff body ≥40 chars, OR (body has an uppercase letter AND no `-`/`_`-delimited segment of length ≥2 is pure-lowercase alphabetic). Add a unit test per section-3 false positive, and an explicit test asserting no Shannon-entropy threshold is used as the discriminator.
- [x] 3.4 Add newline tolerance to the prefixed detectors from 3.1 only, as an alternation with the **wrapped branch first** (design D10) — leftmost-first semantics make the unwrapped branch win otherwise and drop the continuation. Assert the wrapped-token corpus case is caught whole; assert `glpat-AAAAAAAA\n\n\nnext paragraph` does not consume the blank lines (the character-class draft did); assert `generic-sk` and the positional three do NOT cross a newline.
- [x] 3.5 Add the `pii` detector group (email, IPv4/IPv6, phone), registered but inactive unless enabled. Test that default configuration leaves an email and an IP untouched and enabling the group redacts both.
- [x] 3.6 Run the corpus test from 1.2 — it must now be green in both directions.

## 4. Configuration

- [x] 4.1 Add `RedactionConfig` to `internal/config/config.go` (`Enabled *bool` so an unset value can default to true and an explicit `false` is distinguishable; `Mode`, `DisableBuiltins`, `PII`, `Rules []RedactionRule`, `Allowlist`) and hang it off `TelemetryConfig`. `Rules` is an array of objects — never a map keyed by a user-supplied name (design D8).
- [x] 4.2 Extend `validateTelemetryConfig`: compile every rule pattern, reject an empty rule name, reject a capture-group index the pattern does not define, reject an unrecognised `mode`. Each error names the offending rule. When compilation fails on a lookaround construct, say so explicitly rather than surfacing Go's raw parser error — it is the most likely first-contact papercut for a rule author. Add table tests for each rejection.
- [x] 4.3 Add the viper round-trip test required by `CLAUDE.md`, asserting a rule declared with an uppercase-bearing name and a case-sensitive pattern (e.g. `PI-[0-9]{12}`) survives `viper.Unmarshal` character-for-character. This is the test that catches the map-key folding trap, so it must exercise the real loader, not `json.Unmarshal`.
- [x] 4.4 Add a default-resolution test: absent config yields redaction enabled with built-ins active and `pii` off; `enabled: false` yields a pass-through redactor.

## 5. Wire into telemetry

- [x] 5.1 Add the redactor accessor to `internal/langfuse/policy.go`, building the `redact.Redactor` from `config.Get()` once and caching it (config is immutable after load). Nil-safe on an unloaded config, matching the existing policy functions.
- [x] 5.2 Add the `payload(v any, max int) string` helper in `internal/langfuse` = `truncate(redact(marshalAny(v)), max)`, and route `TraceStart` (input + metadata values), `GenerationStart` (input + metadata values), `ToolStart` (input), `span.setOutput`, `span.SetError` (status message) and `context.SetTraceOutput` through it. Confirm by grep that no `attribute.String` on a content-bearing key bypasses the helper.
- [x] 5.3 Add a test asserting redact-happens-before-truncate: a payload whose credential straddles the cap boundary exports no fragment of it (design D2).
- [x] 5.4 Add a test asserting the diagnostic attributes — span name, tool name, timings, usage, cost, model name, session id, user id, observation type — are byte-identical with redaction on and off.
- [x] 5.5 Add an end-to-end test over an in-memory span exporter: run a fake tool call whose output carries several corpus credentials and assert the exported span attributes carry markers and no cleartext.

## 6. Schema, docs, and final checks

- [x] 6.1 Declare `telemetry.redaction` in `cmd/schema/main.go` with types, descriptions, defaults and the `mode` enum; regenerate with `make schema` and commit `opencode-schema.json`.
- [x] 6.2 Add `mode` values to the `probe` corpus in `cmd/schema/main_test.go` so the enum-vs-validator drift test covers them (required by `CLAUDE.md`; regeneration alone cannot catch this).
- [x] 6.3 Add a `docs/telemetry.md` section: what is redacted and what is deliberately not, the built-in detector table, the default-on posture and how to switch it off, how to add a rule, the `pii` opt-in, and the marker/fingerprint format with a note that fingerprints let you correlate occurrences without exposing values.
- [x] 6.4 Add `scripts/test/redaction.sh` (driving `cmd/redaction-e2e`) per `CLAUDE.md`: a self-contained sandbox that writes an `.opencode.json` with a custom rule, runs the binary against a payload carrying corpus credentials, and asserts the exported span content carries markers — the cross-process viper round-trip that unit tests cannot cover.
- [x] 6.5 Add a benchmark over a 400KB payload. **Outcome: the <1ms budget was missed and the design was corrected rather than the scope narrowed.** Naive implementation measured 106ms/400KB; candidate-position scanning (design D12) brings it to 5.5ms adversarial / 4.7ms realistic / 0.19ms at 10KB. Narrowing to tool payloads was rejected — it would leave generation payloads unredacted, which is the leak this change closes.
- [x] 6.6 Run `make test` and `./scripts/check_hidden_chars.sh`.

## 7. Verification against the real incident

- [ ] 7.1 After deploy, re-run the ClickHouse scan from GENAI-360 comment 937343 (Q1 counts by class, Q2 per-match fingerprints) over a window starting at the deploy timestamp; expect zero hits across Q1's eight classes and a population of `[REDACTED:…]` markers. Report the result on GENAI-360.
- [ ] 7.2 Record any shape the scan finds that the built-ins missed, and add it either as a built-in (if broadly applicable) or as a documented `rules` entry.
