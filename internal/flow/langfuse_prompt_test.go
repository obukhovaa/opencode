package flow

import (
	"context"
	"errors"
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
