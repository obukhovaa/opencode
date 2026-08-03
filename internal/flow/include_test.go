package flow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

// Tests in this file map 1:1 onto the scenarios of the flow-api
// requirement "Flow files compose shared step definitions via include and
// extends" (openspec/changes/flow-step-includes/specs/flow-api/spec.md).
// The scenario each test covers is named in its doc comment.

// writeIncludeFile writes a file, creating parent directories.
func writeIncludeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// setIncludeWorkspace points config.WorkingDir at root, which is the base
// `include: local:` paths resolve against (design D5).
func setIncludeWorkspace(t *testing.T, root string) {
	t.Helper()
	config.Reset()
	if _, err := config.Load(root, false); err != nil {
		t.Logf("config.Load warning: %v", err)
	}
	t.Cleanup(config.Reset)
	cfg := config.Get()
	if cfg == nil {
		t.Fatal("config.Get() returned nil after Load")
	}
	cfg.WorkingDir = root
}

func stepByID(t *testing.T, f *Flow, id string) Step {
	t.Helper()
	for _, s := range f.Spec.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found in flow %q", id, f.ID)
	return Step{}
}

// TestParseFlowFile_StepInheritsTemplate covers scenario "A step inherits
// a template": agent + prompt come from the template and the merged step
// is indistinguishable from the inline equivalent.
func TestParseFlowFile_StepInheritsTemplate(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, ".agents", "steps", "resolve-team.yaml"), `.resolve-team:
  agent: piano-manager
  maxTurns: 15
  timeout: 10m
  session:
    fork: true
  prompt: |
    Resolve the owning team.
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, ".agents", "flows", "shared.yaml"), `name: Shared
description: d
include:
  - local: .agents/steps/resolve-team.yaml
flow:
  steps:
    - id: resolve-team
      extends: [".resolve-team"]
    - id: work
      agent: piano-coder
      prompt: "work"
`)

	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}
	step := stepByID(t, f, "resolve-team")
	if step.Agent != "piano-manager" {
		t.Errorf("Agent = %q, want piano-manager", step.Agent)
	}
	if !strings.Contains(step.Prompt, "Resolve the owning team.") {
		t.Errorf("Prompt = %q, want the template's prompt", step.Prompt)
	}
	if step.MaxTurns != 15 {
		t.Errorf("MaxTurns = %d, want 15", step.MaxTurns)
	}
	if step.Timeout != "10m" {
		t.Errorf("Timeout = %q, want 10m", step.Timeout)
	}
	if !step.Session.Fork {
		t.Errorf("Session.Fork = false, want true (inherited)")
	}
	// A merged step is indistinguishable from an inline one: identity and
	// scheduling keys stay owned by the flow.
	if step.Interactive {
		t.Errorf("Interactive = true, want false — a template may not set it")
	}
	// Untouched sibling step is unaffected.
	if other := stepByID(t, f, "work"); other.Agent != "piano-coder" {
		t.Errorf("sibling step Agent = %q, want piano-coder", other.Agent)
	}
}

// TestParseFlowFile_StepOverridesInheritedKeys covers scenario "The step
// overrides an inherited key", including the zero-value trap from task
// 1.7: an explicit `maxTurns: 0` and an omitted `maxTurns` must be
// distinguishable, and `session: {fork: false}` must override a
// template's `fork: true`.
func TestParseFlowFile_StepOverridesInheritedKeys(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "base.yaml"), `.base:
  agent: piano-manager
  prompt: "template prompt"
  maxTurns: 15
  maxIterations: 3
  timeout: 10m
  session:
    fork: true
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "override.yaml"), `name: Override
description: d
include:
  - local: steps/base.yaml
flow:
  steps:
    - id: plain-override
      extends: [".base"]
      maxTurns: 20
    - id: explicit-zero
      extends: [".base"]
      maxTurns: 0
      maxIterations: 0
      timeout: ""
      agent: ""
      prompt: ""
      session:
        fork: false
    - id: inherit-all
      extends: [".base"]
`)

	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}

	t.Run("scalar override wins and unset keys keep the template value", func(t *testing.T) {
		step := stepByID(t, f, "plain-override")
		if step.MaxTurns != 20 {
			t.Errorf("MaxTurns = %d, want 20", step.MaxTurns)
		}
		if step.Agent != "piano-manager" || step.Prompt != "template prompt" {
			t.Errorf("unset keys not inherited: agent=%q prompt=%q", step.Agent, step.Prompt)
		}
		if step.MaxIterations != 3 || step.Timeout != "10m" {
			t.Errorf("unset keys not inherited: maxIterations=%d timeout=%q", step.MaxIterations, step.Timeout)
		}
	})

	t.Run("explicit zero overrides the template and is not read as unset", func(t *testing.T) {
		zero := stepByID(t, f, "explicit-zero")
		inherit := stepByID(t, f, "inherit-all")
		if inherit.MaxTurns != 15 {
			t.Fatalf("control step MaxTurns = %d, want 15 (omitted key must inherit)", inherit.MaxTurns)
		}
		if zero.MaxTurns != 0 {
			t.Errorf("explicit maxTurns: 0 → MaxTurns = %d, want 0 (must be distinguishable from omitted)", zero.MaxTurns)
		}
		if zero.MaxIterations != 0 {
			t.Errorf("explicit maxIterations: 0 → MaxIterations = %d, want 0", zero.MaxIterations)
		}
		if zero.Timeout != "" {
			t.Errorf("explicit timeout: \"\" → Timeout = %q, want empty", zero.Timeout)
		}
		if zero.Agent != "" {
			t.Errorf("explicit agent: \"\" → Agent = %q, want empty", zero.Agent)
		}
		if zero.Prompt != "" {
			t.Errorf("explicit prompt: \"\" → Prompt = %q, want empty", zero.Prompt)
		}
	})

	t.Run("session fork false overrides a template fork true", func(t *testing.T) {
		zero := stepByID(t, f, "explicit-zero")
		if zero.Session.Fork {
			t.Errorf("Session.Fork = true, want false — `session: {fork: false}` must override the template")
		}
		inherit := stepByID(t, f, "inherit-all")
		if !inherit.Session.Fork {
			t.Errorf("control step Session.Fork = false, want true (omitted session must inherit)")
		}
	})
}

