package flow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestValidateFlowID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid simple", "my-flow", false},
		{"valid single word", "flow", false},
		{"valid with numbers", "flow-123", false},
		{"valid complex", "my-flow-v2", false},
		{"empty", "", true},
		{"uppercase", "MyFlow", true},
		{"underscore", "my_flow", true},
		{"starts with hyphen", "-flow", true},
		{"ends with hyphen", "flow-", true},
		{"double hyphen", "my--flow", true},
		{"too long", string(make([]byte, 65)), true},
		{"spaces", "my flow", true},
		// Namespaced IDs (custom-path flows): exactly one "/" joining
		// two kebab-case segments is admitted; anything else is not.
		{"valid namespaced", "team/name", false},
		{"valid namespaced complex", "id/fix-failing-tests", false},
		{"valid namespaced with numbers", "team-2/flow-v2", false},
		{"three segments", "a/b/c", true},
		{"leading slash", "/x", true},
		{"trailing slash", "x/", true},
		{"bare slash", "/", true},
		{"empty middle segment", "a//c", true},
		{"uppercase namespace", "Team/name", true},
		{"uppercase name", "team/Name", true},
		{"namespaced too long", strings.Repeat("a", 32) + "/" + strings.Repeat("b", 32), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlowID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFlowID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestValidateStepID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid", "analyse-issue", false},
		{"valid single", "step", false},
		{"empty", "", true},
		{"uppercase", "Step", true},
		{"too long", string(make([]byte, 65)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStepID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStepID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestValidateFlow(t *testing.T) {
	tests := []struct {
		name    string
		flow    Flow
		wantErr error
	}{
		{
			name: "valid flow",
			flow: Flow{
				ID: "test-flow",
				Spec: FlowSpec{
					Steps: []Step{
						{ID: "step-a", Prompt: "do something"},
						{ID: "step-b", Prompt: "do more", Rules: []Rule{{If: "${args.x} == y", Then: "step-a"}}},
					},
				},
			},
			wantErr: nil,
		},
		{
			name:    "no steps",
			flow:    Flow{ID: "empty", Spec: FlowSpec{}},
			wantErr: ErrNoSteps,
		},
		{
			name: "duplicate step IDs",
			flow: Flow{
				ID: "dup",
				Spec: FlowSpec{
					Steps: []Step{
						{ID: "step-a", Prompt: "x"},
						{ID: "step-a", Prompt: "y"},
					},
				},
			},
			wantErr: ErrDuplicateStepID,
		},
		{
			name: "invalid step ID",
			flow: Flow{
				ID: "bad-step",
				Spec: FlowSpec{
					Steps: []Step{{ID: "BAD", Prompt: "x"}},
				},
			},
			wantErr: ErrInvalidStepID,
		},
		{
			name: "rule references non-existent step",
			flow: Flow{
				ID: "bad-rule",
				Spec: FlowSpec{
					Steps: []Step{
						{ID: "step-a", Prompt: "x", Rules: []Rule{{If: "${args.x} == y", Then: "nonexistent"}}},
					},
				},
			},
			wantErr: ErrInvalidRule,
		},
		{
			name: "fallback references non-existent step",
			flow: Flow{
				ID: "bad-fallback",
				Spec: FlowSpec{
					Steps: []Step{
						{ID: "step-a", Prompt: "x", Fallback: &Fallback{Retry: 1, To: "nonexistent"}},
					},
				},
			},
			wantErr: ErrInvalidFallback,
		},
		{
			name: "maxTurns positive is valid",
			flow: Flow{
				ID: "mt-positive",
				Spec: FlowSpec{
					Steps: []Step{{ID: "step-a", Prompt: "x", MaxTurns: 5}},
				},
			},
			wantErr: nil,
		},
		{
			name: "maxTurns zero (unset) is valid",
			flow: Flow{
				ID: "mt-zero",
				Spec: FlowSpec{
					Steps: []Step{{ID: "step-a", Prompt: "x", MaxTurns: 0}},
				},
			},
			wantErr: nil,
		},
		{
			name: "maxTurns negative rejected",
			flow: Flow{
				ID: "mt-neg",
				Spec: FlowSpec{
					Steps: []Step{{ID: "step-a", Prompt: "x", MaxTurns: -1}},
				},
			},
			wantErr: ErrInvalidMaxTurns,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlow(&tt.flow)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("validateFlow() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateFlow() expected error containing %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					if !strings.Contains(err.Error(), tt.wantErr.Error()) {
						t.Errorf("validateFlow() error = %v, want %v", err, tt.wantErr)
					}
				}
			}
		})
	}
}

