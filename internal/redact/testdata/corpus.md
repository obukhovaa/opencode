# Redaction backtest corpus

Derived from the credential shapes and false positives found by the GENAI-360
90-day scan of the C2 Agent Langfuse project. See
https://tinypass.atlassian.net/browse/GENAI-360 (comment 937343 carries the scan
queries).

**Every value in sections 1 and 2 is SYNTHETIC and DEFUSED.** Shapes, lengths,
character classes and segment structure match the real findings so detector
coverage is unaffected, but each value embeds `EXAMPLE` (or a constant filler
like `deadbeef-0000-…` where the character class is hex-only) so it is
recognisable as an example on sight and does not trip a secret scanner. None of
them grants access to anything. See design.md D0.

**Section 3 is committed VERBATIM and must stay that way.** Those strings are
real strings from the live corpus, and they are real precisely because they are
*not* credentials — they are `task-`/`risk-`/`disk-` fragments followed by
hyphenated English. Their exact characters are what the false-positive
regression tests. Do not defuse them.

A note for future editors: `glpat-EXAMPLEn8KpLdR4T` is 22 characters (a 6-char
prefix and a 16-char body), even though the upstream memo labels that shape
"26ch legacy". The fixture is right and the label is off — a `glpat-…{20,}`
pattern misses it, which is why the built-in uses `{16,}`. Do not "correct" this
to match the comment.

## 1. MUST REDACT — bare

- gitlab-pat        `glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0`
- gitlab-pat        `glpat-EXAMPLEn8KpLdR4T`
- slack-token       `xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M`
- slack-token       `xoxb-EXAMPLE-EXAMPLE-EXAMPLEu4Ts2Rq0Pn`

  The Slack samples are the one place where defusing had to change a value's
  *shape*, not just its characters. The production form carries two long digit
  runs (`xoxb-2650701234567-8901234567890-…`), and GitHub push protection
  rejects that shape even with `EXAMPLE` in the tail — it blocked this very
  commit. The digit runs are replaced with `EXAMPLE`, which leaves our own
  detector fully exercised (it matches `xox[baprse]-` followed by alphanumeric
  segments of two or more, with no digit or length requirement) while no longer
  resembling a token to an external scanner.
- aws-access-key-id `AKIAIOSFODNN7EXAMPLE`
- aws-access-key-id `ASIAIOSFODNN7EXAMPLE`
- langfuse-key      `sk-lf-deadbeef-0000-4000-8000-000000000000`
- langfuse-key      `pk-lf-deadbeef-0000-4000-8000-000000000000`
- generic-sk        `sk-_EXAMPLEXr4Kd9Wb2Nv7Lp`
- anthropic-key     `sk-ant-api03-EXAMPLEb2Nv7Lp3Qm8Zs5Tc1Yf6Hj0Ag-AAAAAA`
- github-token      `ghp_EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0A`
- jwt               `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJFWEFNUExFIn0.EXAMPLEsignatureDoNotUse`

## 2. MUST REDACT — embedded in realistic tool output

Context is where filters actually fail. Each mirrors a real span type.

- webfetch input — a PAT inside a URL; 300 real occurrences took this shape
  `https://oauth2:glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0@gitlab.com/piano/composer/agents/developer.git`
- bash stdout — env dump
  `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE`
- bash stdout — env dump, shape-agnostic path
  `SLACK_BOT_TOKEN=xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M`
- gitlab_get_file_contents output — secret read out of a repo
  `{"file_path":".env.prod","content":"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\nAWS_SECRET_ACCESS_KEY=wJalrEXAMPLEKEY/K7MDENG/bPxRfiCY"}`
- bash stdout — curl with a header
  `curl -H "Authorization: Bearer sk-_EXAMPLEXr4Kd9Wb2Nv7Lp" https://litellm.de-prod.cxense.com/key/info`
- JSON tool payload, key nested and escaped
  `{"command":"kubectl get secret -o json","stdout":"{\"data\":{\"token\":\"glpat-EXAMPLEn8KpLdR4T\"}}"}`
- PEM block spanning lines
  `-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAEXAMPLEdoNotUseThisKeyItIsFake\n-----END RSA PRIVATE KEY-----`

### 2w. MUST REDACT — wrapped across a line break

Terminal output wraps; a line-oblivious detector leaves the tail in the clear.

```
token=glpat-EXAMPLExTbVm2LpR8sJdHy4Fc
ZaXeW6uNi0
```

## 3. MUST NOT REDACT — real false positives (verbatim, do not edit)

Actual strings from the live corpus that a naive `sk-[A-Za-z0-9_-]{20,}`
matches. Ordinary text, no credential value.

- `sk-clusters-fork-ebs-csi-metrics`
- `sk-id-token-refresh-fails`
- `sk-mitigation-plan-scan`
- `sk-dynamic-batch-size-gap`
- `sk-for-sync-with-upstream-id`
- `sk-to-fix-upstream-merge-gm`
- `sk-gitops-pipeline-stage-765`
- `sk-26aug-2100-rollout-kf2`

## 4. MUST NOT REDACT — adversarial negatives

Constructed to pin behaviours that a careless pattern gets wrong.

- `the disk-id-token-refresh-fails task`
- `see glpat-doc in the runbook`
- `eyJust-a-word.and-another-thing`
- `base64 payload eyJshort.abc`
- `see -----BEGIN PRIVATE KEY----- in docs without an end marker`

### 4w. MUST NOT REDACT — short prefix followed by blank lines

A newline-tolerant detector must admit exactly one wrap, not runs of them.

```
glpat-AAAAAAAA


next paragraph
```
