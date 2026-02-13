package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
)

func TestParseAgentMarkdown(t *testing.T) {
	dir := t.TempDir()
	md := `---
description: Reviews code for quality
mode: subagent
tools:
  write: false
  edit: false
  bash: false
permission:
  read: allow
---

You are in code review mode. Focus on quality.
`
	path := filepath.Join(dir, "reviewer.md")
	os.WriteFile(path, []byte(md), 0o644)

	agent, err := parseAgentMarkdown(path)
	if err != nil {
		t.Fatalf("parseAgentMarkdown() error = %v", err)
	}

	if agent.ID != "reviewer" {
		t.Errorf("ID = %q, want %q", agent.ID, "reviewer")
	}
	if agent.Description != "Reviews code for quality" {
		t.Errorf("Description = %q, want %q", agent.Description, "Reviews code for quality")
	}
	if agent.Mode != config.AgentModeSubagent {
		t.Errorf("Mode = %q, want %q", agent.Mode, config.AgentModeSubagent)
	}
	if agent.Tools["write"] != false {
		t.Error("Tools[write] should be false")
	}
	if agent.Tools["bash"] != false {
		t.Error("Tools[bash] should be false")
	}
	if agent.Prompt == "" {
		t.Error("Prompt should not be empty")
	}
	if !contains(agent.Prompt, "code review mode") {
		t.Errorf("Prompt = %q, should contain 'code review mode'", agent.Prompt)
	}
}

func TestScanAgentDirectory(t *testing.T) {
	dir := t.TempDir()

	md1 := `---
description: Agent one
mode: subagent
---

Prompt for agent one.
`
	md2 := `---
description: Agent two
mode: agent
color: "#FF0000"
---

Prompt for agent two.
`
	os.WriteFile(filepath.Join(dir, "agent-one.md"), []byte(md1), 0o644)
	os.WriteFile(filepath.Join(dir, "agent-two.md"), []byte(md2), 0o644)
	os.WriteFile(filepath.Join(dir, "not-an-agent.txt"), []byte("ignore"), 0o644)

	agents := scanAgentDirectory(dir)
	if len(agents) != 2 {
		t.Fatalf("scanAgentDirectory() returned %d agents, want 2", len(agents))
	}

	ids := map[string]bool{}
	for _, a := range agents {
		ids[a.ID] = true
	}
	if !ids["agent-one"] {
		t.Error("missing agent-one")
	}
	if !ids["agent-two"] {
		t.Error("missing agent-two")
	}
}

func TestRegistryBuiltins(t *testing.T) {
	agents := make(map[string]AgentInfo)
	cfg := &config.Config{
		Agents: make(map[config.AgentName]config.Agent),
	}
	registerBuiltins(agents, cfg)

	expected := []string{
		config.AgentCoder,
		config.AgentHivemind,
		config.AgentExplorer,
		config.AgentWorkhorse,
		config.AgentSummarizer,
		config.AgentDescriptor,
	}

	for _, id := range expected {
		a, ok := agents[id]
		if !ok {
			t.Errorf("builtin agent %q not registered", id)
			continue
		}
		if a.Name == "" {
			t.Errorf("builtin agent %q has empty Name", id)
		}
		if a.Mode == "" {
			t.Errorf("builtin agent %q has empty Mode", id)
		}
		if !a.Native {
			t.Errorf("builtin agent %q should be native", id)
		}
	}

	// Verify tool restrictions on specific agents
	hivemind := agents[config.AgentHivemind]
	if hivemind.Tools == nil {
		t.Error("hivemind should have Tools restrictions")
	} else {
		for _, tool := range []string{"bash", "edit", "multiedit", "write", "delete", "patch", "lsp"} {
			if enabled, exists := hivemind.Tools[tool]; !exists || enabled {
				t.Errorf("hivemind Tools[%q] should be false", tool)
			}
		}
	}

	explorer := agents[config.AgentExplorer]
	if explorer.Tools == nil {
		t.Error("explorer should have Tools restrictions")
	} else {
		for _, tool := range []string{"bash", "edit", "multiedit", "write", "delete", "patch", "task"} {
			if enabled, exists := explorer.Tools[tool]; !exists || enabled {
				t.Errorf("explorer Tools[%q] should be false", tool)
			}
		}
	}

	summarizer := agents[config.AgentSummarizer]
	if summarizer.Tools == nil {
		t.Error("summarizer should have Tools restrictions")
	} else if enabled, exists := summarizer.Tools["*"]; !exists || enabled {
		t.Error("summarizer Tools[*] should be false")
	}

	descriptor := agents[config.AgentDescriptor]
	if descriptor.Tools == nil {
		t.Error("descriptor should have Tools restrictions")
	} else if enabled, exists := descriptor.Tools["*"]; !exists || enabled {
		t.Error("descriptor Tools[*] should be false")
	}

	// Coder and workhorse should have nil Tools (all enabled)
	if agents[config.AgentCoder].Tools != nil {
		t.Error("coder should have nil Tools (all enabled)")
	}
	if agents[config.AgentWorkhorse].Tools == nil {
		t.Error("workhorse should have Tools restrictions")
	} else {
		for _, tool := range []string{"task"} {
			if enabled, exists := agents[config.AgentWorkhorse].Tools[tool]; !exists || enabled {
				t.Errorf("explorer Tools[%q] should be false", tool)
			}
		}
	}
}

