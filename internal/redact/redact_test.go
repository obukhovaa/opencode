package redact

import (
	"math"
	"strings"
	"testing"
)

func TestString_OverlapResolvesToOneMarker(t *testing.T) {
	r := New(Options{})
	// SLACK_BOT_TOKEN=xoxb-... matches both slack-token (shape) and
	// secret-assignment (position) at the same offset.
	got := r.String("SLACK_BOT_TOKEN=xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M")
	if n := strings.Count(got, markerPrefix); n != 1 {
		t.Fatalf("want exactly 1 marker, got %d: %s", n, got)
	}
	// Tie on length breaks by declaration order, and slack-token is declared
	// before secret-assignment, so attribution names the specific shape.
	if !strings.Contains(got, "[REDACTED:slack-token:") {
		t.Errorf("want slack-token attribution, got %s", got)
	}
	if !strings.HasPrefix(got, "SLACK_BOT_TOKEN=") {
		t.Errorf("surrounding text not preserved: %s", got)
	}
}

func TestString_PartiallyOverlappingMatchesLeaveNoFragment(t *testing.T) {
	r := New(Options{})
	in := "https://oauth2:glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0@gitlab.com/x.git"
	got := r.String(in)
	if strings.Contains(got, "glpat-") || strings.Contains(got, "EXAMPLExTbVm") {
		t.Errorf("fragment survived: %s", got)
	}
	if !strings.HasPrefix(got, "https://oauth2:") || !strings.HasSuffix(got, "@gitlab.com/x.git") {
		t.Errorf("URL structure not preserved: %s", got)
	}
}

func TestString_Deterministic(t *testing.T) {
	r := New(Options{})
	in := `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE and glpat-EXAMPLEn8KpLdR4T`
	if a, b := r.String(in), r.String(in); a != b {
		t.Errorf("not deterministic:\n  %s\n  %s", a, b)
	}
}

// Re-entrancy. Without protected regions, url-userinfo re-wraps the marker
// already sitting in the userinfo position and destroys the detector name and
// fingerprint underneath it.
func TestString_Idempotent(t *testing.T) {
	r := New(Options{})
	for _, in := range []string{
		"https://oauth2:glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0@gitlab.com/x.git",
		"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		`curl -H "Authorization: Bearer sk-_EXAMPLEXr4Kd9Wb2Nv7Lp" https://x/y`,
	} {
		once := r.String(in)
		if twice := r.String(once); twice != once {
			t.Errorf("not idempotent\n  in:    %q\n  once:  %q\n  twice: %q", in, once, twice)
		}
	}
}

func TestString_ExistingMarkerKeepsItsProvenance(t *testing.T) {
	r := New(Options{})
	// A subagent's already-redacted output nested into a parent payload.
	in := `{"subagent_output":"https://oauth2:[REDACTED:gitlab-pat:38e77b]@gitlab.com/x.git"}`
	got := r.String(in)
	if got != in {
		t.Errorf("existing marker was disturbed\n  in:  %s\n  out: %s", in, got)
	}
	if !strings.Contains(got, "[REDACTED:gitlab-pat:38e77b]") {
		t.Errorf("original detector name and fingerprint lost: %s", got)
	}
}

func TestGenericKeyGate(t *testing.T) {
	// The live-corpus false positives: task-/risk-/disk- fragments followed by
	// hyphenated English. All-lowercase, no dictionary-free body.
	falsePositives := []string{
		"sk-clusters-fork-ebs-csi-metrics", "sk-id-token-refresh-fails",
		"sk-mitigation-plan-scan", "sk-dynamic-batch-size-gap",
		"sk-for-sync-with-upstream-id", "sk-to-fix-upstream-merge-gm",
		"sk-gitops-pipeline-stage-765", "sk-26aug-2100-rollout-kf2",
	}
	for _, s := range falsePositives {
		if acceptGenericKey(s) {
			t.Errorf("false positive accepted: %q", s)
		}
	}
	realKeys := []string{
		"sk-_EXAMPLEXr4Kd9Wb2Nv7Lp",     // uppercase, no word segments
		"sk-" + strings.Repeat("a", 40), // long branch, all lowercase
	}
	for _, s := range realKeys {
		if !acceptGenericKey(s) {
			t.Errorf("real key rejected: %q", s)
		}
	}
}

