## Context

See `proposal.md` — Why. Design-relevant current state:

**The capture path is already a narrow seam.** A grep for `langfuse.observation.*` / `langfuse.trace.*` attribute writes across `internal/` and `cmd/` returns hits in exactly three files, all inside `internal/langfuse`:

| Site | Attribute(s) written |
|---|---|
| `client.go` `TraceStart` | `langfuse.trace.input` or `langfuse.observation.input`; `langfuse.trace.metadata.*` |
| `client.go` `GenerationStart` | `langfuse.observation.input`; `langfuse.observation.metadata.*` |
| `client.go` `ToolStart` | `langfuse.observation.input` |
| `span.go` `setOutput` | `langfuse.observation.output` (tool **and** generation) |
| `span.go` `SetError` | `langfuse.observation.status_message` |
| `context.go` `SetTraceOutput` | `langfuse.trace.output` or `langfuse.observation.output` |

Every one already funnels its value through `truncate(marshalAny(v), max)`. That shared helper pair is the interception point, and nothing outside the package can write a content attribute — so a fix applied there cannot be bypassed by a future caller.

**Capture is already policy-gated.** `policy.go` resolves `telemetry.tools` / `telemetry.generations` through one matcher. Redaction is a second, independent layer: capture policy decides *whether* a payload is attached, redaction decides *what it may contain*. They compose; neither substitutes for the other.

**A real backtest corpus exists.** Stefan Dingler supplied a synthetic corpus whose shapes and lengths mirror the actual GENAI-360 findings, in three sections: must-redact bare tokens, must-redact embedded in realistic tool output (URL, env dump, nested and escaped JSON, curl header, line-wrapped token), and — most valuable — **must-not-redact strings taken from the live corpus** that a naive `sk-[A-Za-z0-9_-]{20,}` matches. Those are ordinary text: `task-`/`risk-`/`disk-` fragments followed by hyphenated English.

Every decision below was prototyped and run against that corpus **in Go** before being written down, extended with the two classes from the scan's Q1 and with adversarial negatives (a blank-line run after a short prefix, a lone PEM BEGIN marker, `eyJust-a-word.and-another-thing`). The final prototype passes 32/32 in both directions. Porting it to Go rather than trusting the Python draft is what surfaced D5b and D6b, both of which would have been implementation-time surprises.

## Goals / Non-Goals

**Goals:**

