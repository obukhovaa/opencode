package flow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

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
  agent: template-agent
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
      agent: step-agent
      prompt: "work"
`)

	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}
	step := stepByID(t, f, "resolve-team")
	if step.Agent != "template-agent" {
		t.Errorf("Agent = %q, want template-agent", step.Agent)
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
	if other := stepByID(t, f, "work"); other.Agent != "step-agent" {
		t.Errorf("sibling step Agent = %q, want step-agent", other.Agent)
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
  agent: template-agent
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
		if step.Agent != "template-agent" || step.Prompt != "template prompt" {
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
  agent: template-agent
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
// per task 2.5, and cross-checked against nonInheritableStepKeys so
// arguing a key into or out of that set forces a case here. This is the
// SECOND half of the two-part rule — the four keys the orchestrator reads
// are rejected by name; everything else Step models is inheritable (see
// TestTemplateKeyRule_TwoPart).
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
	if len(cases) != len(nonInheritableStepKeys) {
		t.Errorf("table has %d cases but nonInheritableStepKeys has %d entries — keep them in step",
			len(cases), len(nonInheritableStepKeys))
	}
	for _, tc := range cases {
		if _, forbidden := nonInheritableStepKeys[tc.key]; !forbidden {
			t.Errorf("case %q is not in nonInheritableStepKeys", tc.key)
		}
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
			// The message must carry WHY, not just the key: the
			// orchestrator reason is the whole contract here.
			if !strings.Contains(err.Error(), nonInheritableStepKeys[tc.key]) {
				t.Errorf("error %v does not quote the reason for rejecting %q", err, tc.key)
			}
		})
	}
}

// TestTemplateKeyRule_TwoPart pins the two-part template-key rule: a key
// must be a KNOWN STEP FIELD, and must not be one of the four the
// orchestrator reads. There is deliberately no curated allow-list of
// inheritable fields, so a field added to Step later is inheritable BY
// DEFAULT — the first subtest is driven off Step's own yaml tags and
// fails when a new field appears without a sample, which is the only
// maintenance this rule asks for.
func TestTemplateKeyRule_TwoPart(t *testing.T) {
	t.Run("every step field except the rejected four is inheritable", func(t *testing.T) {
		// One sample value per inheritable step field, plus the
		// assertion that it survived the merge.
		samples := map[string]struct {
			yaml  string
			check func(t *testing.T, s Step)
		}{
			"agent": {"  agent: tmpl-agent\n", func(t *testing.T, s Step) {
				if s.Agent != "tmpl-agent" {
					t.Errorf("Agent = %q", s.Agent)
				}
			}},
			"prompt": {"  prompt: tmpl prompt\n", func(t *testing.T, s Step) {
				if s.Prompt != "tmpl prompt" {
					t.Errorf("Prompt = %q", s.Prompt)
				}
			}},
			"session": {"  session:\n    fork: true\n", func(t *testing.T, s Step) {
				if !s.Session.Fork {
					t.Errorf("Session.Fork = false, want true")
				}
			}},
			"output": {"  output:\n    schema:\n      type: object\n", func(t *testing.T, s Step) {
				if s.Output == nil || s.Output.Schema["type"] != "object" {
					t.Errorf("Output = %#v", s.Output)
				}
			}},
			"rules": {"  rules:\n    - then: other\n", func(t *testing.T, s Step) {
				if len(s.Rules) != 1 || s.Rules[0].Then != "other" {
					t.Errorf("Rules = %+v", s.Rules)
				}
			}},
			"fallback": {"  fallback:\n    retry: 2\n", func(t *testing.T, s Step) {
				if s.Fallback == nil || s.Fallback.Retry != 2 {
					t.Errorf("Fallback = %#v", s.Fallback)
				}
			}},
			"maxTurns": {"  maxTurns: 7\n", func(t *testing.T, s Step) {
				if s.MaxTurns != 7 {
					t.Errorf("MaxTurns = %d", s.MaxTurns)
				}
			}},
			"maxIterations": {"  maxIterations: 4\n", func(t *testing.T, s Step) {
				if s.MaxIterations != 4 {
					t.Errorf("MaxIterations = %d", s.MaxIterations)
				}
			}},
			"timeout": {"  timeout: 3m\n", func(t *testing.T, s Step) {
				if s.Timeout != "3m" {
					t.Errorf("Timeout = %q", s.Timeout)
				}
			}},
			"compact": {"  compact:\n    threshold: 0.7\n", func(t *testing.T, s Step) {
				if s.Compact == nil || s.Compact.Threshold != 0.7 {
					t.Errorf("Compact = %#v", s.Compact)
				}
			}},
		}

		// Coverage guard: the rule admits every step field, so a field
		// added to Step must show up here. This failing is the signal to
		// add a sample — NOT to curate an allow-list in include.go.
		for _, key := range inheritableStepKeys() {
			if _, ok := samples[key]; !ok {
				t.Errorf("step field %q is inheritable but has no sample here — add one (the rule itself needs no code change)", key)
			}
		}
		for key := range samples {
			if _, forbidden := nonInheritableStepKeys[key]; forbidden {
				t.Errorf("sample %q is in the rejected set and must not be inheritable", key)
			}
		}

		for _, key := range sortedKeys(samples) {
			sample := samples[key]
			t.Run(key, func(t *testing.T) {
				root := t.TempDir()
				setIncludeWorkspace(t, root)
				writeIncludeFile(t, filepath.Join(root, "steps", "t.yaml"), ".t:\n"+sample.yaml)
				flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "x.yaml"), `name: X