// Entropy is not the discriminator, and this pins why: these two score
// identically (4.00 bits/char over the body) yet must be classified opposite.
// Any entropy floor placed between them is arbitrary.
func TestGenericKeyGate_EntropyWouldNotSeparate(t *testing.T) {
	realKey := "glpat-7Fq2MxVn8KpLdR4T"        // body entropy 4.00
	falsePos := "sk-for-sync-with-upstream-id" // body entropy 4.00
	if e1, e2 := shannon(realKey[6:]), shannon(falsePos[3:]); !approx(e1, e2, 0.01) {
		t.Fatalf("fixture drift: entropies no longer equal (%.2f vs %.2f); the "+
			"no-entropy-gate rationale in design.md D3 rests on this", e1, e2)
	}
	r := New(Options{})
	if !strings.Contains(r.String(realKey), markerPrefix) {
		t.Error("real key not redacted")
	}
	if got := r.String(falsePos); got != falsePos {
		t.Errorf("false positive redacted: %s", got)
	}
}

func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]int{}
	for _, r := range s {
		freq[r]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range freq {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

func TestNewlineTolerance(t *testing.T) {
	r := New(Options{})

	wrapped := "token=glpat-EXAMPLExTbVm2LpR8sJdHy4Fc\nZaXeW6uNi0"
	got := r.String(wrapped)
	if strings.Contains(got, "ZaXeW6uNi0") {
		t.Errorf("wrapped tail left in the clear: %q", got)
	}

	// Exactly one wrap, never runs of blank lines. An earlier draft put \n
	// inside the body character class and swallowed paragraph breaks.
	blanks := "glpat-AAAAAAAA\n\n\nnext paragraph"
	if got := r.String(blanks); got != blanks {
		t.Errorf("blank-line run consumed: %q", got)
	}

	// Generic and positional detectors must never cross a newline.
	multi := "sk-_EXAMPLEXr4Kd9Wb2\nNv7LpMore"
	if got := r.String(multi); strings.Contains(got, "Nv7LpMore") == false {
		t.Errorf("generic detector crossed a newline: %q", got)
	}
}

// Candidate scanning locates fold-gated detectors by byte offset in a folded
// copy of the payload. strings.ToLower would have made those offsets lie:
// 'İ' (2 bytes) folds to 'i' (1) and 'K' (3) to 'k' (1), so one such rune
// anywhere ahead of a credential shifted every later offset and the anchored
// pattern found nothing — auth-header and secret-assignment silently stopped
// firing for the rest of the payload. asciiLower preserves length.
func TestFoldedOffsetsSurviveNonASCII(t *testing.T) {
	r := New(Options{})
	const secret = "AbCdEfGhIjKlMnOpQrStUvWx"

	// Every prefix below contains a rune whose Unicode lowercase is SHORTER
	// in UTF-8 than the original, which is what skewed the offsets.
	prefixes := map[string]string{
		"dotted-capital-I": "\u0130stanbul deploy log\n",
		"kelvin-sign":      "core temp 100\u212A\n",
		"capital-sharp-s":  "STRA\u1e9eE gateway\n",
		"plain-ascii":      "Istanbul deploy log\n",
	}
	carriers := map[string]string{
		"auth-header":       "Authorization: Bearer " + secret,
		"secret-assignment": "API_KEY=" + secret,
	}

	for pname, prefix := range prefixes {
		for cname, carrier := range carriers {
			t.Run(pname+"/"+cname, func(t *testing.T) {
				got := r.String(prefix + carrier)
				if strings.Contains(got, secret) {
					t.Errorf("credential survived after a non-ASCII prefix: %q", got)
				}
				if !strings.Contains(got, "[REDACTED:") {
					t.Errorf("no marker emitted: %q", got)
				}
				if !strings.HasPrefix(got, prefix) {
					t.Errorf("prefix text was altered: %q", got)
				}
			})
		}
	}
}

func TestAsciiLowerPreservesByteLength(t *testing.T) {
	for _, s := range []string{
		"Authorization", "\u0130stanbul", "100\u212A", "STRA\u1e9eE", "", "\u00e9\u00c9",
	} {
		if got := asciiLower(s); len(got) != len(s) {
			t.Errorf("asciiLower(%q) changed length: %d -> %d", s, len(s), len(got))
		}
	}
	if got := asciiLower("AUTHORIZATION"); got != "authorization" {
		t.Errorf("asciiLower did not fold ASCII: %q", got)
	}
}

func TestModes(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeFingerprint, "[REDACTED:aws-access-key-id:" + Fingerprint(secret) + "]"},
		{ModeStrict, "[REDACTED:aws-access-key-id]"},
		{ModeRemove, ""},
	}
	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			if got := New(Options{Mode: tc.mode}).String(secret); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestFingerprint_StableAndDistinct(t *testing.T) {
	a := "AKIAIOSFODNN7EXAMPLE"
	b := "ASIAIOSFODNN7EXAMPLE"
	if Fingerprint(a) != Fingerprint(a) {
		t.Error("fingerprint not stable")
	}
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("distinct secrets share a fingerprint")
	}
	// No part of the value survives into the marker.
	fp := Fingerprint(a)
	for i := 0; i+4 <= len(a); i++ {
		if strings.Contains(fp, a[i:i+4]) {
			t.Errorf("fingerprint %q leaks plaintext run %q", fp, a[i:i+4])
		}
	}
}

