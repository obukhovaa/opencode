package redact

import (
	"regexp"
	"strings"
)

// Built-in detectors, ordered specific-before-generic. The order is load-
// bearing twice over: a length tie is broken by it, and the GENAI-360 scan hit
// the same hazard from the other side — "put sk-lf- before generic sk-, or
// Langfuse keys match the loose pattern first".
//
// Prefixed shape detectors tolerate a single line wrap: terminal output wraps
// mid-token, and a line-oblivious pattern redacts the head and leaves the tail
// in the clear. The wrapped branch must come FIRST in the alternation — Go's
// regexp is leftmost-first, so an unwrapped branch placed first would match the
// head and drop the continuation. Both branches require the same total body
// length. Never give this treatment to a generic or positional detector: there,
// crossing a newline swallows the following line.
func wrapped(prefix string, minBody int) *regexp.Regexp {
	half := minBody / 2
	return regexp.MustCompile(
		prefix + `(?:[A-Za-z0-9_-]{` + itoa(half) + `,}\r?\n[A-Za-z0-9_-]{` + itoa(half) + `,}` +
			`|[A-Za-z0-9_-]{` + itoa(minBody) + `,})`)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func builtins() []detector {
	return []detector{
		// GitLab PAT. Body minimum is 16, not GitLab's documented 20: the
		// shorter legacy shape observed in production has a 16-char body and a
		// {20,} pattern misses it entirely.
		{name: "gitlab-pat", re: wrapped(`glpat-`, 16), starts: []string{"glpat-"}},

		{name: "slack-token", re: regexp.MustCompile(`xox[baprse]-[A-Za-z0-9]{2,}(?:-[A-Za-z0-9]{2,}){1,3}`), starts: []string{"xox"}},

		// Every documented AWS unique-id prefix, not just AKIA/ASIA.
		{name: "aws-access-key-id", re: regexp.MustCompile(
			`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ABIA|ACCA)[A-Z0-9]{16}\b`),
			starts: []string{"AKIA", "ASIA", "AGPA", "AIDA", "AROA", "AIPA", "ANPA", "ANVA", "ABIA", "ACCA"}},

		// Langfuse keys are a UUID, so they are all-lowercase hex and would
		// fail the generic gate below. They must be matched by their own
		// pattern, which is why specific-before-generic is a requirement.
		{name: "langfuse-key", re: regexp.MustCompile(
			`\b[sp]k-lf-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`),
			starts: []string{"sk-lf-", "pk-lf-"}},

		{name: "anthropic-key", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`), starts: []string{"sk-ant-"}},
		{name: "github-token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
			starts: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}},

		// A real JWT's payload is base64url-encoded JSON, so the second
		// segment also starts "eyJ". Requiring it costs no real token and
		// drops false positives like "eyJust-a-word.and-another-thing" that
		// the looser scan pattern matches. A scan a human reviews may be
		// noisy; a filter that rewrites silently may not.
		{name: "jwt", re: regexp.MustCompile(
			`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_-]+)?`), starts: []string{"eyJ"}},

		// Requires the END marker: a lone BEGIN marker in prose is
		// documentation ("paste your -----BEGIN PRIVATE KEY----- here").
		{name: "private-key", re: regexp.MustCompile(
			`(?s)-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----.*?-----END (?:[A-Z]+ )*PRIVATE KEY-----`),
			prefilter: []string{"PRIVATE KEY"}},

		// Positional detectors. These carry most of the weight: they catch a
		// credential whose shape we have never seen, purely from where it
		// sits. RE2 has no lookaround, so each matches its surrounding context
		// and replaces a capture group instead.
		{name: "url-userinfo", re: regexp.MustCompile(`://[^/\s:@]+:([^/\s@]+)@`), group: 1, starts: []string{"://"}},
		{name: "auth-header", re: regexp.MustCompile(
			`(?i)(?:authorization|proxy-authorization)"?\s*[:=]\s*"?\s*(?:bearer|basic|token)\s+([^\s"',}]+)`), group: 1,
			starts: []string{"authorization", "proxy-authorization"}, fold: true},
		// The leading name characters are deliberately NOT matched. An earlier
		// draft opened with `\b[A-Z0-9_]*`, which under (?i) matches almost
		// anything and leaves RE2 with no literal to anchor on: it cost 75ms on
		// a 400KB payload, 70% of the whole scan. Starting at the keyword lets
		// the engine prefilter on the alternation's literals and is three
		// orders of magnitude faster, at the cost of also matching a name that
		// merely ends with a keyword ("notoken=…") — a harmless over-match.
		{name: "secret-assignment", re: regexp.MustCompile(
			`(?i)(?:TOKEN|SECRET|PASSWORD|PASSWD|APIKEY|API_KEY|ACCESS_KEY|PRIVATE_KEY|CREDENTIAL)[A-Z0-9_]*"?\s*[:=]\s*\\*"?([^\s"',}\\]{6,})`), group: 1,
			starts: []string{"token", "secret", "password", "passwd", "apikey", "api_key", "access_key", "private_key", "credential"}, fold: true},

		// Generic sk-/pk-. The only gated detector, and the only one that
		// could plausibly over-match: the live corpus is full of "task-",
		// "risk-" and "disk-" fragments followed by hyphenated English.
		{name: "generic-sk", re: regexp.MustCompile(`\b[sp]k-[A-Za-z0-9_-]+`), accept: acceptGenericKey,
			starts: []string{"sk-", "pk-"}},
	}
}

// acceptGenericKey decides whether an ambiguous sk-/pk- candidate is a real
// key. Two signals, measured against the production corpus:
//
//	glpat-7Fq2MxVn8KpLdR4T        body entropy 4.00  real key
//	sk-for-sync-with-upstream-id  body entropy 4.00  false positive
//
// Shannon entropy is therefore useless here — any floor between them both
// misses real keys and admits false positives. What separates them cleanly is
// case and word shape: every real key in the corpus carries uppercase and no
// dictionary words, every false positive is all-lowercase hyphenated English.
// The length branch is the escape hatch for an all-lowercase long key; the
// longest false-positive body observed is 29 characters.
func acceptGenericKey(match string) bool {
	body := strings.TrimPrefix(strings.TrimPrefix(match, "sk-"), "pk-")
	if len(body) >= 40 {
		return true
	}
	if !strings.ContainsFunc(body, func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		return false
	}
	for _, seg := range strings.FieldsFunc(body, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(seg) >= 2 && isLowerAlpha(seg) {
			return false
		}
	}
	return true
}

func isLowerAlpha(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return len(s) > 0
}

// piiDetectors are off by default. Every credential in the incident that
// motivated this package was a secret, while agent telemetry legitimately
// carries emails (Jira assignees, git authors) and IPs (pod addresses), so
// redacting them by default would mangle ordinary output.
func piiDetectors() []detector {
	return []detector{
		{name: "email", re: regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), prefilter: []string{"@"}},
		{name: "ipv4", re: regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)},
		{name: "ipv6", re: regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\b`), prefilter: []string{":"}},
		{name: "phone", re: regexp.MustCompile(`(?:\+\d{1,3}[ .\-]?)?\(?\d{3}\)?[ .\-]\d{3}[ .\-]\d{4}\b`)},
	}
}
