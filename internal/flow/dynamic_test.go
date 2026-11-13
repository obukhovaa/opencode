package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dynamicTestYAML = `name: Example
flow:
  steps:
    - id: plan
      prompt: plan
      rules:
        - then: implement
    - id: implement
      prompt: implement
`

func TestDynamicDefinitionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, body, size string
		bad              bool
	}{
		{"valid", dynamicTestYAML, "S", false},
		{"different primary", strings.Replace(dynamicTestYAML, "prompt: plan", "agent: hivemind\n      prompt: plan", 1), "S", true},
		{"configured subagent", strings.Replace(dynamicTestYAML, "prompt: plan", "agent: reviewer\n      prompt: plan", 1), "S", false},
		{"turn cap", strings.Replace(dynamicTestYAML, "prompt: plan", "maxTurns: 999\n      prompt: plan", 1), "S", true},
		{"model override", strings.Replace(dynamicTestYAML, "prompt: plan", "model: other\n      prompt: plan", 1), "S", true},
		{"unknown field", strings.Replace(dynamicTestYAML, "prompt: plan", "maxTurn: 1\n      prompt: plan", 1), "S", true},
		{"unreachable", strings.Replace(dynamicTestYAML, "      rules:\n        - then: implement\n", "", 1), "S", true},
		{"shell macro", strings.Replace(dynamicTestYAML, "prompt: plan", "prompt: '!`touch unauthorized`'", 1), "S", true},
		{"invalid size", dynamicTestYAML, "s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ParseDynamic(tc.body, "dynamic-test", "coder", tc.size, map[string]bool{"reviewer": true})
			if (err != nil) != tc.bad {
				t.Fatalf("error=%v wantBad=%t", err, tc.bad)
			}
			if err == nil && (f.Spec.Steps[1].Agent != "coder" || f.Spec.Steps[1].MaxTurns != 40 || !f.Spec.Session.ResumeOnFailure) {
				t.Fatalf("defaults lost: %+v", f)
			}
		})
	}
}
func TestDynamicManifestRestartAndAmendment(t *testing.T) {
	f, err := ParseDynamic(dynamicTestYAML, "dynamic-test", "coder", "S", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "flows")
	m := DynamicManifest{RootSessionID: "../../root", Agent: "coder", Complexity: "S", Definition: *f, Steering: []string{"keep docs"}}
	if err = SaveDynamic(dir, m); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDynamic(dir, m.RootSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Steering[0] != "keep docs" || loaded.Definition.Spec.Steps[0].ID != "plan" {
		t.Fatalf("lost state: %+v", loaded)
	}
	info, _ := os.Stat(manifestPath(dir, m.RootSessionID))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", info.Mode())
	}
	next := *f
	next.Spec.Steps = append([]Step(nil), f.Spec.Steps...)
	next.Spec.Steps[1].Prompt = "updated"
	if err := ValidateDynamicAmendment(f, &next, map[string]bool{"plan": true}); err != nil {
		t.Fatal(err)
	}
	next.Spec.Steps[0].Prompt = "rewrite history"
	if err := ValidateDynamicAmendment(f, &next, map[string]bool{"plan": true}); err == nil {
		t.Fatal("entered step rewrite accepted")
	}
}

func TestDynamicDescriptionPlacementHint(t *testing.T) {
	bad := strings.Replace(dynamicTestYAML, "flow:\n", "flow:\n  description: misplaced\n", 1)
	_, err := ParseDynamic(bad, "dynamic-test", "coder", "S", nil)
	if err == nil || !strings.Contains(err.Error(), "field description not found") || !strings.Contains(err.Error(), "description belongs at the document root") {
		t.Fatalf("expected strict rejection with placement hint, got %v", err)
	}
	good := "description: A short summary\n" + dynamicTestYAML
	f, err := ParseDynamic(good, "dynamic-test", "coder", "S", nil)
	if err != nil || f.Description != "A short summary" {
		t.Fatalf("valid root description rejected: %v", err)
	}
	unknown := strings.Replace(dynamicTestYAML, "prompt: plan", "unexpected: true\n      prompt: plan", 1)
	_, err = ParseDynamic(unknown, "dynamic-test", "coder", "S", nil)
	if err == nil || strings.Contains(err.Error(), "description belongs") {
		t.Fatalf("unrelated unknown field should remain a normal strict error: %v", err)
	}
}