func TestFingerprint_SharedPrefixDoesNotLeak(t *testing.T) {
	a := "glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0"
	b := "glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi1"
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("near-identical secrets share a fingerprint")
	}
	if strings.HasPrefix(Fingerprint(a), "glpat") || strings.HasPrefix(Fingerprint(b), "glpat") {
		t.Error("fingerprint carries the shared prefix")
	}
}

func TestAllowlist(t *testing.T) {
	const s = "AKIAIOSFODNN7EXAMPLE"
	r := New(Options{Allowlist: []string{s}})
	if got := r.String("key " + s); got != "key "+s {
		t.Errorf("allowlisted value was redacted: %s", got)
	}
}

func TestDisableBuiltins(t *testing.T) {
	r := New(Options{DisableBuiltins: []string{"aws-access-key-id"}})
	if got := r.String("AKIAIOSFODNN7EXAMPLE"); got != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("disabled detector still fired: %s", got)
	}
	if got := r.String("glpat-EXAMPLEn8KpLdR4T"); !strings.Contains(got, markerPrefix) {
		t.Errorf("unrelated builtin stopped working: %s", got)
	}
}

func TestPII_OffByDefaultOnWhenEnabled(t *testing.T) {
	const in = "assignee alice@piano.io on pod 10.42.0.17"
	if got := New(Options{}).String(in); got != in {
		t.Errorf("PII redacted by default: %s", got)
	}
	got := New(Options{EnablePII: true}).String(in)
	if strings.Contains(got, "alice@piano.io") || strings.Contains(got, "10.42.0.17") {
		t.Errorf("PII not redacted when enabled: %s", got)
	}
}

func TestCustomRule(t *testing.T) {
	r := New(Options{Rules: []Rule{{Name: "piano-internal", Pattern: `PI-[0-9]{12}`}}})
	got := r.String("ref PI-123456789012 here")
	if !strings.Contains(got, "[REDACTED:piano-internal:") {
		t.Errorf("custom rule did not fire: %s", got)
	}
}

