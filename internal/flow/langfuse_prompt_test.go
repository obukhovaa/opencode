package flow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFlow_PromptSource(t *testing.T) {
	tests := []struct {
		name    string
		step    Step
		wantErr bool
		errHas  string
	}{
		{
			name: "inline prompt only",
			step: Step{ID: "s", Prompt: "do the thing"},
		},
		{
			name: "langfuse reference only",
			step: Step{ID: "s", LangfusePromptPath: "flows/x/main"},
		},
		{
			name: "langfuse reference with an explicit label",
			step: Step{ID: "s", LangfusePromptPath: "flows/x/main", LangfusePromptLabel: "staging"},
		},
		{
			name:    "both sources is ambiguous",
			step:    Step{ID: "s", Prompt: "inline", LangfusePromptPath: "flows/x/main"},
			wantErr: true,
			errHas:  "declares both",
		},
		{
			name:    "neither source",
			step:    Step{ID: "s"},
			wantErr: true,
			errHas:  "declares neither",
		},
		{
			name:    "a whitespace-only inline prompt is not a prompt",
			step:    Step{ID: "s", Prompt: "   \n "},
			wantErr: true,
			errHas:  "declares neither",
		},
		{
			name:    "label without a path",
			step:    Step{ID: "s", Prompt: "inline", LangfusePromptLabel: "staging"},
			wantErr: true,
			errHas:  "langfusePromptLabel without langfusePromptPath",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlow(&Flow{ID: "f", Spec: FlowSpec{Steps: []Step{tt.step}}})
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("validateFlow() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateFlow() error = nil, want an error")
			}
			if !errors.Is(err, ErrInvalidPromptSource) {
				t.Errorf("error = %v, want ErrInvalidPromptSource", err)
			}
			if !strings.Contains(err.Error(), tt.errHas) {
				t.Errorf("error = %v, want one containing %q", err, tt.errHas)
			}
		})
	}
}

// stubPromptResolver records what it was asked for and returns a canned
// answer, so the flow engine's half of the contract can be exercised
// without a Langfuse backend.
type stubPromptResolver struct {
	text      string
	err       error
	gotPath   string
	gotLabel  string
	callCount int
}

func (s *stubPromptResolver) ResolvePrompt(_ context.Context, path, label string) (string, error) {
	s.callCount++
	s.gotPath = path
	s.gotLabel = label
	return s.text, s.err
}

func TestResolveStepPrompt(t *testing.T) {
	t.Run("an inline prompt is returned verbatim and never resolved", func(t *testing.T) {
		stub := &stubPromptResolver{text: "should not be used"}
		svc := &service{promptResolver: stub}

		got, err := svc.resolveStepPrompt(context.Background(), Step{ID: "s", Prompt: "inline ${args.x}"})
		if err != nil {
			t.Fatalf("resolveStepPrompt() error: %v", err)
		}
		if got != "inline ${args.x}" {
			t.Errorf("prompt = %q, want the inline text untouched", got)
		}
		if stub.callCount != 0 {
			t.Errorf("resolver called %d times, want 0 for an inline prompt", stub.callCount)
		}
	})

	t.Run("a reference is resolved and its label passed through", func(t *testing.T) {
		stub := &stubPromptResolver{text: "managed prompt"}
		svc := &service{promptResolver: stub}

		got, err := svc.resolveStepPrompt(context.Background(), Step{
			ID:                  "s",
			LangfusePromptPath:  "flows/x/main",
			LangfusePromptLabel: "staging",
		})
		if err != nil {
			t.Fatalf("resolveStepPrompt() error: %v", err)
		}
		if got != "managed prompt" {
			t.Errorf("prompt = %q, want the resolved text", got)
		}
		if stub.gotPath != "flows/x/main" || stub.gotLabel != "staging" {
			t.Errorf("resolver got (%q, %q), want (flows/x/main, staging)", stub.gotPath, stub.gotLabel)
		}
	})

	t.Run("an empty label is passed through for the client to default", func(t *testing.T) {
		stub := &stubPromptResolver{text: "managed prompt"}
		svc := &service{promptResolver: stub}

		if _, err := svc.resolveStepPrompt(context.Background(), Step{ID: "s", LangfusePromptPath: "a"}); err != nil {
			t.Fatalf("resolveStepPrompt() error: %v", err)
		}
		if stub.gotLabel != "" {
			t.Errorf("label = %q, want empty — defaulting belongs to the prompt client", stub.gotLabel)
		}
	})

	// Validation treats a whitespace-only path as "no reference" (it trims
	// before comparing), so the runtime must agree. An untrimmed check here
	// sent "  " to the client and failed the step at run time over a key
	// the loader had already decided was not a reference.
	t.Run("a whitespace-only path is not a reference", func(t *testing.T) {
		stub := &stubPromptResolver{text: "should not be used"}
		svc := &service{}
		svc.SetPromptResolver(stub)

		got, err := svc.resolveStepPrompt(context.Background(), Step{
			ID:                 "s",
			Prompt:             "inline",
			LangfusePromptPath: "   ",
		})
		if err != nil {
			t.Fatalf("resolveStepPrompt() error: %v", err)
		}
		if got != "inline" {
			t.Errorf("prompt = %q, want the inline text", got)
		}
		if stub.callCount != 0 {
			t.Errorf("resolver called %d times, want 0", stub.callCount)
		}
	})

	t.Run("a resolution failure names the step and the path", func(t *testing.T) {
		sentinel := errors.New("langfuse exploded")
		svc := &service{promptResolver: &stubPromptResolver{err: sentinel}}

		_, err := svc.resolveStepPrompt(context.Background(), Step{ID: "prepare", LangfusePromptPath: "flows/x/main"})
		if err == nil {
			t.Fatal("resolveStepPrompt() error = nil, want the resolver's error")
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want it to wrap the resolver's error", err)
		}
		for _, want := range []string{"prepare", "flows/x/main"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want one naming %q", err, want)
			}
		}
	})
}