// TestParseFlowFile_OverridingBlockReplacesWholly covers scenario
// "Overriding a block replaces it wholly": the merge is shallow per
// top-level key, so no field of the template's `output` / `rules` /
// `fallback` survives the step's own block.
func TestParseFlowFile_OverridingBlockReplacesWholly(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "blocks.yaml"), `.blocks:
  agent: piano-manager
  prompt: "p"
  output:
    schema:
      type: object
      properties:
        template_only:
          type: string
      required: ["template_only"]
  rules:
    - if: "${output.template_only} == x"
      then: other
  fallback:
    retry: 3
    to: other
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "blocks.yaml"), `name: Blocks
description: d
include:
  - local: steps/blocks.yaml
flow:
  steps:
    - id: main
      extends: [".blocks"]
      output:
        schema:
          type: object
          properties:
            step_only:
              type: string
      rules:
        - if: "${output.step_only} == y"
          then: other
      fallback:
        retry: 1
    - id: other
      prompt: "o"
`)

	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}
	step := stepByID(t, f, "main")

	props, ok := step.Output.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("output.schema.properties has unexpected type %T", step.Output.Schema["properties"])
	}
	if _, present := props["template_only"]; present {
		t.Errorf("template's output property survived the override: %v", props)
	}
	if _, present := props["step_only"]; !present {
		t.Errorf("step's output property missing: %v", props)
	}
	if _, present := step.Output.Schema["required"]; present {
		t.Errorf("template's output.required survived the override: %v", step.Output.Schema)
	}

	if len(step.Rules) != 1 {
		t.Fatalf("Rules count = %d, want 1 (the step's block replaces the template's wholly)", len(step.Rules))
	}
	if strings.Contains(step.Rules[0].If, "template_only") {
		t.Errorf("template's rule survived the override: %+v", step.Rules)
	}

	if step.Fallback.Retry != 1 {
		t.Errorf("Fallback.Retry = %d, want 1", step.Fallback.Retry)
	}
	if step.Fallback.To != "" {
		t.Errorf("template's fallback.to = %q survived the override, want empty", step.Fallback.To)
	}
}

// TestParseFlowFile_SeveralTemplatesApplyInOrder covers scenario "Several
// templates apply in order": later `extends` entries override earlier
// ones, and a key set only by the first survives.
func TestParseFlowFile_SeveralTemplatesApplyInOrder(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "base.yaml"), `.base:
  agent: base-agent
  prompt: "base prompt"
  maxTurns: 15
