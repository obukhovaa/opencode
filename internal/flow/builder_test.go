package flow

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestBundledDynamicFlowGuideMatchesSource(t *testing.T) {
	source, err := os.ReadFile("../../.agents/skills/flow-creator/references/dynamic-flow.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != builderGuide {
		t.Fatal("embedded dynamic guide is stale; copy references/dynamic-flow.md to internal/flow/bundled/dynamic-flow.md")
	}
	// This bounds the injected instructions, not the user's goal or generated
	// definition. Catch accidentally bundling the full standalone guide again.
	if len(builderGuide) > 6000 {
		t.Fatalf("dynamic guide grew to %d bytes; keep it focused", len(builderGuide))
	}
}

func TestDynamicBuilderExamplesValidate(t *testing.T) {
	examples := regexp.MustCompile("(?s)```yaml\\n(.*?)\\n```").FindAllStringSubmatch(builderGuide, -1)
	if len(examples) == 0 {
		t.Fatal("builder guide needs a complete executable example")
	}
	for i, example := range examples {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			// Even the smallest size and a custom primary must work without
			// copying a built-in agent name out of the standalone guide.
			f, err := ParseDynamic(example[1], "builder-example", "custom-primary", "XS", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range f.Spec.Steps {
				if step.Agent != "custom-primary" {
					t.Fatalf("example lost primary identity: %s", step.Agent)
				}
				if step.MaxIterations != 1 || step.MaxTurns <= 0 || step.MaxTurns > 20 {
					t.Fatalf("example is not bounded for XS: %+v", step)
				}
			}
			if f.Spec.Steps[len(f.Spec.Steps)-1].Output != nil {
				t.Fatal("terminal example should return prose without a schema")
			}
		})
	}
}

func TestBuilderPromptKeepsRuntimeContextAndLimits(t *testing.T) {
	goal := "Review the unique-example-widget without publishing changes."
	for _, size := range []string{"auto", "XS", "S", "M", "L", "XL", "XXL"} {
		prompt := BuilderPrompt(goal, "personal", size, []string{"reviewer"})
		if strings.Count(prompt, goal) != 1 || !strings.Contains(prompt, "Primary agent: personal.") || !strings.Contains(prompt, "Available subagents: [reviewer].") || !strings.Contains(prompt, "Complexity: "+size+" ") {
			t.Fatalf("builder lost scope for %s", size)
		}
		if len(prompt) > 7500 {
			t.Fatalf("builder instruction overhead grew to %d bytes", len(prompt))
		}
		for _, name := range []string{"XS", "S", "M", "L", "XL", "XXL"} {
			limits, _ := config.DynamicFlowLimits(name)
			want := fmt.Sprintf("%s=(%d,%d,%d,%d)", name, limits.MaxSteps, limits.MaxParallel, limits.MaxTurns, limits.MaxIterations)
			if !strings.Contains(prompt, want) {
				t.Fatalf("builder limits drifted from runtime: missing %s", want)
			}
		}
	}
}