func TestCustomRule_CaptureGroupOnly(t *testing.T) {
	r := New(Options{Rules: []Rule{{Name: "kv", Pattern: `mykey=(\S+)`, Group: 1}}})
	got := r.String("mykey=supersecretvalue")
	if !strings.HasPrefix(got, "mykey=") {
		t.Errorf("context outside the group was replaced: %s", got)
	}
	if strings.Contains(got, "supersecretvalue") {
		t.Errorf("group not replaced: %s", got)
	}
}

func TestDisabledRedactorIsPassThrough(t *testing.T) {
	const in = "AKIAIOSFODNN7EXAMPLE glpat-EXAMPLEn8KpLdR4T"
	if got := Disabled().String(in); got != in {
		t.Errorf("disabled redactor altered input: %s", got)
	}
}

func TestValidateRule(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantErr string
	}{
		{"valid", Rule{Name: "a", Pattern: `x[0-9]+`}, ""},
		{"valid with group", Rule{Name: "a", Pattern: `k=(\S+)`, Group: 1}, ""},
		{"empty name", Rule{Pattern: `x`}, "rule name is required"},
		{"empty pattern", Rule{Name: "a"}, "pattern is required"},
		{"bad pattern", Rule{Name: "a", Pattern: `[unclosed`}, "invalid pattern"},
		{"lookahead", Rule{Name: "a", Pattern: `foo(?=bar)`}, "lookahead/lookbehind"},
		{"lookbehind", Rule{Name: "a", Pattern: `(?<=x)y`}, "lookahead/lookbehind"},
		{"group out of range", Rule{Name: "a", Pattern: `x(\d)`, Group: 2}, "does not exist in pattern"},
		{"negative group", Rule{Name: "a", Pattern: `x`, Group: -1}, "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRule(tc.rule)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidMode(t *testing.T) {
	for _, m := range Modes {
		if !ValidMode(m) {
			t.Errorf("%q should be valid", m)
		}
	}
	if ValidMode("nope") {
		t.Error("unknown mode accepted")
	}
}

func approx(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// The redactor runs on every content attribute of every span, so its cost sits
// on the hot path. Design budget: under 1ms for a 400KB payload (the
// generation cap). RE2 is linear-time with no backtracking blowup, so the
// figure should be stable.
func BenchmarkString_400KB(b *testing.B) {
	chunk := `2026-09-23T10:00:00Z INFO  fetching https://gitlab.com/piano/composer/agents/developer.git
2026-09-23T10:00:01Z DEBUG cache hit for task-clusters-fork-ebs-csi-metrics, disk-id-token-refresh-fails
2026-09-23T10:00:02Z INFO  AWS_REGION=eu-central-1 CLUSTER=prod-euc1 replicas=3
`
	var sb strings.Builder
	for sb.Len() < 400*1024 {
		sb.WriteString(chunk)
	}
	// One credential near the end, so the scan cannot short-circuit early.
	payload := sb.String() + "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\n"

	r := New(Options{})
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.String(payload)
	}
}

// A realistic generation payload: a system prompt plus message history. Few
// candidate literals, which is the case that actually runs on every LLM call.
func BenchmarkString_400KB_Realistic(b *testing.B) {
	chunk := `You are a coding agent. Follow the repository conventions.
The user asked to refactor the export pipeline in internal/report.
I read internal/report/pipeline.go and found the writer is buffered.
Consider whether the flush happens before the context is cancelled.
`
	var sb strings.Builder
	for sb.Len() < 400*1024 {
		sb.WriteString(chunk)
	}
	payload := sb.String()

	r := New(Options{})
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.String(payload)
	}
}

// The common case is a payload with nothing to redact; it must not be slower
// than the hit case.
func BenchmarkString_10KB_NoMatch(b *testing.B) {
	payload := strings.Repeat("ordinary tool output with no credentials in it at all\n", 200)
	r := New(Options{})
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.String(payload)
	}
}