`)
	writeIncludeFile(t, filepath.Join(root, "steps", "override.yaml"), `.override:
  agent: override-agent
  maxTurns: 0
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "ordered.yaml"), `name: Ordered
description: d
include:
  - local: steps/base.yaml
  - local: steps/override.yaml
flow:
  steps:
    - id: main
      extends: [".base", ".override"]
`)

	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}
	step := stepByID(t, f, "main")
	if step.Agent != "override-agent" {
		t.Errorf("Agent = %q, want override-agent (last extends wins)", step.Agent)
	}
	if step.Prompt != "base prompt" {
		t.Errorf("Prompt = %q, want the first template's value (set only by .base)", step.Prompt)
	}
	if step.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0 — a later template's explicit zero must override an earlier 15", step.MaxTurns)
	}
}

// TestParseFlowFile_TemplateRejectsNonInheritableKeys covers scenario "A
// template declaring an orchestrator-read key is rejected". Table-driven
// per task 2.5, so a future field argued into the inheritable set has an
// obvious place to be argued for.
func TestParseFlowFile_TemplateRejectsNonInheritableKeys(t *testing.T) {
	cases := []struct {
		key      string
		template string
	}{
		{"id", ".t:\n  id: hard-coded\n  prompt: p\n"},
		{"interactive", ".t:\n  interactive: true\n  prompt: p\n"},
		{"interaction", ".t:\n  interaction:\n    target: \"${args.reviewer}\"\n  prompt: p\n"},
		{"resume_after", ".t:\n  resume_after: 1h\n  prompt: p\n"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			root := t.TempDir()
			setIncludeWorkspace(t, root)
			writeIncludeFile(t, filepath.Join(root, "steps", "t.yaml"), tc.template)
			flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "uses.yaml"), `name: Uses
description: d
include:
  - local: steps/t.yaml
flow:
  steps:
    - id: main
      extends: [".t"]
`)
			_, err := parseFlowFile(flowPath)
			if err == nil {
				t.Fatalf("expected a load error for template key %q", tc.key)
			}
			if !errors.Is(err, ErrInvalidTemplate) {
				t.Errorf("error = %v, want ErrInvalidTemplate", err)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %v does not name the offending key %q", err, tc.key)
			}
			if !strings.Contains(err.Error(), ".t") {
				t.Errorf("error %v does not name the template", err)
			}
		})
	}
}

// TestParseFlowFile_MergedStepValidatedLikeInline covers scenario "A
// merged step is validated like an inline one" — the assertion that
// merging precedes validation. A template's rule naming a step the
// extending flow lacks must fail with the pre-existing ErrInvalidRule.
func TestParseFlowFile_MergedStepValidatedLikeInline(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "routes.yaml"), `.routes:
  agent: piano-manager
  prompt: "p"
  rules:
    - if: "${output.status} == ok"
      then: nonexistent-step
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "routes.yaml"), `name: Routes
description: d
include:
  - local: steps/routes.yaml
flow:
  steps:
    - id: main
      extends: [".routes"]
`)

	_, err := parseFlowFile(flowPath)
	if err == nil {
		t.Fatal("expected validation to reject the merged step's unknown rule target")
	}
	if !errors.Is(err, ErrInvalidRule) {
		t.Errorf("error = %v, want the existing ErrInvalidRule (proves merge precedes validateFlow)", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-step") {
		t.Errorf("error %v does not name the unknown rule target", err)
	}

	// Negative control: a template rule naming a step the flow DOES
	// define loads cleanly.
	okFlow := writeIncludeFile(t, filepath.Join(root, "flows", "routes-ok.yaml"), `name: Routes OK
description: d
include:
  - local: steps/routes.yaml
flow:
  steps:
    - id: main
      extends: [".routes"]
    - id: nonexistent-step
      prompt: "now it exists"