func TestParseFlowFile(t *testing.T) {
	t.Run("valid flow file", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: Test Flow
description: A test flow
flow:
  steps:
    - id: step-one
      prompt: "Do ${args.prompt}"
    - id: step-two
      prompt: "Continue"
      rules:
        - if: "${args.status} == done"
          then: step-one
`
		path := filepath.Join(dir, "test-flow.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		f, err := parseFlowFile(path)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}

		if f.ID != "test-flow" {
			t.Errorf("ID = %q, want %q", f.ID, "test-flow")
		}
		if f.Name != "Test Flow" {
			t.Errorf("Name = %q, want %q", f.Name, "Test Flow")
		}
		if len(f.Spec.Steps) != 2 {
			t.Errorf("Steps count = %d, want 2", len(f.Spec.Steps))
		}
	})

	t.Run("invalid YAML", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad-flow.yaml")
		if err := os.WriteFile(path, []byte("not: valid: yaml: ["), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := parseFlowFile(path)
		if err == nil {
			t.Error("expected error for invalid YAML")
		}
	})

	t.Run("invalid flow ID from filename", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: Bad
description: Bad flow
flow:
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "BAD_NAME.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := parseFlowFile(path)
		if err == nil {
			t.Error("expected error for invalid flow ID")
		}
	})

	t.Run("yml extension", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: YML Flow
description: A yml flow
flow:
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "yml-flow.yml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		f, err := parseFlowFile(path)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if f.ID != "yml-flow" {
			t.Errorf("ID = %q, want %q", f.ID, "yml-flow")
		}
	})

	t.Run("session.resume_on_failure is accepted and round-trips", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: Resume On Failure Flow
description: A flow that opts into retry-from-failure
flow:
  session:
    prefix: "${args.id}"
    resume_on_failure: true
  args:
    id:
      type: string
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "resume-on-failure-flow.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		f, err := parseFlowFile(path)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if !f.Spec.Session.ResumeOnFailure {
			t.Errorf("ResumeOnFailure = false, want true")
		}
		if f.Spec.Session.Prefix != "${args.id}" {
			t.Errorf("Prefix = %q, want %q", f.Spec.Session.Prefix, "${args.id}")
		}
	})

	t.Run("session.resume_on_failure defaults to false when omitted", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: Default Session Flow
description: No resume_on_failure key
flow:
  session:
    prefix: "${args.id}"
  args:
    id:
      type: string
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "default-session-flow.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		f, err := parseFlowFile(path)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if f.Spec.Session.ResumeOnFailure {
			t.Errorf("ResumeOnFailure = true, want false (default)")
		}
	})

	t.Run("typo in session block is rejected with ErrInvalidYAML", func(t *testing.T) {
		dir := t.TempDir()
		// `resume_on_fail` (missing `ure`) is the kind of typo that
		// would otherwise be silently dropped by the typed YAML
		// decode. Authors deserve a signal so they can fix the
		// config; the gate test in service_retrigger_test.go relies
		// on this signal to ensure ResumeOnFailure actually reaches
		// the runtime when the author intends it to.
		content := `name: Typo Flow
description: Has a typo'd session key
flow:
  session:
    prefix: "${args.id}"
    resume_on_fail: true
  args:
    id:
      type: string
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "typo-flow.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := parseFlowFile(path)
		if err == nil {
			t.Fatal("expected error for typo'd session key, got nil")
		}
		if !errors.Is(err, ErrInvalidYAML) {
			t.Errorf("error = %v, want wraps ErrInvalidYAML", err)
		}
		if !strings.Contains(err.Error(), "resume_on_fail") {
			t.Errorf("error message should name the unknown key %q; got %v", "resume_on_fail", err)
		}
	})

	t.Run("no session block is accepted", func(t *testing.T) {
		// Flows without a session block are valid — the runtime
		// derives a Unix-timestamp prefix in resolveSessionPrefix.
		// The validation only fires on keys WITHIN session, so an
		// absent block must not trip it.
		dir := t.TempDir()
		content := `name: No Session Flow
description: Has no session block
flow:
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "no-session-flow.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		f, err := parseFlowFile(path)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if f.Spec.Session.Prefix != "" {
			t.Errorf("Prefix = %q, want \"\"", f.Spec.Session.Prefix)
		}
		if f.Spec.Session.ResumeOnFailure {
			t.Errorf("ResumeOnFailure = true, want false")
		}
	})

	t.Run("disabled flow", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: Disabled Flow
description: A disabled flow
disabled: true
flow:
  steps:
    - id: step-one
      prompt: "x"
`
		path := filepath.Join(dir, "disabled-flow.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		f, err := parseFlowFile(path)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if !f.Disabled {
			t.Error("expected Disabled = true")
		}
	})
}

