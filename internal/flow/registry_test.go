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