`)
	if _, err := parseFlowFile(okFlow); err != nil {
		t.Errorf("parseFlowFile() with the rule target defined: %v", err)
	}
}

// TestParseFlowFile_IncludeAndExtendsErrors covers scenarios "An unknown
// template name is an error", "A template may not nest composition" and
// the unsupported-include-kind requirement (task 2.7). Each failure mode
// must produce its own message.
func TestParseFlowFile_IncludeAndExtendsErrors(t *testing.T) {
	cases := []struct {
		name      string
		template  string
		flow      string
		wantErr   error
		wantParts []string
	}{
		{
			name:     "unknown template name",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - local: steps/t.yaml
flow:
  steps:
    - id: main
      extends: [".missing"]
`,
			wantErr:   ErrUnknownTemplate,
			wantParts: []string{".missing", "main", ".present"},
		},
		{
			name:     "extends with no include at all",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrUnknownTemplate,
			wantParts: []string{".present", "main"},
		},
		{
			name:     "remote include kind",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - remote: https://example.com/steps.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidYAML,
			wantParts: []string{"remote", "local"},
		},
		{
			name:     "project include kind",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - project: piano/other
    file: steps.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidYAML,
			wantParts: []string{"local"},
		},
		{
			name:     "template file declares include",
			template: "include:\n  - local: steps/other.yaml\n.present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - local: steps/t.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidInclude,
			wantParts: []string{"include"},
		},
		{
			name:     "template declares extends",
			template: ".present:\n  prompt: p\n  extends: [\".other\"]\n",
			flow: `name: X
description: d
include:
  - local: steps/t.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidTemplate,
			wantParts: []string{"extends", ".present"},
		},
		{
			name:     "template declares an unknown step key",
			template: ".present:\n  promt: typo\n",
			flow: `name: X
description: d
include:
  - local: steps/t.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidTemplate,
			wantParts: []string{"promt", ".present"},
		},
		{
			name:     "missing included file",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - local: steps/absent.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidInclude,
			wantParts: []string{"steps/absent.yaml"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			setIncludeWorkspace(t, root)
			writeIncludeFile(t, filepath.Join(root, "steps", "t.yaml"), tc.template)
			flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "x.yaml"), tc.flow)

			_, err := parseFlowFile(flowPath)
			if err == nil {
				t.Fatalf("expected a load error")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %v does not mention %q", err, part)
				}
			}
		})
	}
}

// TestParseFlowFile_IncludePathResolution covers task 2.8: `local:` paths
// are ROOT-relative (design D5), so the SAME include line works from a
// shared flow in `.agents/flows/` and from a team flow in
// `<team>/flows/`; absolute paths are honoured; and a template's
// output-schema `$ref` resolves against the TEMPLATE file's directory,
// not the flow's.
func TestParseFlowFile_IncludePathResolution(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, ".agents", "steps", "schema.json"), `{
  "type": "object",
  "properties": {"team": {"type": "string"}},
  "required": ["team"]
}`)
	writeIncludeFile(t, filepath.Join(root, ".agents", "steps", "resolve-team.yaml"), `.resolve-team:
  agent: piano-manager
  prompt: "resolve"
  output:
    schema:
      $ref: schema.json
`)

	body := `name: Uses
description: d
include:
  - local: .agents/steps/resolve-team.yaml
flow:
  steps:
    - id: resolve-team
      extends: [".resolve-team"]
`
	sharedFlow := writeIncludeFile(t, filepath.Join(root, ".agents", "flows", "shared.yaml"), body)
	// Same include line, different flow-file depth: this is why
	// root-relative was chosen over file-relative.
	teamFlow := writeIncludeFile(t, filepath.Join(root, "composer", "flows", "team.yaml"), body)

	for name, path := range map[string]string{"shared flow": sharedFlow, "team flow": teamFlow} {
		t.Run(name+" resolves the same root-relative include", func(t *testing.T) {
			f, err := parseFlowFile(path)
			if err != nil {
				t.Fatalf("parseFlowFile() error: %v", err)
			}
			step := stepByID(t, f, "resolve-team")
			if step.Agent != "piano-manager" {
				t.Errorf("Agent = %q, want piano-manager", step.Agent)
			}
			// The $ref was resolved against the TEMPLATE's dir; the
			// flow's own dir holds no schema.json at all.
			props, ok := step.Output.Schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("output schema not resolved from the template's $ref: %#v", step.Output.Schema)
			}
			if _, present := props["team"]; !present {
				t.Errorf("resolved schema lacks the template file's property: %v", props)
			}
			if _, stillRef := step.Output.Schema["$ref"]; stillRef {
				t.Errorf("output schema still carries an unresolved $ref: %v", step.Output.Schema)
			}
		})
	}

	t.Run("absolute include path is honoured", func(t *testing.T) {
		outside := t.TempDir()
		writeIncludeFile(t, filepath.Join(outside, "abs.yaml"), ".abs:\n  agent: abs-agent\n  prompt: p\n")
		flowPath := writeIncludeFile(t, filepath.Join(root, ".agents", "flows", "abs.yaml"), `name: Abs
description: d
include:
  - local: `+filepath.Join(outside, "abs.yaml")+`
flow:
  steps:
    - id: main
      extends: [".abs"]
`)
		f, err := parseFlowFile(flowPath)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if got := stepByID(t, f, "main").Agent; got != "abs-agent" {
			t.Errorf("Agent = %q, want abs-agent", got)
		}
	})

	t.Run("relative include with no config loaded does not panic", func(t *testing.T) {
		// config.WorkingDirectory() panics when config is not loaded;
		// resolveIncludePath must degrade to the process working
		// directory instead, so parseFlowFile stays callable without a
		// loaded config (as existing tests do).
		config.Reset()
		if got := resolveIncludePath("steps/t.yaml"); got != filepath.Clean("steps/t.yaml") {
			t.Errorf("resolveIncludePath with no config = %q, want %q", got, "steps/t.yaml")
		}
	})
}