description: d
include:
  - local: steps/t.yaml
flow:
  steps:
    - id: main
      extends: [".t"]
    - id: other
      prompt: o
`)
				f, err := parseFlowFile(flowPath)
				if err != nil {
					t.Fatalf("a template declaring step field %q must load: %v", key, err)
				}
				sample.check(t, stepByID(t, f, "main"))
			})
		}
	})

	t.Run("a key that is not a step field is rejected", func(t *testing.T) {
		// The first half of the rule: typo protection survives the move
		// away from an allow-list, and the message lists the real fields.
		for _, key := range []string{"promt", "resume_aftr", "maxturns", "invented"} {
			t.Run(key, func(t *testing.T) {
				root := t.TempDir()
				setIncludeWorkspace(t, root)
				writeIncludeFile(t, filepath.Join(root, "steps", "t.yaml"), ".t:\n  "+key+": x\n")
				flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "x.yaml"), `name: X
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
					t.Fatalf("expected %q to be rejected as not a step field", key)
				}
				if !errors.Is(err, ErrInvalidTemplate) {
					t.Errorf("error = %v, want ErrInvalidTemplate", err)
				}
				if !strings.Contains(err.Error(), key) {
					t.Errorf("error %v does not name the offending key", err)
				}
				if !strings.Contains(err.Error(), "prompt") {
					t.Errorf("error %v does not list the real step fields", err)
				}
			})
		}
	})
}

