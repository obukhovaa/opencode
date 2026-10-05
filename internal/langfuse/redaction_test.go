package langfuse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

// Defused synthetic credentials — same shapes as the production findings, no
// access to anything. See internal/redact/testdata/corpus.md.
const (
	fakePAT      = "glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0"
	fakeAWSKey   = "AKIAIOSFODNN7EXAMPLE"
	fakeCloneURL = "https://oauth2:" + fakePAT + "@gitlab.com/piano/composer/agents/developer.git"
)

// withRedaction installs a redactor built from rc for the duration of the test.
//
// It deliberately does NOT go through config.Load: that function is load-once
// per process, so the first test to call it wins and every later call is a
// silent no-op — which makes a "disabled" test pass against an enabled
// redactor. buildRedactor is exercised directly instead, and the loader itself
// is covered by internal/config's viper round-trip tests.
func withRedaction(t *testing.T, rc *config.RedactionConfig) {
	t.Helper()
	redactorOverride.Store(buildRedactor(rc))
	t.Cleanup(func() { redactorOverride.Store(nil) })
}

func TestToolInputOutputRedacted(t *testing.T) {
	withRedaction(t, nil) // unconfigured => on by default
	c, sr := newTestClient()

	span := c.ToolStart(context.Background(), ToolParams{
		Name:  "webfetch",
		Input: map[string]any{"url": fakeCloneURL},
	})
	span.SetOutput("cloned ok, token was " + fakePAT)
	span.End()

	spans := sr.Ended()
	in, ok := attrValue(spans, "webfetch", "langfuse.observation.input")
	if !ok {
		t.Fatal("no tool input attribute")
	}
	if strings.Contains(in, fakePAT) {
		t.Errorf("tool input leaked the PAT: %s", in)
	}
	if !strings.Contains(in, "[REDACTED:") {
		t.Errorf("tool input not marked redacted: %s", in)
	}
	if !strings.Contains(in, "gitlab.com") {
		t.Errorf("surrounding input content lost: %s", in)
	}

	out, ok := attrValue(spans, "webfetch", "langfuse.observation.output")
	if !ok {
		t.Fatal("no tool output attribute")
	}
	if strings.Contains(out, fakePAT) {
		t.Errorf("tool output leaked the PAT: %s", out)
	}
}

func TestGenerationInputOutputRedacted(t *testing.T) {
	withRedaction(t, nil)
	c, sr := newTestClient()

	span := c.GenerationStart(context.Background(), GenerationParams{
		Name:  "coder/model",
		Model: "model",
		Input: "here is my key " + fakeAWSKey,
	})
	span.SetGenerationOutput("I will use " + fakeAWSKey)
	span.End()

	spans := sr.Ended()
	for _, key := range []string{"langfuse.observation.input", "langfuse.observation.output"} {
		v, ok := attrValue(spans, "coder/model", key)
		if !ok {
			t.Fatalf("missing %s", key)
		}
		if strings.Contains(v, fakeAWSKey) {
			t.Errorf("%s leaked the AWS key: %s", key, v)
		}
	}
}

func TestTraceInputOutputRedacted(t *testing.T) {
	withRedaction(t, nil)
	c, sr := newTestClient()

	ctx := c.TraceStart(context.Background(), TraceParams{
		Name:     "turn",
		Input:    "clone " + fakeCloneURL,
		Metadata: map[string]any{"cmd": "git clone " + fakeCloneURL},
	})
	SetTraceOutput(ctx, "done, key "+fakeAWSKey)
	c.TraceEnd(ctx)

	spans := sr.Ended()
	for _, key := range []string{"langfuse.trace.input", "langfuse.trace.output", "langfuse.trace.metadata.cmd"} {
		v, ok := attrValue(spans, "turn", key)
		if !ok {
			t.Fatalf("missing %s", key)
		}
		if strings.Contains(v, fakePAT) || strings.Contains(v, fakeAWSKey) {
			t.Errorf("%s leaked a credential: %s", key, v)
		}
	}
}

