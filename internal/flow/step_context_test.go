package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/contextfile"
)

// Tests in this file cover the flow-api delta scenarios of the
// scoped-context-files change: the `context` step field parses, resolves
// as the highest-precedence layer, is inheritable through
// include/extends, and is overridden wholly by an extending step.

func writeStepContextFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseFlowFile_StepContextResolvesReplace covers "Step `replace`
// excludes agent and global context": the parsed step's context resolves
// only its own file.
func TestParseFlowFile_StepContextResolvesReplace(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)
	writeStepContextFile(t, root, "AGENTS.md", "global instructions")
	writeStepContextFile(t, root, "STEP.md", "step instructions")

	flowPath := writeIncludeFile(t, filepath.Join(root, ".agents", "flows", "ctx.yaml"), `name: Ctx
description: d
flow:
  steps:
    - id: scoped
      agent: coder
      prompt: "work"
      context:
        paths: ["STEP.md"]
        mode: replace
    - id: plain
      agent: coder
      prompt: "work"
`)
	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}

	scoped := stepByID(t, f, "scoped")
	if scoped.Context == nil {
		t.Fatal("step context did not parse")
	}
	if scoped.Context.Mode != "replace" || len(scoped.Context.Paths) != 1 || scoped.Context.Paths[0] != "STEP.md" {
		t.Fatalf("parsed context = %+v, want paths [STEP.md] mode replace", scoped.Context)
	}

	vars := contextfile.TemplateVars{Agent: "coder", FlowID: f.ID, FlowStep: scoped.ID}
	resolved := contextfile.ResolveForAgent([]string{"AGENTS.md"}, nil, scoped.Context, root, vars)
	if !strings.Contains(resolved, "step instructions") {
		t.Errorf("resolved context misses the step file: %q", resolved)
	}
	if strings.Contains(resolved, "global instructions") {
		t.Errorf("step replace must exclude the global layer: %q", resolved)
	}

	// A step without `context` keeps the global default.
	plain := stepByID(t, f, "plain")
	if plain.Context != nil {
		t.Fatalf("plain step context = %+v, want nil", plain.Context)
	}
	resolved = contextfile.ResolveForAgent([]string{"AGENTS.md"}, nil, plain.Context, root,
		contextfile.TemplateVars{Agent: "coder", FlowID: f.ID, FlowStep: plain.ID})
	if !strings.Contains(resolved, "global instructions") {
		t.Errorf("a step without context must resolve the global default: %q", resolved)
	}
}

// TestParseFlowFile_StepContextInheritedFromTemplate covers "A template
// supplies `context` and the extending step inherits it".
func TestParseFlowFile_StepContextInheritedFromTemplate(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, ".agents", "steps", "runtime.yaml"), `.runtime:
  agent: coder
  prompt: "work"
  context:
    paths: ["AGENTS.runtime.md"]
    mode: replace
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, ".agents", "flows", "inherit.yaml"), `name: Inherit
description: d
include:
  - local: .agents/steps/runtime.yaml
flow:
  steps:
    - id: inherits
      extends: [".runtime"]
    - id: inherits2
      extends: [".runtime"]
    - id: overrides
      extends: [".runtime"]
      context:
        paths: ["AGENTS.custom.md"]
        mode: append
`)
	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}

	inherits := stepByID(t, f, "inherits")
	if inherits.Context == nil {
		t.Fatal("template context was not inherited")
	}
	if inherits.Context.Mode != "replace" || len(inherits.Context.Paths) != 1 || inherits.Context.Paths[0] != "AGENTS.runtime.md" {
		t.Fatalf("inherited context = %+v, want the template's", inherits.Context)
	}

	// The extending step's own `context` replaces the template's wholly
	// (shallow merge, consistent with output/rules/fallback).
	overrides := stepByID(t, f, "overrides")
	if overrides.Context == nil {
		t.Fatal("overriding step context did not parse")
	}
	if overrides.Context.Mode != "append" || len(overrides.Context.Paths) != 1 || overrides.Context.Paths[0] != "AGENTS.custom.md" {
		t.Fatalf("overriding context = %+v, want paths [AGENTS.custom.md] mode append", overrides.Context)
	}

	// Inherited values must be deep-copied WITHIN one parse: two steps
	// extending the same template must not share a *StepContext or its
	// Paths backing array — otherwise a later mutation of one step's
	// context (template substitution, defaulting) silently rewrites its
	// sibling's. (A re-parse from disk can never detect this: parseFlowFile
	// re-decodes the YAML, so cross-load isolation holds trivially.)
	inherits2 := stepByID(t, f, "inherits2")
	if inherits2.Context == nil {
		t.Fatal("second extending step did not inherit the template context")
	}
	if inherits.Context == inherits2.Context {
		t.Fatal("two steps extending one template share the same *StepContext — mergeTemplates must deep-copy")
	}
	inherits.Context.Paths[0] = "MUTATED.md"
	if got := inherits2.Context.Paths[0]; got != "AGENTS.runtime.md" {
		t.Errorf("Paths backing array is shared between sibling steps: %q", got)
	}
}

// TestRunStep_ThreadsStepContextIntoNewAgent pins the runStep → NewAgent
// plumbing: step.Context rides the signature unchanged, and the
// ${flow.id}/${flow.step} template values are populated explicitly from
// f.ID and step.ID — NOT from the telemetry ctx keys, which are set later
// on the Run context and invisible to the context-free NewAgent.
func TestRunStep_ThreadsStepContextIntoNewAgent(t *testing.T) {
	testFlow := Flow{
		ID:   "test-step-context",
		Name: "Test Step Context",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:      "scoped",
					Prompt:  "work",
					Context: &contextfile.StepContext{Paths: []string{"STEP.md"}, Mode: "replace"},
					Rules:   []Rule{{Then: "plain"}},
				},
				{ID: "plain", Prompt: "more work"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	factory := &stubAgentFactory{agent: newStubAgent()}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, factory)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	drainFlow(t, agentEvents, flowStates)

	calls := factory.snapshotNewAgentCalls()
	byStep := make(map[string]stubNewAgentCall, len(calls))
	for _, c := range calls {
		byStep[c.stepID] = c
	}

	scoped, ok := byStep["scoped"]
	if !ok {
		t.Fatalf("NewAgent was not called for step 'scoped'; calls: %+v", calls)
	}
	if scoped.stepCtx == nil || len(scoped.stepCtx.Paths) != 1 || scoped.stepCtx.Paths[0] != "STEP.md" || scoped.stepCtx.Mode != "replace" {
		t.Errorf("step context did not reach NewAgent: %+v", scoped.stepCtx)
	}
	want := contextfile.TemplateVars{FlowID: testFlow.ID, FlowStep: "scoped"}
	if scoped.vars != want {
		t.Errorf("template vars = %+v, want %+v", scoped.vars, want)
	}

	plain, ok := byStep["plain"]
	if !ok {
		t.Fatalf("NewAgent was not called for step 'plain'; calls: %+v", calls)
	}
	if plain.stepCtx != nil {
		t.Errorf("a step without context must pass nil, got %+v", plain.stepCtx)
	}
	if plain.vars.FlowStep != "plain" || plain.vars.FlowID != testFlow.ID {
		t.Errorf("plain step template vars = %+v", plain.vars)
	}
}
