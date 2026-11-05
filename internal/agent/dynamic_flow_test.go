package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestDynamicFlowMarkdownMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "planner.md")
	if err := os.WriteFile(path, []byte("---\nmode: agent\ndynamicFlow:\n  enabled: true\n  flowComplexity: M\n---\nPlan carefully."), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := parseAgentMarkdown(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.DynamicFlow == nil || !a.DynamicFlow.Enabled || a.DynamicFlow.FlowComplexity != "M" {
		t.Fatalf("setting lost: %+v", a)
	}
	target := AgentInfo{}
	mergeMarkdownIntoExisting(&target, a)
	if target.DynamicFlow == nil || !target.DynamicFlow.Enabled {
		t.Fatal("markdown merge lost setting")
	}
	agents := map[string]AgentInfo{"planner": *a}
	cfg := &config.Config{Agents: map[config.AgentName]config.Agent{"planner": {DynamicFlow: &config.DynamicFlow{Enabled: false}}}}
	applyConfigOverrides(agents, cfg)
	if agents["planner"].DynamicFlow.Enabled {
		t.Fatal("explicit JSON disable did not override frontmatter")
	}
}