func TestScanFlowDirectory(t *testing.T) {
	t.Run("non-existent directory", func(t *testing.T) {
		result := scanFlowDirectory("/nonexistent/path")
		if result != nil {
			t.Errorf("expected nil for non-existent dir, got %v", result)
		}
	})

	t.Run("directory with flows", func(t *testing.T) {
		dir := t.TempDir()
		content := `name: Flow One
description: First flow
flow:
  steps:
    - id: step-one
      prompt: "x"
`
		if err := os.WriteFile(filepath.Join(dir, "flow-one.yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644); err != nil {
			t.Fatal(err)
		}

		flows := scanFlowDirectory(dir)
		if len(flows) != 1 {
			t.Errorf("expected 1 flow, got %d", len(flows))
		}
		if len(flows) > 0 && flows[0].ID != "flow-one" {
			t.Errorf("flow ID = %q, want %q", flows[0].ID, "flow-one")
		}
	})
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"307200", 307200, false},
		{"300k", 300 * 1024, false},
		{"300K", 300 * 1024, false},
		{"300kb", 300 * 1024, false},
		{"300KB", 300 * 1024, false},
		{"300 kb", 300 * 1024, false},
		{"300kib", 300 * 1024, false},
		{"2m", 2 * 1024 * 1024, false},
		{"2MB", 2 * 1024 * 1024, false},
		{"2mib", 2 * 1024 * 1024, false},
		{"0", 0, false},
		{"", 0, true},
		{"abc", 0, true},
		{"300kb kb", 0, true},
		{"1.5m", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseByteSize(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseByteSize(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseByteSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseFlowFile_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	// Write a file that exceeds the default 300 KB limit. The body is
	// syntactically valid YAML padding inside a comment so the size
	// check fires BEFORE the YAML decoder would otherwise pass.
	header := "name: Big\ndescription: Big flow\nflow:\n  steps:\n    - id: step-one\n      prompt: \"x\"\n# "
	padding := strings.Repeat("x", 320*1024)
	path := filepath.Join(dir, "big-flow.yaml")
	if err := os.WriteFile(path, []byte(header+padding+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := parseFlowFile(path)
	if err == nil {
		t.Fatal("expected error for oversized flow file")
	}
	if !errors.Is(err, ErrInvalidYAML) {
		t.Errorf("error not wrapping ErrInvalidYAML: %v", err)
	}
	if !strings.Contains(err.Error(), "file exceeds") {
		t.Errorf("error text should mention size ceiling: %v", err)
	}
	if !strings.Contains(err.Error(), "OPENCODE_MAX_FLOW_FILE_SIZE") {
		t.Errorf("error text should point at the env knob: %v", err)
	}
}

// writeMinimalFlow writes a syntactically valid single-step flow YAML
// named <id>.yaml into dir. The display name doubles as a marker so
// tests can tell which copy of a duplicated basename won discovery.
func writeMinimalFlow(t *testing.T, dir, id, displayName string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "name: " + displayName + "\ndescription: test flow\nflow:\n  steps:\n    - id: step-one\n      prompt: \"x\"\n"
	path := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverCustomPathFlows(t *testing.T) {
	tmpDir := t.TempDir()

	// <tmp>/workspace/id/flows/fix-failing-tests.yaml — the namespace
	// must derive from the flows dir's PARENT basename ("id"), not from
	// any other path component.
	flowsDir := filepath.Join(tmpDir, "workspace", "id", "flows")
	writeMinimalFlow(t, flowsDir, "fix-failing-tests", "Fix Failing Tests")
	// A non-YAML file in the same dir must be ignored.
	if err := os.WriteFile(filepath.Join(flowsDir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Absolute path.
	absFlows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{flowsDir},
	})
	if len(absFlows) != 1 {
		t.Fatalf("absolute path: got %d flows, want 1", len(absFlows))
	}
	if absFlows[0].ID != "id/fix-failing-tests" {
		t.Errorf("absolute path: ID = %q, want id/fix-failing-tests", absFlows[0].ID)
	}
	if absFlows[0].Name != "Fix Failing Tests" {
		t.Errorf("absolute path: Name = %q, want Fix Failing Tests", absFlows[0].Name)
	}

	// Absolute path WITH a trailing slash: the path must be cleaned
	// before namespace derivation, so the namespace is still the team
	// dir ("id") — not "flows" (filepath.Dir of an uncleaned
	// ".../flows/" is ".../flows", which would collapse every
	// trailing-slash entry into one colliding "flows/" namespace).
	trailingFlows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{flowsDir + string(filepath.Separator)},
	})
	if len(trailingFlows) != 1 {
		t.Fatalf("trailing slash: got %d flows, want 1", len(trailingFlows))
	}
	if trailingFlows[0].ID != "id/fix-failing-tests" {
		t.Errorf("trailing slash: ID = %q, want id/fix-failing-tests", trailingFlows[0].ID)
	}

	// Relative path, resolved against WorkingDir; same namespacing.
	relFlows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{filepath.Join("workspace", "id", "flows")},
	})
	if len(relFlows) != 1 {
		t.Fatalf("relative path: got %d flows, want 1", len(relFlows))
	}
	if relFlows[0].ID != "id/fix-failing-tests" {
		t.Errorf("relative path: ID = %q, want id/fix-failing-tests", relFlows[0].ID)
	}

	// Missing path is skipped without error.
	missingFlows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{filepath.Join(tmpDir, "does-not-exist")},
	})
	if len(missingFlows) != 0 {
		t.Errorf("missing path: got %d flows, want 0", len(missingFlows))
	}

	// A non-directory path is skipped without error.
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nonDirFlows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{filePath},
	})
	if len(nonDirFlows) != 0 {
		t.Errorf("non-dir path: got %d flows, want 0", len(nonDirFlows))
	}

	// Nil config and empty FlowPaths both yield nothing.
	if got := discoverCustomPathFlows(nil); got != nil {
		t.Errorf("nil config: got %v, want nil", got)
	}
	if got := discoverCustomPathFlows(&config.Config{WorkingDir: tmpDir}); len(got) != 0 {
		t.Errorf("empty FlowPaths: got %d flows, want 0", len(got))
	}
}