// Error text is caller content too: a failed clone reports the URL it tried.
func TestSpanErrorRedacted(t *testing.T) {
	withRedaction(t, nil)
	c, sr := newTestClient()

	span := c.ToolStart(context.Background(), ToolParams{Name: "bash"})
	span.SetError(errors.New("fatal: could not read from " + fakeCloneURL))
	span.End()

	spans := sr.Ended()
	msg, ok := attrValue(spans, "bash", "langfuse.observation.status_message")
	if !ok {
		t.Fatal("no status_message attribute")
	}
	if strings.Contains(msg, fakePAT) {
		t.Errorf("error message leaked the PAT: %s", msg)
	}
	if !strings.Contains(msg, "fatal: could not read from") {
		t.Errorf("error text destroyed rather than redacted: %s", msg)
	}
	var found bool
	for _, s := range spans {
		if s.Name() == "bash" && s.Status().Description != "" {
			found = true
			if strings.Contains(s.Status().Description, fakePAT) {
				t.Errorf("span status description leaked the PAT: %s", s.Status().Description)
			}
		}
	}
	if !found {
		t.Error("span was not marked errored")
	}
}

// Redaction must run BEFORE truncation. A credential straddling the cap would
// otherwise be cut in half and its prefix shipped in the clear.
func TestRedactBeforeTruncate(t *testing.T) {
	withRedaction(t, nil)
	c, sr := newTestClient()

	// Place the credential so it spans the maxIOSize boundary.
	filler := strings.Repeat("x", maxIOSize-len(fakePAT)/2)
	span := c.ToolStart(context.Background(), ToolParams{Name: "bash"})
	span.SetOutput(filler + fakePAT + strings.Repeat("y", 100))
	span.End()

	out, ok := attrValue(sr.Ended(), "bash", "langfuse.observation.output")
	if !ok {
		t.Fatal("no output attribute")
	}
	// No fragment of the token, not just no whole token.
	for i := 0; i+12 <= len(fakePAT); i++ {
		if frag := fakePAT[i : i+12]; strings.Contains(out, frag) {
			t.Fatalf("credential fragment %q survived the truncation boundary", frag)
		}
	}
	if len(out) > maxIOSize+len("...[truncated]") {
		t.Errorf("cap not respected: %d bytes", len(out))
	}
}

// Structure, timing and cost are the diagnostic signal worth keeping, and the
// ticket calls them out as safe. They must be byte-identical either way.
func TestDiagnosticAttributesUnaffected(t *testing.T) {
	collect := func(rc *config.RedactionConfig) map[string]string {
		withRedaction(t, rc)
		c, sr := newTestClient()
		span := c.GenerationStart(context.Background(), GenerationParams{
			Name: "coder/claude", Model: "claude", Input: "key " + fakeAWSKey,
		})
		span.SetUsage(&Usage{Input: 10, Output: 20, Total: 30, TotalCost: 0.5})
		span.End()

		got := map[string]string{}
		for _, s := range sr.Ended() {
			got["__name"] = s.Name()
			for _, kv := range s.Attributes() {
				k := string(kv.Key)
				if strings.Contains(k, ".input") || strings.Contains(k, ".output") {
					continue
				}
				got[k] = kv.Value.Emit()
			}
		}
		return got
	}

	on := collect(nil)
	off := collect(&config.RedactionConfig{Enabled: boolPtr(false)})

	if len(on) == 0 {
		t.Fatal("no diagnostic attributes captured")
	}
	for k, v := range off {
		if on[k] != v {
			t.Errorf("diagnostic attribute %q changed with redaction on: %q vs %q", k, on[k], v)
		}
	}
	if len(on) != len(off) {
		t.Errorf("attribute count differs: on=%d off=%d", len(on), len(off))
	}
}

func TestRedactionCanBeDisabled(t *testing.T) {
	withRedaction(t, &config.RedactionConfig{Enabled: boolPtr(false)})
	c, sr := newTestClient()

	span := c.ToolStart(context.Background(), ToolParams{Name: "bash"})
	span.SetOutput("token " + fakePAT)
	span.End()

	out, _ := attrValue(sr.Ended(), "bash", "langfuse.observation.output")
	if !strings.Contains(out, fakePAT) {
		t.Errorf("redaction ran despite enabled:false: %s", out)
	}
}

func TestCustomRuleAppliesToSpans(t *testing.T) {
	withRedaction(t, &config.RedactionConfig{
		Rules: []config.RedactionRule{{Name: "piano-id", Pattern: `PI-[0-9]{12}`}},
	})
	c, sr := newTestClient()

	span := c.ToolStart(context.Background(), ToolParams{Name: "bash"})
	span.SetOutput("record PI-123456789012 done")
	span.End()

	out, _ := attrValue(sr.Ended(), "bash", "langfuse.observation.output")
	if strings.Contains(out, "PI-123456789012") {
		t.Errorf("custom rule did not apply to span output: %s", out)
	}
	if !strings.Contains(out, "[REDACTED:piano-id:") {
		t.Errorf("custom rule name missing from marker: %s", out)
	}
}