// TestParseFlowFile_PromptSourceReplacesSource pins that a step declaring
// one prompt source does not inherit the other from its templates.
//
// The two sources are mutually exclusive (validateStepPromptSource), so
// inheriting them independently produced two failures that read as loader
// bugs rather than as author mistakes: a step overriding a template's
// `prompt` with a `langfusePromptPath` collided on "declares both", and a
// step overriding an inherited path with an inline prompt was left holding
// the template's orphaned `langfusePromptLabel`.
func TestParseFlowFile_PromptSourceReplacesSource(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "base.yaml"), `.inline:
  agent: template-agent
  maxTurns: 15
  prompt: "template prompt"
.managed:
  agent: template-agent
  maxTurns: 15
  langfusePromptPath: flows/template/main
  langfusePromptLabel: staging
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "sources.yaml"), `name: Sources
description: d
include:
  - local: steps/base.yaml
flow:
  steps:
    - id: reference-over-inline
      extends: [".inline"]
      langfusePromptPath: flows/own/main
    - id: inline-over-reference
      extends: [".managed"]
      prompt: "own prompt"
    - id: relabel-only
      extends: [".managed"]
      langfusePromptLabel: production
    - id: explicit-empty-prompt
      extends: [".inline"]
      prompt: ""
      langfusePromptPath: flows/own/other
`)

	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}

	t.Run("a declared reference replaces an inherited inline prompt", func(t *testing.T) {
		step := stepByID(t, f, "reference-over-inline")
		if step.LangfusePromptPath != "flows/own/main" {
			t.Errorf("LangfusePromptPath = %q, want the step's own", step.LangfusePromptPath)
		}
		if step.Prompt != "" {
			t.Errorf("Prompt = %q, want empty — the template's inline prompt must not survive", step.Prompt)
		}
		// The template's other keys are unaffected: only the competing
		// prompt source is suppressed.
		if step.Agent != "template-agent" || step.MaxTurns != 15 {
			t.Errorf("unrelated keys not inherited: agent=%q maxTurns=%d", step.Agent, step.MaxTurns)
		}
	})

	t.Run("a declared inline prompt replaces an inherited reference AND its label", func(t *testing.T) {
		step := stepByID(t, f, "inline-over-reference")
		if step.Prompt != "own prompt" {
			t.Errorf("Prompt = %q, want the step's own", step.Prompt)
		}
		if step.LangfusePromptPath != "" {
			t.Errorf("LangfusePromptPath = %q, want empty", step.LangfusePromptPath)
		}
		if step.LangfusePromptLabel != "" {
			t.Errorf("LangfusePromptLabel = %q, want empty — an orphaned label is the trap this rule removes", step.LangfusePromptLabel)
		}
	})

	t.Run("a label alone re-labels the inherited path", func(t *testing.T) {
		step := stepByID(t, f, "relabel-only")
		if step.LangfusePromptPath != "flows/template/main" {
			t.Errorf("LangfusePromptPath = %q, want the template's — a label-only override must keep it", step.LangfusePromptPath)
		}
		if step.LangfusePromptLabel != "production" {
			t.Errorf("LangfusePromptLabel = %q, want the step's own", step.LangfusePromptLabel)
		}
	})

	// Restores the explicit-zero coverage the include tests used to carry on
	// `prompt`. It is load-bearing here: `prompt: ""` is how an author
	// declares "I have no inline prompt" in a raw-key merge, and it is what
	// makes the source suppression observable rather than incidental.
	t.Run("an explicit empty prompt is a declaration, not an omission", func(t *testing.T) {
		step := stepByID(t, f, "explicit-empty-prompt")
		if step.Prompt != "" {
			t.Errorf("Prompt = %q, want empty — an explicit \"\" must override the template", step.Prompt)
		}
		if step.LangfusePromptPath != "flows/own/other" {
			t.Errorf("LangfusePromptPath = %q, want the step's own", step.LangfusePromptPath)
		}
	})
}

// TestParseFlowFile_TemplatesSupplyingBothSourcesCollide is the other half:
// suppression only applies to a source the STEP declares. Two templates that
// between them supply both sources is a real conflict with no author intent
// to honour, and the error says to look at the templates.
func TestParseFlowFile_TemplatesSupplyingBothSourcesCollide(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "base.yaml"), `.inline:
  prompt: "template prompt"
.managed:
  langfusePromptPath: flows/template/main
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "collide.yaml"), `name: Collide
description: d
include:
  - local: steps/base.yaml
flow:
  steps:
    - id: both
      extends: [".inline", ".managed"]
      agent: coder
`)

	_, err := parseFlowFile(flowPath)
	if err == nil {
		t.Fatal("parseFlowFile() error = nil, want a prompt-source collision")
	}
	if !errors.Is(err, ErrInvalidPromptSource) {
		t.Errorf("error = %v, want one wrapping ErrInvalidPromptSource", err)
	}
	if !strings.Contains(err.Error(), "extends") {
		t.Errorf("error = %v, want one pointing at the extends templates — the step's own YAML shows neither key", err)
	}
}