// TestDiscoverCustomPathFlows_InvalidNamespaceSkipped verifies that a
// flows dir whose parent basename is not kebab-case yields no flows —
// the namespaced ID fails validateFlowID and the flow is skipped rather
// than registered under an unaddressable ID.
func TestDiscoverCustomPathFlows_InvalidNamespaceSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	flowsDir := filepath.Join(tmpDir, "My_Team", "flows")
	writeMinimalFlow(t, flowsDir, "some-flow", "Some Flow")

	flows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{flowsDir},
	})
	if len(flows) != 0 {
		t.Errorf("invalid namespace: got %d flows, want 0 (got IDs %v)", len(flows), flowIDs(flows))
	}
}

// TestDiscoverCustomPathFlows_RespectsSizeCap verifies the existing
// per-file size cap applies to custom-path flows: an oversized YAML is
// dropped (parseFlowFile rejects it inside scanFlowDirectory) while a
// small sibling in the same directory is still discovered.
func TestDiscoverCustomPathFlows_RespectsSizeCap(t *testing.T) {
	tmpDir := t.TempDir()
	flowsDir := filepath.Join(tmpDir, "id", "flows")
	writeMinimalFlow(t, flowsDir, "small-flow", "Small Flow")

	header := "name: Big\ndescription: Big flow\nflow:\n  steps:\n    - id: step-one\n      prompt: \"x\"\n# "
	padding := strings.Repeat("x", 320*1024)
	if err := os.WriteFile(filepath.Join(flowsDir, "big-flow.yaml"), []byte(header+padding+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	flows := discoverCustomPathFlows(&config.Config{
		WorkingDir: tmpDir,
		FlowPaths:  []string{flowsDir},
	})
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1 (oversized must be dropped); IDs %v", len(flows), flowIDs(flows))
	}
	if flows[0].ID != "id/small-flow" {
		t.Errorf("ID = %q, want id/small-flow", flows[0].ID)
	}
}

// TestCustomPathFlowDoesNotShadowSharedFlow drives the full discoverFlows
// pipeline: a project flow and a custom-path flow share the basename
// `dup`, and both must be retrievable under their distinct IDs (`dup`
// and `id/dup`) — namespacing makes shadowing structurally impossible.
func TestCustomPathFlowDoesNotShadowSharedFlow(t *testing.T) {
	workDir := t.TempDir()
	// Isolate global discovery (~/.config/opencode/flows, ~/.agents/flows)
	// from the developer's real home directory.
	t.Setenv("HOME", t.TempDir())

	// Shared project flow: <work>/.opencode/flows/dup.yaml → ID "dup".
	writeMinimalFlow(t, filepath.Join(workDir, ".opencode", "flows"), "dup", "Shared Version")
	// Team flow with the same basename: <work>/teams/id/flows/dup.yaml → ID "id/dup".
	customDir := filepath.Join(workDir, "teams", "id", "flows")
	writeMinimalFlow(t, customDir, "dup", "Team Version")

	config.Reset()
	if _, err := config.Load(workDir, false); err != nil {
		t.Logf("config.Load warning: %v", err)
	}
	t.Cleanup(config.Reset)
	config.Get().FlowPaths = []string{customDir}

	Invalidate()
	defer Invalidate()

	shared, err := Get("dup")
	if err != nil {
		t.Fatalf("Get(dup): %v", err)
	}
	if shared.Name != "Shared Version" {
		t.Errorf("shared flow Name = %q, want Shared Version", shared.Name)
	}

	team, err := Get("id/dup")
	if err != nil {
		t.Fatalf("Get(id/dup): %v", err)
	}
	if team.Name != "Team Version" {
		t.Errorf("team flow Name = %q, want Team Version", team.Name)
	}

	if shared.Location == team.Location {
		t.Errorf("shared and team flow resolved to the same file: %s", shared.Location)
	}
}

func flowIDs(flows []Flow) []string {
	ids := make([]string, len(flows))
	for i, f := range flows {
		ids[i] = f.ID
	}
	return ids
}

func TestGetAndAll(t *testing.T) {
	tmpDir := t.TempDir()
	config.Reset()
	if _, err := config.Load(tmpDir, false); err != nil {
		t.Logf("config.Load warning: %v", err)
	}
	t.Cleanup(config.Reset)

	Invalidate()
	defer Invalidate()

	_, err := Get("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent flow")
	}
}