// End-to-end over the recorder: a realistic tool call carrying several corpus
// credentials must export no cleartext anywhere in the span.
func TestEndToEndNoCleartextInExportedSpan(t *testing.T) {
	withRedaction(t, nil)
	c, sr := newTestClient()

	stdout := fmt.Sprintf(
		`{"file_path":".env.prod","content":"AWS_ACCESS_KEY_ID=%s\nSLACK_BOT_TOKEN=xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M"}
curl -H "Authorization: Bearer sk-_EXAMPLEXr4Kd9Wb2Nv7Lp" https://litellm.example/key/info
remote: %s`, fakeAWSKey, fakeCloneURL)

	ctx := c.TraceStart(context.Background(), TraceParams{Name: "turn", Input: "read prod env"})
	span := c.ToolStart(ctx, ToolParams{Name: "bash", Input: map[string]any{"command": "cat .env.prod"}})
	span.SetOutput(stdout)
	span.End()
	SetTraceOutput(ctx, "done")
	c.TraceEnd(ctx)

	secrets := []string{
		fakeAWSKey, fakePAT,
		"xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M",
		"sk-_EXAMPLEXr4Kd9Wb2Nv7Lp",
	}
	var checked int
	for _, s := range sr.Ended() {
		for _, kv := range s.Attributes() {
			v := kv.Value.Emit()
			checked++
			for _, secret := range secrets {
				if strings.Contains(v, secret) {
					t.Errorf("span %q attribute %q leaked %q\n  value: %s", s.Name(), kv.Key, secret, v)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no attributes examined")
	}
	out, ok := attrValue(sr.Ended(), "bash", "langfuse.observation.output")
	if !ok {
		t.Fatal("no tool output")
	}
	if n := strings.Count(out, "[REDACTED:"); n < 4 {
		t.Errorf("expected at least 4 markers, got %d: %s", n, out)
	}
}

func boolPtr(b bool) *bool { return &b }

// buildRedactor is the config->options mapping, and the seam the tests above
// install through. Cover it directly so a field dropped in the mapping cannot
// hide behind an override.
func TestBuildRedactor_MapsConfig(t *testing.T) {
	const pat = "glpat-EXAMPLEn8KpLdR4T"

	t.Run("nil config is enabled with builtins", func(t *testing.T) {
		if got := buildRedactor(nil).String(pat); !strings.Contains(got, "[REDACTED:gitlab-pat") {
			t.Errorf("nil config did not redact: %s", got)
		}
	})

	t.Run("enabled false is pass-through", func(t *testing.T) {
		rc := &config.RedactionConfig{Enabled: boolPtr(false)}
		if got := buildRedactor(rc).String(pat); got != pat {
			t.Errorf("want pass-through, got %s", got)
		}
	})

	t.Run("mode is applied", func(t *testing.T) {
		rc := &config.RedactionConfig{Mode: "strict"}
		if got := buildRedactor(rc).String(pat); got != "[REDACTED:gitlab-pat]" {
			t.Errorf("strict mode not applied: %s", got)
		}
	})

	t.Run("disableBuiltins is applied", func(t *testing.T) {
		rc := &config.RedactionConfig{DisableBuiltins: []string{"gitlab-pat"}}
		if got := buildRedactor(rc).String(pat); got != pat {
			t.Errorf("detector not disabled: %s", got)
		}
	})

	t.Run("allowlist is applied", func(t *testing.T) {
		rc := &config.RedactionConfig{Allowlist: []string{pat}}
		if got := buildRedactor(rc).String(pat); got != pat {
			t.Errorf("allowlist not applied: %s", got)
		}
	})

	t.Run("pii toggle is applied", func(t *testing.T) {
		const in = "alice@piano.io"
		if got := buildRedactor(&config.RedactionConfig{}).String(in); got != in {
			t.Errorf("pii on by default: %s", got)
		}
		if got := buildRedactor(&config.RedactionConfig{PII: true}).String(in); got == in {
			t.Errorf("pii toggle ignored: %s", got)
		}
	})

	t.Run("rules carry name, group and replacement", func(t *testing.T) {
		rc := &config.RedactionConfig{Rules: []config.RedactionRule{
			{Name: "kv", Pattern: `MYKEY=(\S+)`, Group: 1, Replacement: "[GONE]"},
		}}
		got := buildRedactor(rc).String("MYKEY=hunter2secret")
		if got != "MYKEY=[GONE]" {
			t.Errorf("rule fields not mapped: %s", got)
		}
	})
}