// TestParseFlowFile_MergedStepValidatedLikeInline covers scenario "A
// merged step is validated like an inline one" — the assertion that
// merging precedes validation. A template's rule naming a step the
// extending flow lacks must fail with the pre-existing ErrInvalidRule.
func TestParseFlowFile_MergedStepValidatedLikeInline(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	writeIncludeFile(t, filepath.Join(root, "steps", "routes.yaml"), `.routes:
  agent: template-agent
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

// TestParseFlowFile_RawStepAlignmentIsVerified pins the fail-closed guard
// on the two parses' element count. yaml.v3 DROPS a null / comment-only
// sequence entry from the typed `[]Step` decode but KEEPS it as an empty
// map in the raw decode, so without the guard the real step is paired with
// the empty key set, read as declaring nothing, and has its own `agent`
// overwritten by the template's — a guard step silently running an agent
// its flow does not name. validateFlow cannot catch it: the dropped entry
// is absent from the typed tree.
//
// If the guard in stepsRawKeysAligned is removed, this test fails.
func TestParseFlowFile_RawStepAlignmentIsVerified(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)
	writeIncludeFile(t, filepath.Join(root, "steps", "base.yaml"), ".base:\n  agent: template-agent\n  prompt: p\n")

	cases := []struct {
		name string
		flow string
	}{
		{"comment-only entry", `name: X
description: d
include:
  - local: steps/base.yaml
flow:
  steps:
    - # placeholder
    - id: main
      agent: mine
      extends: [".base"]
`},
		{"explicit null entry", `name: X
description: d
include:
  - local: steps/base.yaml
flow:
  steps:
    - null
    - id: main
      agent: mine
      extends: [".base"]
`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "x.yaml"), tc.flow)
			f, err := parseFlowFile(flowPath)
			if err == nil {
				// Fail closed is the specified contract, but if a future
				// change makes the parses agree instead, the step's own
				// agent MUST still win — never the template's.
				if got := stepByID(t, f, "main").Agent; got != "mine" {
					t.Fatalf("flow loaded with Agent = %q; the step's own `agent: mine` must never be overwritten by the template", got)
				}
				t.Fatal("expected the flow to fail loading: the typed and raw step counts disagree, so per-step declared keys cannot be recovered")
			}
			if !errors.Is(err, ErrInvalidInclude) {
				t.Errorf("error = %v, want ErrInvalidInclude", err)
			}
			for _, part := range []string{"raw", "steps", "2", "1"} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %v does not mention %q (the counts and what to fix)", err, part)
				}
			}
		})
	}

	t.Run("stepRawKeys reports a parse failure instead of returning nil", func(t *testing.T) {
		// nil would be indistinguishable from "no step declared any key",
		// which would let templates overwrite every step at once.
		if _, err := stepRawKeys([]byte("flow:\n  steps: not-a-sequence\n")); err == nil {
			t.Error("stepRawKeys() on an unparseable flow.steps returned nil error")
		}
		if _, err := stepRawKeys([]byte("flow:\n  steps:\n    - id: main\n")); err != nil {
			t.Errorf("stepRawKeys() on a valid flow: %v", err)
		}
	})

	t.Run("shapes that do NOT diverge still load", func(t *testing.T) {
		// Probed alongside the null-entry case: anchors + aliases, a merge
		// key (whose merged-in keys appear in BOTH decodes, so an
		// anchor-supplied `agent` counts as declared by the step), and a
		// multi-document file (both decodes take the first document).
		writeIncludeFile(t, filepath.Join(root, "steps", "base.yaml"), ".base:\n  agent: template-agent\n  prompt: p\n")
		anchors := writeIncludeFile(t, filepath.Join(root, "flows", "anchors.yaml"), `name: X
description: d
include:
  - local: steps/base.yaml
defaults:
  common: &common
    agent: anchor-agent
flow:
  steps:
    - id: main
      extends: [".base"]
      <<: *common
    - id: other
      prompt: o
`)
		f, err := parseFlowFile(anchors)
		if err != nil {
			t.Fatalf("anchors / merge key must still load: %v", err)
		}
		if got := stepByID(t, f, "main").Agent; got != "anchor-agent" {
			t.Errorf("Agent = %q, want anchor-agent — a merge-key-supplied key counts as declared by the step", got)
		}
	})
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
			// A GitLab-style project include carries `file:` alongside
			// `project:`. EVERY unsupported key must be reported — naming
			// only the first one encountered points the author at `file:`
			// and hides the actual mistake.
			name:     "project include kind",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - project: group/other
    file: steps.yaml
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidYAML,
			wantParts: []string{"project", "file", "local"},
		},
		{
			// An empty `local:` cannot resolve to anything; rejecting it
			// at the include line beats a "file not found" naming the
			// workspace root.
			name:     "empty local path",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - local: ""
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidYAML,
			wantParts: []string{"empty", "local"},
		},
		{
			// A null template contributes nothing, and validateFlow does
			// not require a prompt — so without this the extending step
			// would load and run empty.
			name:     "null template",
			template: ".present:\n",
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
			wantParts: []string{".present", "no keys"},
		},
		{
			// `local:` present but joined by an unsupported sibling: the
			// entry is still rejected, and the message names the sibling
			// rather than silently honouring the local path.
			name:     "local with an unsupported sibling key",
			template: ".present:\n  prompt: p\n",
			flow: `name: X
description: d
include:
  - local: steps/t.yaml
    ref: main
flow:
  steps:
    - id: main
      extends: [".present"]
`,
			wantErr:   ErrInvalidYAML,
			wantParts: []string{"ref", "local"},
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
  agent: template-agent
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
			if step.Agent != "template-agent" {
				t.Errorf("Agent = %q, want template-agent", step.Agent)
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

	oversized := ".big:\n  agent: template-agent\n  prompt: |\n    " + strings.Repeat("x", 320*1024) + "\n"
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

// resetMaxFlowFileSize re-arms the sync.Once behind maxFlowFileSize so a
// test can exercise OPENCODE_MAX_FLOW_FILE_SIZE, and restores the default
// afterwards. Without this the cap is parsed once per test binary and the
// env var is untestable — which is why nothing held it before.
func resetMaxFlowFileSize(t *testing.T) {
	t.Helper()
	maxFlowFileSizeOnce = sync.Once{}
	maxFlowFileSizeVal = 0
	t.Cleanup(func() {
		maxFlowFileSizeOnce = sync.Once{}
		maxFlowFileSizeVal = 0
	})
}

// TestIncludedFileSizeCapHonoursEnvVar proves an included file is checked
// against the ENV-CONFIGURED cap, not a literal: the same file is accepted
// under a large cap and rejected under a small one. Replacing
// maxFlowFileSize() with a constant fails this test.
func TestIncludedFileSizeCapHonoursEnvVar(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)

	// ~2 KB of template: over a 1k cap, well under a 400k one.
	writeIncludeFile(t, filepath.Join(root, "steps", "mid.yaml"),
		".mid:\n  agent: template-agent\n  prompt: |\n    "+strings.Repeat("x", 2*1024)+"\n")
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "mid.yaml"), `name: Mid
description: d
include:
  - local: steps/mid.yaml
flow:
  steps:
    - id: main
      extends: [".mid"]
`)

	t.Run("raised cap admits the file", func(t *testing.T) {
		resetMaxFlowFileSize(t)
		t.Setenv("OPENCODE_MAX_FLOW_FILE_SIZE", "400k")
		if got := maxFlowFileSize(); got != 400*1024 {
			t.Fatalf("maxFlowFileSize() = %d, want %d — env var not honoured", got, 400*1024)
		}
		if _, err := parseFlowFile(flowPath); err != nil {
			t.Errorf("parseFlowFile() under a 400k cap: %v", err)
		}
	})

	t.Run("lowered cap rejects the same included file", func(t *testing.T) {
		resetMaxFlowFileSize(t)
		t.Setenv("OPENCODE_MAX_FLOW_FILE_SIZE", "1k")
		if got := maxFlowFileSize(); got != 1024 {
			t.Fatalf("maxFlowFileSize() = %d, want 1024 — env var not honoured", got)
		}
		_, err := parseFlowFile(flowPath)
		if err == nil {
			t.Fatal("expected the included file to exceed a 1k cap")
		}
		if !errors.Is(err, ErrInvalidInclude) {
			t.Errorf("error = %v, want ErrInvalidInclude", err)
		}
		if !strings.Contains(err.Error(), "1024") {
			t.Errorf("error %v does not quote the configured limit", err)
		}
	})
}

// TestParseFlowFile_DuplicateTemplateLastIncludeWins pins the include
// precedence: two included files defining the same template name — the
// LAST include wins. Making the first win survives every other test here.
func TestParseFlowFile_DuplicateTemplateLastIncludeWins(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)
	writeIncludeFile(t, filepath.Join(root, "steps", "first.yaml"), ".dup:\n  agent: first-agent\n  prompt: first\n")
	writeIncludeFile(t, filepath.Join(root, "steps", "second.yaml"), ".dup:\n  agent: second-agent\n")

	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "dup.yaml"), `name: Dup
description: d
include:
  - local: steps/first.yaml
  - local: steps/second.yaml
flow:
  steps:
    - id: main
      extends: [".dup"]
`)
	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}
	step := stepByID(t, f, "main")
	if step.Agent != "second-agent" {
		t.Errorf("Agent = %q, want second-agent (the LAST include's definition wins)", step.Agent)
	}
	// Redefinition replaces the template wholly: the second `.dup`
	// declares no prompt, so nothing supplies one.
	if step.Prompt != "" {
		t.Errorf("Prompt = %q, want empty — the later template redefines `.dup` and declares no prompt", step.Prompt)
	}
}

// TestParseFlowFile_NonTemplateKeysContributeNothing pins the `.` prefix
// convention: a top-level key WITHOUT the prefix is not a template.
// Deleting the prefix filter survives every other test.
func TestParseFlowFile_NonTemplateKeysContributeNothing(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)
	writeIncludeFile(t, filepath.Join(root, "steps", "mixed.yaml"), `undotted:
  agent: undotted-agent
  prompt: undotted
.real:
  agent: real-agent
  prompt: real
`)

	t.Run("the dotted template is usable", func(t *testing.T) {
		flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "ok.yaml"), `name: X
description: d
include:
  - local: steps/mixed.yaml
flow:
  steps:
    - id: main
      extends: [".real"]
`)
		f, err := parseFlowFile(flowPath)
		if err != nil {
			t.Fatalf("parseFlowFile() error: %v", err)
		}
		if got := stepByID(t, f, "main").Agent; got != "real-agent" {
			t.Errorf("Agent = %q, want real-agent", got)
		}
	})

	for _, name := range []string{"undotted", ".undotted"} {
		t.Run("extending "+name+" fails", func(t *testing.T) {
			flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "bad.yaml"), `name: X
description: d
include:
  - local: steps/mixed.yaml
flow:
  steps:
    - id: main
      extends: ["`+name+`"]
`)
			_, err := parseFlowFile(flowPath)
			if err == nil {
				t.Fatalf("expected extending %q to fail: an undotted key is not a template", name)
			}
			if !errors.Is(err, ErrUnknownTemplate) {
				t.Errorf("error = %v, want ErrUnknownTemplate", err)
			}
			// The message lists what IS available, which is how a missing
			// `.` prefix gets diagnosed.
			if !strings.Contains(err.Error(), ".real") {
				t.Errorf("error %v does not list the available templates", err)
			}
		})
	}
}

// TestParseFlowFile_InheritedValuesAreDeepCopied pins the isolation
// contract for reference-typed inherited fields. Two steps extending one
// template must not share an `*StepOutput`, its `Schema` map, the `Rules`
// backing array, `*Fallback` or `*Compact`: flows live in the
// process-wide flowCache shared by every concurrent job, so the first
// per-step mutation anyone adds would otherwise corrupt sibling steps and
// other jobs at once — and -race stays silent while nothing writes.
func TestParseFlowFile_InheritedValuesAreDeepCopied(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)
	writeIncludeFile(t, filepath.Join(root, "steps", "shared.yaml"), `.shared:
  agent: template-agent
  prompt: p
  output:
    schema:
      type: object
      properties:
        team:
          type: string
  rules:
    - then: other
  fallback:
    retry: 3
    to: other
  compact:
    threshold: 0.7
`)
	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "shared.yaml"), `name: Shared
description: d
include:
  - local: steps/shared.yaml
flow:
  steps:
    - id: main
      extends: [".shared"]
    - id: twin
      extends: [".shared"]
    - id: other
      prompt: o
`)
	f, err := parseFlowFile(flowPath)
	if err != nil {
		t.Fatalf("parseFlowFile() error: %v", err)
	}
	main := stepByID(t, f, "main")
	twin := stepByID(t, f, "twin")

	if main.Output == twin.Output {
		t.Error("both steps share the same *StepOutput")
	}
	if main.Fallback == twin.Fallback {
		t.Error("both steps share the same *Fallback")
	}
	if main.Compact == twin.Compact {
		t.Error("both steps share the same *StepCompact")
	}

	// Mutate everything reachable from one step; the other must not move.
	main.Rules[0].Then = "mutated"
	main.Output.Schema["type"] = "mutated"
	main.Output.Schema["properties"].(map[string]any)["team"] = "mutated"
	main.Fallback.Retry = 99
	main.Compact.Threshold = 0.1

	if twin.Rules[0].Then != "other" {
		t.Errorf("twin.Rules[0].Then = %q — the Rules backing array is shared", twin.Rules[0].Then)
	}
	if twin.Output.Schema["type"] != "object" {
		t.Errorf("twin schema type = %v — the Schema map is shared", twin.Output.Schema["type"])
	}
	if props, ok := twin.Output.Schema["properties"].(map[string]any); !ok || props["team"] == "mutated" {
		t.Errorf("twin nested schema = %v — nested maps are shared", twin.Output.Schema["properties"])
	}
	if twin.Fallback.Retry != 3 {
		t.Errorf("twin.Fallback.Retry = %d — Fallback is shared", twin.Fallback.Retry)
	}
	if twin.Compact.Threshold != 0.7 {
		t.Errorf("twin.Compact.Threshold = %v — Compact is shared", twin.Compact.Threshold)
	}
}

// TestParseFlowFile_TemplateChainedSchemaRefRejected pins that a
// template's output schema may not resolve to ANOTHER $ref.
// format.ResolveSchemaRef resolves exactly one level, so the second hop
// would be resolved later by the flow's own loop against the FLOW's
// directory — silently violating "a template's $ref resolves against the
// template file", and picking the wrong file when a same-named schema
// sits beside the flow (as one does here).
func TestParseFlowFile_TemplateChainedSchemaRefRejected(t *testing.T) {
	root := t.TempDir()
	setIncludeWorkspace(t, root)
	writeIncludeFile(t, filepath.Join(root, "steps", "outer.json"), `{"$ref": "inner.json"}`)
	writeIncludeFile(t, filepath.Join(root, "steps", "inner.json"), `{"type": "object", "properties": {"from": {"const": "template-dir"}}}`)
	// Decoy beside the FLOW, which the flow's own $ref loop would pick.
	writeIncludeFile(t, filepath.Join(root, "flows", "inner.json"), `{"type": "object", "properties": {"from": {"const": "flow-dir"}}}`)
	writeIncludeFile(t, filepath.Join(root, "steps", "chain.yaml"), ".chain:\n  prompt: p\n  output:\n    schema:\n      $ref: outer.json\n")

	flowPath := writeIncludeFile(t, filepath.Join(root, "flows", "chain.yaml"), `name: Chain
description: d
include:
  - local: steps/chain.yaml
flow:
  steps:
    - id: main
      extends: [".chain"]
`)
	_, err := parseFlowFile(flowPath)
	if err == nil {
		t.Fatal("expected a chained template $ref to be rejected rather than resolved against the flow's directory")
	}
	if !errors.Is(err, ErrInvalidTemplate) {
		t.Errorf("error = %v, want ErrInvalidTemplate", err)
	}
	for _, part := range []string{"$ref", ".chain", "inner.json"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %v does not mention %q", err, part)
		}
	}
}

// TestStepFieldIndexByYAMLKey_MirrorsDecoder pins the two invariants the
// reflection-driven key rule and merge both depend on.
func TestStepFieldIndexByYAMLKey_MirrorsDecoder(t *testing.T) {
	stepType := reflect.TypeOf(Step{})
	fields := stepFieldIndexByYAMLKey()

	t.Run("every mapped field is exported and settable", func(t *testing.T) {
		// An unexported field in the map makes the merge panic with
		// "reflect: reflect.Value.Set using value obtained using
		// unexported field" — a PROCESS panic, since nothing between
		// parseFlowFile and discoverFlows recovers.
		probe := reflect.ValueOf(&Step{}).Elem()
		for _, key := range sortedKeys(fields) {
			idx := fields[key]
			if !stepType.Field(idx).IsExported() {
				t.Errorf("key %q maps to unexported field %q", key, stepType.Field(idx).Name)
				continue
			}
			if !probe.Field(idx).CanSet() {
				t.Errorf("key %q maps to a field the merge cannot Set (%q)", key, stepType.Field(idx).Name)
			}
		}
	})

	t.Run("every exported step field is reachable by some key", func(t *testing.T) {
		// The invariant behind "a field added to Step later is
		// inheritable by default": an exported field missing here would
		// be settable inline yet rejected in a template.
		byIndex := map[int]string{}
		for key, idx := range fields {
			byIndex[idx] = key
		}
		for i := 0; i < stepType.NumField(); i++ {
			field := stepType.Field(i)
			if !field.IsExported() {
				if key, mapped := byIndex[i]; mapped {
					t.Errorf("unexported field %q is mapped to key %q", field.Name, key)
				}
				continue
			}
			parts := strings.Split(field.Tag.Get("yaml"), ",")
			if parts[0] == "-" {
				continue
			}
			// Mirror the derivation's own skips, or this invariant
			// contradicts them: adding a legitimate `,inline` field to
			// Step would fail HERE with the wrong reason. An inline
			// field's inner keys are what an author declares; the outer
			// field name is not a key at all (yaml.v3 inlines it), and an
			// anonymous field without `,inline` IS keyed by its lowercased
			// type name but is skipped deliberately — see yamlFieldIndexes.
			if slices.Contains(parts[1:], "inline") || field.Anonymous {
				if key, mapped := byIndex[i]; mapped {
					t.Errorf("inline/embedded field %q is mapped to key %q; the merge would zero it", field.Name, key)
				}
				continue
			}
			if _, mapped := byIndex[i]; !mapped {
				t.Errorf("exported field %q has no key — it would be settable inline but rejected in a template", field.Name)
			}
		}
	})

	t.Run("derivation skips unexported fields and lowercases untagged ones", func(t *testing.T) {
		// Step has neither shape today, so the rules are exercised
		// against a probe type — otherwise the IsExported filter and the
		// lowercase fallback could be deleted with the suite still green,
		// and the first unexported tagged field added to Step would take
		// the process down with "reflect: reflect.Value.Set using value
		// obtained using unexported field".
		type nested struct {
			Deep string `yaml:"deep"`
		}
		type embedded struct {
			Shallow string `yaml:"shallow"`
		}
		type probe struct {
			Tagged   string `yaml:"tagged"`
			Untagged string
			Skipped  string `yaml:"-"`
			hidden   string `yaml:"hidden"` //nolint:unused // presence is the point
			Inner    nested `yaml:",inline"`
			embedded `yaml:",inline"`
		}
		got := yamlFieldIndexes(reflect.TypeOf(probe{}))
		if _, ok := got["hidden"]; ok {
			t.Error("unexported tagged field is mapped; the merge would panic on it")
		}
		if _, ok := got["skipped"]; ok {
			t.Error(`yaml:"-" field is mapped`)
		}
		if _, ok := got["untagged"]; !ok {
			t.Errorf("untagged exported field not mapped to its lowercased name: %v", sortedKeys(got))
		}
		if _, ok := got["tagged"]; !ok {
			t.Errorf("tagged field not mapped: %v", sortedKeys(got))
		}
		// An inline / embedded field must contribute NO key of its own.
		// yaml.v3 accepts the inner fields' keys (`deep:`) and ignores the
		// outer field's name, so mapping `inner` would be accepted by the
		// key check, left zero by node.Decode, and then written over the
		// step's own value — silent data loss, the one shape where "add a
		// field to Step and it just works" fails quietly.
		for _, key := range []string{"inner", "embedded"} {
			if _, ok := got[key]; ok {
				t.Errorf("inline/embedded field is mapped under %q; the merge would zero the step's value", key)
			}
		}
		if len(got) != 2 {
			t.Errorf("derived %d keys (%v), want exactly tagged+untagged", len(got), sortedKeys(got))
		}
	})

	t.Run("yaml.v3 ignores an inline field's own name and takes the inner keys", func(t *testing.T) {
		// The decoder behaviour the inline skip mirrors: `deep:` lands,
		// `inner:` is ignored entirely. If this ever changes, the skip in
		// yamlFieldIndexes needs revisiting (and inline fields could then
		// be made inheritable deliberately).
		type nested struct {
			Deep string `yaml:"deep"`
		}
		var probe struct {
			Inner nested `yaml:",inline"`
		}
		if err := yaml.Unmarshal([]byte("deep: landed\n"), &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Inner.Deep != "landed" {
			t.Errorf("inline inner key did not land (Deep = %q)", probe.Inner.Deep)
		}
		probe.Inner.Deep = ""
		if err := yaml.Unmarshal([]byte("inner:\n  deep: ignored\n"), &probe); err == nil && probe.Inner.Deep != "" {
			t.Errorf("yaml.v3 now honours an inline field's own name (Deep = %q); revisit the inline skip", probe.Inner.Deep)
		}
	})

	t.Run("yaml.v3 maps an untagged exported field to its lowercased name", func(t *testing.T) {
		// The decoder behaviour our fallback mirrors. Pinned here because
		// the rule lives in our code while the behaviour it must match
		// lives in the library.
		var probe struct {
			Retries int
			Tagged  int `yaml:"custom"`
		}
		if err := yaml.Unmarshal([]byte("retries: 3\ncustom: 4\n"), &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Retries != 3 {
			t.Errorf("yaml.v3 no longer lowercases untagged field names (Retries = %d); revisit the fallback in stepFieldIndexByYAMLKey", probe.Retries)
		}
		if probe.Tagged != 4 {
			t.Errorf("tagged field = %d, want 4", probe.Tagged)
		}
	})
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
      agent: step-agent
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
					Agent:  "step-agent",
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

	// The one shape where this change could bite a flow that predates it:
	// a null / comment-only entry under `steps:` is silently dropped by the
	// typed decode (pre-existing behaviour), and the new alignment guard
	// WOULD reject it — but the guard sits behind the inertness gate in
	// parseFlowFile, so a flow declaring neither `include` nor `extends`
	// never reaches it. Removing that gate leaves the whole suite green
	// except this case.
	t.Run("a legacy flow with an empty steps entry still loads", func(t *testing.T) {
		legacy := writeIncludeFile(t, filepath.Join(dir, "placeholder-flow.yaml"), `name: Placeholder Flow
description: Predates include/extends and carries an empty steps entry
flow:
  steps:
    - # placeholder
    - id: step-one
      agent: step-agent
      prompt: "x"
`)
		f, err := parseFlowFile(legacy)
		if err != nil {
			t.Fatalf("parseFlowFile() on a pre-existing flow with an empty steps entry: %v", err)
		}
		if len(f.Spec.Steps) != 1 {
			t.Fatalf("Steps count = %d, want 1 (the empty entry is dropped by the decoder, as before)", len(f.Spec.Steps))
		}
		if got := f.Spec.Steps[0].Agent; got != "step-agent" {
			t.Errorf("Agent = %q, want step-agent", got)
		}
	})
}