func TestRegistryEvaluatePermission(t *testing.T) {
	r := &registry{
		agents: map[string]AgentInfo{
			"readonly": {
				ID:   "readonly",
				Mode: config.AgentModeSubagent,
				Permission: map[string]any{
					"bash": "deny",
					"edit": "deny",
					"read": "allow",
				},
				Tools: map[string]bool{
					"bash": false,
				},
			},
		},
		globalPerms: map[string]any{
			"bash": "ask",
		},
	}

	if got := r.EvaluatePermission("readonly", "bash", "git status"); got != permission.ActionDeny {
		t.Errorf("bash should be denied for readonly agent, got %v", got)
	}
	if got := r.EvaluatePermission("readonly", "read", "src/main.go"); got != permission.ActionAllow {
		t.Errorf("read should be allowed for readonly agent, got %v", got)
	}
	if got := r.IsToolEnabled("readonly", "bash"); got != false {
		t.Error("bash should be disabled for readonly agent")
	}
	if got := r.EvaluatePermission("unknown", "bash", "git status"); got != permission.ActionAsk {
		t.Errorf("unknown agent should fallback to global, got %v", got)
	}
}

func TestMergeMarkdownIntoExisting(t *testing.T) {
	existing := AgentInfo{
		ID:          "coder",
		Name:        "Coder Agent",
		Description: "Original description",
		Mode:        config.AgentModeAgent,
		Native:      true,
	}

	md := AgentInfo{
		Description: "Override description",
		Color:       "#00FF00",
		Prompt:      "Custom prompt",
	}

	mergeMarkdownIntoExisting(&existing, &md)

	if existing.Description != "Override description" {
		t.Errorf("Description not merged, got %q", existing.Description)
	}
	if existing.Color != "#00FF00" {
		t.Errorf("Color not merged, got %q", existing.Color)
	}
	if existing.Prompt != "Custom prompt" {
		t.Errorf("Prompt not merged, got %q", existing.Prompt)
	}
	if existing.Name != "Coder Agent" {
		t.Errorf("Name should not be overwritten by empty, got %q", existing.Name)
	}
	if !existing.Native {
		t.Error("Native should be preserved")
	}
}

func TestConfigOverrides(t *testing.T) {
	agents := map[string]AgentInfo{
		"coder": {
			ID:   "coder",
			Name: "Coder Agent",
			Mode: config.AgentModeAgent,
		},
	}

	cfg := &config.Config{
		Agents: map[config.AgentName]config.Agent{
			"coder": {
				Name:        "My Custom Coder",
				Description: "Customized coder",
			},
			"custom-agent": {
				Name:        "Custom Agent",
				Description: "A new agent from config",
				Mode:        config.AgentModeSubagent,
			},
		},
	}

	applyConfigOverrides(agents, cfg)

	if agents["coder"].Name != "My Custom Coder" {
		t.Errorf("coder name not overridden, got %q", agents["coder"].Name)
	}
	if agents["coder"].Description != "Customized coder" {
		t.Errorf("coder description not overridden, got %q", agents["coder"].Description)
	}

	custom, ok := agents["custom-agent"]
	if !ok {
		t.Fatal("custom-agent not created from config")
	}
	if custom.Name != "Custom Agent" {
		t.Errorf("custom-agent name = %q, want %q", custom.Name, "Custom Agent")
	}
	if custom.Mode != config.AgentModeSubagent {
		t.Errorf("custom-agent mode = %q, want subagent", custom.Mode)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