- No credential of a known shape leaves the process in a telemetry span, regardless of which attribute carries it.
- Zero false positives on the section-3 corpus. A filter that mangles ordinary tool output gets switched off, and then it protects nothing.
- Unknown credential shapes are still caught when they appear in a recognisable *position* (a URL's userinfo, an `Authorization` header, a `*_TOKEN=` assignment).
- Extensible without a fork change: a team can add a shape via `.opencode.json`.
- Diagnostic value preserved — a redacted span still shows which detector fired and lets you tell two distinct tokens apart.

**Non-Goals:**

- Detecting *arbitrary* high-entropy strings. See the entropy decision below: on this corpus it provably does not work.
- Being a secrets scanner for source code (gitleaks/trufflehog territory). This runs in a hot telemetry path and optimises for low FP and bounded cost, not recall over every known provider.
- Guaranteeing that a *partial* secret never survives. A token split across a truncation boundary or mangled by an intervening escape may leave a fragment; we minimise this (redact-before-truncate, newline tolerance) rather than claim it impossible.

## Decisions

### D0. The corpus is committed defused, not verbatim

The backtest corpus is the heart of this change, and it ships in the repository. Every must-redact value in it is a structurally valid credential — which is exactly what secret scanners hunt. GitHub push protection is a platform feature independent of CI workflows (this repo runs none: `build`, `release`, `schema`, `test-mysql`), and the GitHub→GitLab push mirror lands the same blobs where group-level secret detection may be enabled. A blocked push or a stream of dismissable alerts on a *security* change is a bad outcome and an avoidable one.

So must-redact values are **defused before committing**, preserving length, character class and internal segment structure so detector coverage is untouched. Where the character class admits letters, embed `EXAMPLE` — AWS's own documentation uses `AKIAIOSFODNN7EXAMPLE`, which is still `AKIA` + 16 characters. Where it does not (a hex Langfuse key), use a recognisable constant such as `deadbeef-0000-4000-8000-000000000000`.

Three carve-outs, all load-bearing:

- **Section 3 stays verbatim.** Those strings are real — Stefan's memo describes them as "actual strings from the C2 Agent project" — and they are real precisely because they are *not* credentials: `sk-for-sync-with-upstream-id` is a fragment of `task-for-sync-with-upstream-id`. Their exact characters are the false-positive regression. Rewriting them would defeat the test and protect nothing.
- **An external scanner gets the final say.** Defusing that preserves a shape exactly can still be rejected: GitHub push protection blocked the first push of this change over the two Slack samples, whose production form carries two long digit runs that the scanner matches regardless of an `EXAMPLE` tail. Those runs were replaced with `EXAMPLE`, changing the value's length — a deliberate deviation from "preserve length", recorded in the corpus, and acceptable because our own `slack-token` pattern keys on alphanumeric segments of two or more with no digit or length requirement, so it stays fully exercised.
- **Defusing must not disturb an acceptance gate.** `generic-sk` (D3) decides on letter case and segment shape, not on the literal value, so a careless replacement can silently stop exercising it — swap `sk-_FGTcQOmXr4Kd9Wb2Nv7Lp` for an all-lowercase filler and the gate's most important positive case evaporates while the test still passes. The replacement must keep an uppercase letter and no lowercase-alpha segment of length ≥2.

### D1. Redact at `marshalAny`/`truncate`, not at each call site

Introduce one unexported helper in `internal/langfuse`:

```go
func payload(v any, max int) string { return truncate(redactString(marshalAny(v)), max) }
```

and route all six sites through it. Metadata values (`fmt.Sprint(v)`) and `SetError`'s `err.Error()` go through `redactString` too.

*Alternative considered:* redact in the OTLP exporter via a `SpanProcessor`, catching attributes generically. Rejected — it would also scan attributes we know are safe (`langfuse.session.id`, model names, usage JSON), it puts the security control in the least-visible layer, and a `SpanProcessor` sees attributes after `attribute.String` has already copied the string, so the cleartext has already been materialised in the batch queue. Doing it at the seam keeps the cleartext lifetime as short as it can be and makes the control readable by anyone reading the package.

### D2. Redact **before** truncate

Ordering is load-bearing, not incidental. Truncate-first cuts a token at an arbitrary byte, and the surviving prefix no longer matches its detector — so a 10KB-capped tool output could ship `glpat-3nK7Qw9xTbVm2LpR8sJ` in the clear at the cut point. Redact-first also makes output deterministic: the replacement is shorter than most secrets, so redaction can only *reduce* pressure on the cap.

### D3. Entropy is not a usable discriminator here — measured, not assumed

Stefan's memo offers "require ≥40 trailing chars **OR** an entropy floor" for the generic `sk-` case. Shannon entropy over the token body was measured across the corpus:

| token | body entropy | verdict |
|---|---|---|
| `glpat-7Fq2MxVn8KpLdR4T` | **4.00** | real key |
| `sk-for-sync-with-upstream-id` | **4.00** | false positive |

Identical. Any floor placed between them is arbitrary; a floor at 4.0 simultaneously misses a real GitLab PAT and admits a false positive. **No entropy gate is specified.**

What *does* separate them cleanly, across all 19 relevant samples:

| signal | must-redact (11) | must-not-redact (8) |
|---|---|---|
| uppercase chars in body | ≥6, except the two `*-lf-` UUID keys | **0 for all 8** |
| pure-lowercase-alpha segments (len ≥2, split on `-`/`_`) | **0 for all** | ≥1 for all 8 |
| body length | up to 44 | max 29 |

So the generic `sk-`/`pk-` detector accepts a candidate iff:

> `len(body) >= 40` **OR** (`body` contains an uppercase letter **AND** no `-`/`_` segment of length ≥2 is pure-lowercase alphabetic)

The lowercase-segment test is a cheap stand-in for Stefan's "a dictionary word right after the prefix means false positive" without shipping a dictionary. The `>= 40` branch is safe because the longest false-positive body in the corpus is 29 characters. The two `*-lf-` keys are all-lowercase hex and would fail this gate — they are matched by the specific `langfuse-key` UUID detector instead, which is why prefix-specific detectors must run and be allowed to win regardless of the generic gate.

*This gate applies only to the generic detector.* `glpat-`, `xoxb-`, `AKIA`, `ghp_` and friends are specific enough to redact unconditionally; gating them would reintroduce the misses above.

### D4. Corrected minimum length for `gitlab-pat`

The corpus labels `glpat-7Fq2MxVn8KpLdR4T` as "26ch (legacy)"; it is in fact 22 characters — a 6-char prefix and a **16**-char body. A `glpat-[A-Za-z0-9_-]{20,}` pattern (the obvious reading of GitLab's documented 20-char token) **misses it**. The built-in uses `{16,}`.

Note for whoever implements: the fixture is right and the label is off. Do not "fix" the fixture to match its comment.

### D5. Three shape-agnostic positional detectors

Shape detectors only catch credentials whose format we already know, and GENAI-360 contains two unclassified `sk-` keys precisely because that set is never complete. Three detectors match on *position* instead, each redacting only a capture group:

| detector | catches | group |
|---|---|---|
| `url-userinfo` | `https://oauth2:<anything>@host` | the password |
| `auth-header` | `Authorization: Bearer/Basic/Token <x>` | the credential |
| `secret-assignment` | `<NAME matching TOKEN\|SECRET\|PASSWORD\|API_KEY\|ACCESS_KEY\|PRIVATE_KEY\|CREDENTIAL>=<value>` | the value |

In the prototype these caught `AWS_SECRET_ACCESS_KEY=wJal…` and a bare `Bearer sk-_FGTc…` with no knowledge of either format, and `url-userinfo` covers the single largest finding in the ticket — 300 occurrences of a PAT inside a `webfetch` URL. They also degrade gracefully: a future provider's token is caught the moment it appears next to the word `TOKEN`.

The assignment detector deliberately matches the *name* case-insensitively but anchors on a `[=:]` separator and requires ≥6 value chars, so prose like "the access key is rotated" does not match.

### D5b. Every pattern must be RE2-compatible — no lookaround

Go's `regexp` is RE2: it has no lookahead and no lookbehind. This is not a stylistic note — the *obvious* formulations of two built-ins use them and fail to compile:

```
(?<=://)[^/\s:@]+:([^/\s@]+)(?=@)      → error parsing regexp: invalid named capture
glpat-(?:[A-Za-z0-9_-]|\r?\n(?=...))     → invalid or unsupported Perl syntax: `(?=`
```

Both were caught by compiling the prototype under Go rather than trusting a Python draft. The RE2-safe forms rely on **capture groups instead of assertions**: match the surrounding context as part of the pattern and replace only the group. `url-userinfo` becomes `://[^/\s:@]+:([^/\s@]+)@` with group 1 as the credential — the `://` and `@` are consumed by the match and re-emitted unchanged.

This constraint applies to operator `rules` too, and is the most likely reason a hand-written rule fails validation. The error message should say so.

### D5c. Two more classes from the scan: `jwt` and `private-key`

GENAI-360's Q1 counts eight credential classes; two of them — JWTs and PEM private-key blocks — were absent from the first detector draft and are added.

The JWT pattern is deliberately **tighter than the scan's**. The scan uses `eyJ[A-Za-z0-9_-]{10,}[.][A-Za-z0-9_-]{10,}`, which also matches ordinary text such as `eyJust-a-word.and-another-thing`. A real JWT's second segment is a base64url-encoded JSON object, so it too begins `eyJ`; requiring that kills the false positive and costs no real token:

```
\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_-]+)?
```

The divergence is deliberate and generalises: **a scan and a filter have different false-positive budgets.** A noisy scan is fine because a human reads its output and discards the junk; a noisy filter silently rewrites production telemetry and nobody sees what it destroyed. Every built-in here is allowed to be stricter than its scan counterpart, never looser.

`private-key` matches a whole `-----BEGIN … PRIVATE KEY-----` … `-----END … PRIVATE KEY-----` block with `(?s)` so it spans lines, and requires the END marker — a bare BEGIN marker in prose ("paste your `-----BEGIN PRIVATE KEY-----` here") is documentation, not a key.

### D6. Overlap resolution

`SLACK_BOT_TOKEN=xoxb-2650…` matches both `slack-token` and `secret-assignment` at the same offset — in the prototype the winner was decided by list order, which is not a property to leave implicit. The scan hit the same hazard from the other direction: GENAI-360 comment 937343 records "alternation order matters — put `sk-lf-` before generic `sk-`, or Langfuse keys match the loose pattern first." Specific-before-generic ordering is therefore a requirement, not a convention. The scanner collects all candidate spans, then resolves: **longest span wins; ties broken by detector declaration order (built-ins before user rules).** Overlapping spans are merged rather than nested, so a replacement is never emitted inside another replacement.

### D6b. Markers are protected regions — redaction is re-entrant

Redacting an already-redacted payload must be a no-op. It is not, naively: `url-userinfo` matches `://oauth2:[REDACTED:gitlab-pat:38e77b]@` and re-wraps the existing marker as `[REDACTED:url-userinfo:…]`, destroying the detector name and fingerprint underneath. The prototype failed exactly this test before the fix.

This is not hypothetical. A subagent's redacted tool output is nested into its parent's span; an agent that greps its own telemetry feeds markers back through; a retried tool call re-exports a payload already seen.

The scanner therefore pre-scans for marker literals and treats them as **protected regions**: any candidate span overlapping one is dropped. Verified by an idempotence test — `redact(redact(x)) == redact(x)` — which is cheap to run and would have caught the bug above on the first commit.

### D7. Replacement format: `[REDACTED:<detector>:<fp>]`

`fp` is the first 6 hex characters of `SHA-256(secret)`. Rationale: incident response needs to answer "is this the same token as the one in that other span?" and "how many distinct tokens leaked?" — both answerable from a fingerprint, neither requiring the value. 6 hex chars = 16.7M space, ample for the ~13-token scale here and far too short to brute-force a high-entropy secret (and useless against one that isn't, which is why `mode: strict` drops the fingerprint).

`mode` selects the shape: `fingerprint` (default, above), `strict` (`[REDACTED:<detector>]`, no fingerprint), `remove` (empty string).

*Alternative considered:* Langfuse's own `display_secret_key` convention, prefix + last 3 characters. Rejected — it leaks 3 characters of every secret into the very store we are trying to keep clean.

It does cost one capability, and it is worth being explicit about which: GENAI-360 comment 937343 joins a scan fingerprint against `api_keys.display_secret_key` on the last 3 characters to name *which* Langfuse key leaked. A SHA-256 fingerprint cannot make that join. But that join operates on the **historical, unredacted** rows — the ones that motivated this ticket. Once redaction is live the secret never reaches Langfuse at all, so there is nothing left to join; what a marker needs to answer is "same token or different?", which the hash answers. Deployments that would rather carry no fingerprint at all use `mode: strict`.

### D8. Custom rules are an array, never a map

```jsonc
"rules": [ { "name": "piano-internal", "pattern": "PI-[0-9]{12}", "group": 0 } ]
```

`CLAUDE.md` warns that viper case-folds map keys, and pure `json.Unmarshal` tests pass while the loader mangles in production. A map keyed by rule name would lowercase the name; worse, any design keying on the *pattern* would corrupt the regex itself (`[A-Z]` → `[a-z]`). An array of objects keeps every user string in a value position. A viper round-trip test is required, per `CLAUDE.md`, and must assert an uppercase-bearing pattern survives the loader intact.

### D9. Invalid regex fails startup

User patterns compile during config validation (`validateTelemetryConfig`), and a compile error returns a named error naming the rule. A security filter that silently degrades to no-op because of a typo is worse than one that never existed, because the operator believes they are covered.

### D10. Line-wrapped tokens: partial, honest mitigation

The corpus includes a token split by a terminal wrap:

```
token=glpat-3nK7Qw9xTbVm2LpR8sJdHy4Fc
ZaXeW6uNi0
```

A line-oblivious detector redacts the first fragment and leaves the 10-char tail. The prefixed built-ins therefore accept a single wrap point, expressed as an **alternation with the wrapped branch first** — RE2 has no lookahead (D5b), and leftmost-first alternation semantics mean the unwrapped branch would otherwise win on the first fragment and drop the continuation:

```
glpat-(?:[A-Za-z0-9_-]{8,}\r?\n[A-Za-z0-9_-]{8,}|[A-Za-z0-9_-]{16,})
```

Both branches require ≥16 body characters in total and ≥8 on each side of the wrap. An earlier draft put the newline inside a single character class (`[A-Za-z0-9_\-\r\n]{15,}`), which silently swallowed *runs* of blank lines — `glpat-AAAAAAAA\n\n\nnext paragraph` matched and consumed the paragraph break. The alternation admits exactly one wrap and is covered by a regression case.

Applied **only** to high-specificity prefixed detectors — never to `generic-sk` or the positional three, where crossing a newline would swallow the following line.

Known residue: `glpat-<35 chars>\nAKIAJ4QX…` is absorbed into one `gitlab-pat` match spanning both credentials. Both are still redacted — the marker just mis-attributes the second one's class. Acceptable; the alternative is leaving a real token in the clear.

### D12. Candidate-position scanning, not whole-payload regex passes

The obvious implementation — run each detector's pattern over the whole payload — measured 106ms on 400KB. Per-detector measurement located the cost precisely:

| detector | full pass over 400KB |
|---|---|
| `secret-assignment` | **75ms** |
| `auth-header` | 14ms |
| `aws-access-key-id`, `langfuse-key`, `jwt`, `github-token`, `generic-sk` | ~7ms each |
| everything else | <1ms |

Two findings, both measured rather than reasoned:

1. **A leading `(?i)[A-Z0-9_]*` is poison.** Under `(?i)` it matches nearly anything, leaving RE2 with no literal prefix to anchor on. Starting the pattern at the keyword alternation instead cut it from 75ms to 51ms — and writing the alternation out explicitly in upper/lower/Title case made it *worse* (83ms), so that dead end is recorded here too.
2. **The width of a case-insensitive alternation dominates.** One keyword under `(?i)` costs 7.7ms where nine cost 51ms.

The fix is not a better regex. Detectors declare the literals a match always **starts** with; those are located with `strings.Index` (SIMD, GB/s) and the pattern is applied only at those offsets, anchored. Same work, 1.8ms instead of 51ms.

Three consequences worth knowing:

- A leading `\b` must be **stripped** from the anchored variant and checked by hand against the original string. `\b` is evaluated relative to the slice it is applied to, so at a candidate offset it would always look satisfied, and `XAKIAIOSFODNN7EXAMPLE` would match. There is a test for exactly this.
- Detectors whose literal sits *inside* the match rather than at its head (`private-key` is found by "PRIVATE KEY" but starts at `-----BEGIN`; `email` by "@") keep a presence-gate only and scan full-width. They are cheap.
- **Candidate scanning finds a superset of `FindAll`'s matches**, because `FindAll` returns leftmost *non-overlapping* results. This is a safety improvement, not a regression: the differential test's first run caught the full scan leaving `abcdefghijk` in the clear, from a `my_api_key:` assignment nested inside a longer match that `FindAll` had already consumed. The invariant asserted is therefore *coverage containment* — candidate scanning never redacts less — not output equality.

### D11. On by default

`enabled` defaults `true`. Telemetry content capture is itself opt-in and off by default, so a default-on filter is free for anyone not already capturing, and correct-by-default for everyone who is. A security control that must be discovered and switched on has the wrong default.

## Risks / Trade-offs

- **A shape we don't know, in a position we don't recognise, still leaks.** → The positional detectors (D5) cover the common carriers; `rules` covers the rest without a fork change; and the 90-day ClickHouse scan in GENAI-360 §5 is re-runnable as a standing audit to find what the filter missed. This change reduces the class, it does not close it — which is why the ticket's option (c) (org scoping) and a server-side backstop (d) remain worth doing.
- **False positives mangle telemetry and erode trust.** → The section-3 corpus is committed as a regression test and CI fails on any new match. The generic detector is the only gated one, and it is the only one that could plausibly over-match.
- **Redaction cost on the hot path.** → Measured, and the first honest number was bad: a naive implementation (12 patterns, each scanned over the whole payload) cost **106ms on a 400KB payload**, ~70% of it in one case-insensitive nine-way alternation that gave RE2 no literal to anchor on. Redaction runs on every span of every LLM call, so that is not acceptable.

  Fixed by candidate-position scanning (D12): **5.5ms adversarial / 4.7ms realistic at 400KB, 0.19ms at 10KB**. The remaining cost is the literal gate itself, not the patterns. The original `<1ms at 400KB` budget written into this design was wrong — it is recorded here as measured rather than quietly restated, and the fallback of "narrow the default scope to tool payloads" was explicitly **rejected**: that would leave generation payloads unredacted, which is the leak this change exists to close. 5ms once per multi-second model call is the right trade.
- **Truncation boundary can still bisect a token.** → D2 makes redaction run first, so this only applies to a secret that appears *beyond* the cap and was therefore already being dropped, not shipped.
- **Fingerprint is a (tiny) side channel.** → 6 hex of SHA-256 over a high-entropy secret is not invertible; over a *low*-entropy one (a weak password caught by `secret-assignment`) it is brute-forceable. `mode: strict` exists for deployments that would rather have no fingerprint at all.
- **An operator writes a rule with lookahead and it fails to compile.** → RE2 has no lookaround; validation rejects it at load with an error naming the rule, and the docs give the capture-group idiom as the replacement. Failing loudly is the design (D9), but this is the most likely first-contact papercut, so the message must name lookaround specifically rather than echoing Go's raw parser error.
- **A marker gets re-wrapped and loses its provenance.** → Protected regions plus an idempotence test (D6b). The failure is silent and destroys exactly the diagnostic value the fingerprint exists for, so the test is not optional.
- **Default-on changes what existing dashboards show.** → Documented in `docs/telemetry.md` and the proposal; `enabled: false` restores prior behavior. No stored data is altered — this affects newly exported spans only.

## Migration Plan

1. Ship with `enabled: true`. No config change required for the fix to take effect; the fork's consumers pick it up on the next opencode bump.
2. Existing Langfuse data is **not** rewritten. The credentials already in ClickHouse stay there until rotated and the retention window rolls — rotation (GENAI-360 "Immediate actions") is the remedy for what already leaked, and this change is what stops the next one.
3. Rollback is `telemetry.redaction.enabled: false` in `.opencode.json`, or reverting the opencode bump. No schema migration, no stored state.
4. Verification after deploy: re-run the ClickHouse scan from GENAI-360 [comment 937343](https://tinypass.atlassian.net/browse/GENAI-360?focusedCommentId=937343) — Q1 for counts by class, Q2 for per-match fingerprints — over a window starting at the deploy timestamp. Expected result is zero new hits on Q1's eight classes and a population of `[REDACTED:…]` markers. That scan is also the honest way to discover shapes the built-ins miss, and Q1's class list is what the built-in set is measured against.

## Open Questions

- Whether `mcp-*` tool outputs need any detector beyond the built-in set is answerable from the deploy-window scan in step 4 rather than now; adding one is a `rules` entry, which changes neither the specs nor the task breakdown.
