package flow

import (
	"os"
	"testing"
)

func TestBundledFlowCreatorMatchesSource(t *testing.T) {
	source, err := os.ReadFile("../../.agents/skills/flow-creator/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != builderGuide {
		t.Fatal("embedded flow-creator is stale; copy .agents/skills/flow-creator/SKILL.md to internal/flow/bundled/flow-creator.md")
	}
}
