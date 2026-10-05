package redact

import (
	"math/rand"
	"strings"
	"testing"
)

// Candidate scanning must never redact LESS than a plain full-width scan.
//
// Equality is the wrong invariant: Go's FindAll returns leftmost
// NON-OVERLAPPING matches, so a full scan that matches a long
// secret-assignment span skips a second assignment nested inside it and leaves
// that value in the clear. Candidate scanning examines every literal offset
// independently and catches both. The first run of this test found exactly
// that — full scan leaked "abcdefghijk" from an embedded "my_api_key:" that
// candidate scanning redacted.
//
// So the contract asserted here is coverage containment: every byte the full
// scan would redact is also redacted by candidate scanning. More is safe;
// less is a leak.
func TestCandidateScanNeverRedactsLessThanFullScan(t *testing.T) {
	pieces := []string{
		"glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0", "glpat-EXAMPLEn8KpLdR4T",
		"AKIAIOSFODNN7EXAMPLE", "ASIAIOSFODNN7EXAMPLE", "notAKIAIOSFODNN7EXAMPLE",
		"sk-lf-deadbeef-0000-4000-8000-000000000000",
		"pk-lf-deadbeef-0000-4000-8000-000000000000",
		"sk-ant-api03-EXAMPLEb2Nv7Lp3Qm8Zs5Tc1Yf6Hj0Ag-AAAAAA",
		"ghp_EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0A", "xghp_EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0A",
		"xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M",
		"sk-_EXAMPLEXr4Kd9Wb2Nv7Lp", "sk-clusters-fork-ebs-csi-metrics",
		"disk-id-token-refresh-fails", "task-mitigation-plan-scan",
		"https://oauth2:glpat-EXAMPLEn8KpLdR4T@gitlab.com/x.git",
		"https://example.com/plain", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		"SLACK_BOT_TOKEN=xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M",
		`Authorization: Bearer sk-_EXAMPLEXr4Kd9Wb2Nv7Lp`,
		`authorization="Basic QUJDOmRlZg=="`, "token=short",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJFWEFNUExFIn0.EXAMPLEsig",
		"eyJust-a-word.and-another-thing", "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----",
		"ordinary log line with no secrets", "PASSWORD: hunter2secret", "x", "\n", " ", `"`, "}",
		"my_api_key: abcdefghijk", "PRIVATE_KEY=abcdefghij",
	}

	full := fullScanRedactor()
	fast := New(Options{EnablePII: true})

	rng := rand.New(rand.NewSource(20260923))
	for i := 0; i < 3000; i++ {
		var sb strings.Builder
		for j := 0; j < rng.Intn(8)+1; j++ {
			sb.WriteString(pieces[rng.Intn(len(pieces))])
			switch rng.Intn(4) {
			case 0:
				sb.WriteString(" ")
			case 1:
				sb.WriteString("\n")
			case 2:
				sb.WriteString(`","`)
			}
		}
		in := sb.String()
		wantCovered := coverage(full.coveredSpans(in), len(in))
		gotCovered := coverage(fast.coveredSpans(in), len(in))
		for idx := range wantCovered {
			if wantCovered[idx] && !gotCovered[idx] {
				t.Fatalf("candidate scan redacts less than full scan at byte %d\n  in:   %q\n  full: %q\n  fast: %q",
					idx, in, full.String(in), fast.String(in))
			}
		}
	}
}

// Nothing that both scanners agree is ordinary text may be redacted: the
// false-positive contract still holds under candidate scanning.
func TestCandidateScanKeepsFalsePositivesClean(t *testing.T) {
	fast := New(Options{})
	for _, in := range []string{
		"sk-clusters-fork-ebs-csi-metrics", "disk-id-token-refresh-fails",
		"sk-26aug-2100-rollout-kf2", "eyJust-a-word.and-another-thing",
	} {
		if got := fast.String(in); got != in {
			t.Errorf("false positive under candidate scan: %q -> %q", in, got)
		}
	}
}

func coverage(spans []span, n int) []bool {
	out := make([]bool, n)
	for _, sp := range spans {
		for i := sp.start; i < sp.end && i < n; i++ {
			out[i] = true
		}
	}
	return out
}

// fullScanRedactor builds a redactor with candidate scanning switched off, so
// every detector runs its pattern over the whole payload.
func fullScanRedactor() *Redactor {
	r := New(Options{EnablePII: true})
	dets := make([]detector, len(r.dets))
	copy(dets, r.dets)
	for i := range dets {
		dets[i].starts = nil
		dets[i].anchored = nil
		dets[i].prefilter = nil
	}
	return &Redactor{dets: dets, allow: r.allow, mode: r.mode, needsFold: false}
}

// The anchored variants must keep their leading word-boundary semantics: a
// candidate offset is a slice boundary, where \b would always look satisfied.
func TestWordBoundaryPreservedUnderCandidateScan(t *testing.T) {
	r := New(Options{})
	for _, in := range []string{
		"XAKIAIOSFODNN7EXAMPLE",
		"xghp_EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0A",
	} {
		if got := r.String(in); got != in {
			t.Errorf("word boundary lost, matched inside a longer token\n  in:  %q\n  out: %q", in, got)
		}
	}
}

func TestAnchoredVariantsCompile(t *testing.T) {
	r := New(Options{EnablePII: true})
	for _, d := range r.dets {
		if len(d.starts) > 0 && d.anchored == nil {
			t.Errorf("detector %q declares starts but has no anchored pattern", d.name)
		}
	}
}