// TestParseFlowFile_IncludedFileSizeCap covers task 2.9: the per-file
// ceiling (OPENCODE_MAX_FLOW_FILE_SIZE) applies to EACH included file, so
// `include` is not a way around it.
func TestParseFlowFile_IncludedFileSizeCap(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	oversized := ".big:\n  agent: piano-manager\n  prompt: |\n    " + strings.Repeat("x", 320*1024) + "\n"
	writeIncludeFile(t, filepath.Join(root, "steps", "big.yaml"), oversized)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "big.yaml"), `name: Big
description: d
include:
  - local: steps/big.yaml
flow:
  steps:
    - id: main
      extends: [".big"]
`)

	_, err := parseFlowFile(flowPath)
	if err == nil {
		t.Fatal("expected an oversized included file to be rejected")
	}
	if !errors.Is(err, ErrInvalidInclude) {
		t.Errorf("error = %v, want ErrInvalidInclude", err)
	}
	if !strings.Contains(err.Error(), "OPENCODE_MAX_FLOW_FILE_SIZE") {
		t.Errorf("error %v does not point at the size-cap env var", err)
	}
}

// TestParseFlowFile_WithoutIncludeUnchanged covers scenario "Flows
// without include are unaffected" (task 2.10): a flow declaring neither
// key parses into exactly the value it did before this change — nothing
// is seeded, no key becomes required.
func TestParseFlowFile_WithoutIncludeUnchanged(t *testing.T) {
	dir := t.TempDir()
	// No config loaded on purpose: parseFlowFile must not need one.
	config.Reset()

	path := writeIncludeFile(t, filepath.Join(dir, "legacy-flow.yaml"), `name: Legacy Flow
description: A flow written before include/extends existed
flow:
  session:
    prefix: "${args.id}"
  steps:
    - id: step-one
      agent: piano-coder
      prompt: "Do ${args.prompt}"
      rules:
        - if: "${output.status} == done"
          then: step-two
    - id: step-two
      prompt: "Continue"
`)

	f, err := parseFlowFile(path)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}

	want := Flow{
		ID:          "legacy-flow",
		Name:        "Legacy Flow",
		Description: "A flow written before include/extends existed",
		Location:    path,
		Spec: FlowSpec{
			Session: FlowSession{Prefix: "${args.id}"},
			Steps: []Step{
				{
					ID:     "step-one",
					Agent:  "piano-coder",
					Prompt: "Do ${args.prompt}",
					Rules:  []Rule{{If: "${output.status} == done", Then: "step-two"}},
				},
				{
					ID:     "step-two",
					Prompt: "Continue",
				},
			},
		},
	}
	if !reflect.DeepEqual(*f, want) {
		t.Errorf("parsed flow differs from the pre-change value:\n got: %#v\nwant: %#v", *f, want)
	}
}
