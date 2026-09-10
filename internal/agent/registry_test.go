package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
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

// Upstream dax/opencode spells the primary agent mode as "primary"; we use
// "agent". parseAgentMarkdown must accept the dax spelling so .md files
// authored against either fork drop in unchanged.
func TestParseAgentMarkdown_PrimaryAlias(t *testing.T) {
	dir := t.TempDir()
	md := `---
description: dax-style primary agent
mode: primary
---

Body.
`
	path := filepath.Join(dir, "primary-agent.md")
	os.WriteFile(path, []byte(md), 0o644)

	agent, err := parseAgentMarkdown(path)
	if err != nil {
		t.Fatalf("parseAgentMarkdown() error = %v", err)
	}
	if agent.Mode != config.AgentModeAgent {
		t.Errorf("Mode = %q, want %q (primary should alias to agent)", agent.Mode, config.AgentModeAgent)
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

	// Coder should have nil Tools (all standard tools enabled; cron tools are
	// default-deny via IsToolExplicitlyEnabled, no per-agent flag needed).
	if agents[config.AgentCoder].Tools != nil {
		t.Error("coder should have nil Tools (all enabled by default)")
	}

	// Hivemind opts in to cron tools explicitly so the coordinator can
	// schedule recurring tasks. Other restrictions still apply.
	hivemindTools := agents[config.AgentHivemind].Tools
	if hivemindTools == nil {
		t.Fatal("hivemind should have Tools restrictions")
	}
	for _, tool := range []string{"croncreate", "crondelete", "cronlist"} {
		if enabled, exists := hivemindTools[tool]; !exists || !enabled {
			t.Errorf("hivemind Tools[%q] should be true (explicit opt-in)", tool)
		}
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

func TestRegistryEvaluateReadPermission(t *testing.T) {
	r := &registry{
		agents: map[string]AgentInfo{
			"explorer": {
				ID:   "explorer",
				Mode: config.AgentModeSubagent,
				Permission: map[string]any{
					"read": map[string]any{
						"*":       "allow",
						"/proc/*": "deny",
						"/sys/*":  "deny",
					},
				},
			},
			"restricted-grep": {
				ID:   "restricted-grep",
				Mode: config.AgentModeSubagent,
				Permission: map[string]any{
					"read": map[string]any{
						"/proc/*": "deny",
					},
					"grep": map[string]any{
						"/proc/*": "allow",
					},
				},
			},
		},
		globalPerms: map[string]any{},
	}

	// read category denies /proc/* for explorer
	if got := r.EvaluateReadPermission("explorer", "grep", "/proc/cpuinfo"); got != permission.ActionDeny {
		t.Errorf("grep /proc should be denied for explorer, got %v", got)
	}
	// read category denies /sys/* for explorer
	if got := r.EvaluateReadPermission("explorer", "ls", "/sys/class"); got != permission.ActionDeny {
		t.Errorf("ls /sys should be denied for explorer, got %v", got)
	}
	// other paths allowed for explorer
	if got := r.EvaluateReadPermission("explorer", "read", "/home/user/file.go"); got != permission.ActionAllow {
		t.Errorf("read /home should be allowed for explorer, got %v", got)
	}
	// specific grep override allows /proc for restricted-grep
	if got := r.EvaluateReadPermission("restricted-grep", "grep", "/proc/cpuinfo"); got != permission.ActionAllow {
		t.Errorf("grep /proc should be allowed for restricted-grep (overrides read), got %v", got)
	}
	// read still denies /proc for ls on restricted-grep
	if got := r.EvaluateReadPermission("restricted-grep", "ls", "/proc/cpuinfo"); got != permission.ActionDeny {
		t.Errorf("ls /proc should be denied for restricted-grep (no override), got %v", got)
	}
	// unknown agent defaults to allow
	if got := r.EvaluateReadPermission("unknown", "grep", "/anything"); got != permission.ActionAllow {
		t.Errorf("unknown agent should default to allow, got %v", got)
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
		Color:       "secondary",
		Prompt:      "Custom prompt",
	}

	mergeMarkdownIntoExisting(&existing, &md)

	if existing.Description != "Override description" {
		t.Errorf("Description not merged, got %q", existing.Description)
	}
	if existing.Color != "secondary" {
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

func TestDisabledAgentRemovedFromRegistry(t *testing.T) {
	agents := map[string]AgentInfo{
		"enabled-agent": {
			ID:   "enabled-agent",
			Name: "Enabled",
			Mode: config.AgentModeSubagent,
		},
		"disabled-agent": {
			ID:       "disabled-agent",
			Name:     "Disabled",
			Mode:     config.AgentModeSubagent,
			Disabled: true,
		},
	}

	removeDisabledAgents(agents)

	if _, ok := agents["enabled-agent"]; !ok {
		t.Error("enabled-agent should still be in registry")
	}
	if _, ok := agents["disabled-agent"]; ok {
		t.Error("disabled-agent should be removed from registry")
	}
}

func TestDisabledViaConfigOverride(t *testing.T) {
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
				Disabled: true,
			},
		},
	}

	applyConfigOverrides(agents, cfg)

	if !agents["coder"].Disabled {
		t.Error("coder should be marked as disabled after config override")
	}
}

func TestDisabledViaMarkdownMerge(t *testing.T) {
	existing := AgentInfo{
		ID:   "myagent",
		Name: "My Agent",
		Mode: config.AgentModeSubagent,
	}

	md := AgentInfo{
		Disabled: true,
	}

	mergeMarkdownIntoExisting(&existing, &md)

	if !existing.Disabled {
		t.Error("agent should be marked as disabled after markdown merge")
	}
}

func TestDeduplicateSkills(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty",
			input:    nil,
			expected: nil,
		},
		{
			name:     "no duplicates",
			input:    []string{"review", "commit", "deploy"},
			expected: []string{"review", "commit", "deploy"},
		},
		{
			name:     "with duplicates",
			input:    []string{"review", "commit", "review", "deploy", "commit"},
			expected: []string{"review", "commit", "deploy"},
		},
		{
			name:     "all same",
			input:    []string{"review", "review", "review"},
			expected: []string{"review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deduplicateSkills(tt.input, "test-agent")
			if tt.expected == nil {
				if got != nil {
					t.Errorf("deduplicateSkills() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.expected) {
				t.Fatalf("deduplicateSkills() len = %d, want %d", len(got), len(tt.expected))
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("deduplicateSkills()[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestParseAgentMarkdownWithSkills(t *testing.T) {
	dir := t.TempDir()
	md := `---
description: Domain expert
mode: subagent
skills:
  - review
  - domain-knowledge
---

You are a domain expert.
`
	path := filepath.Join(dir, "expert.md")
	os.WriteFile(path, []byte(md), 0o644)

	agent, err := parseAgentMarkdown(path)
	if err != nil {
		t.Fatalf("parseAgentMarkdown() error = %v", err)
	}

	if len(agent.Skills) != 2 {
		t.Fatalf("Skills len = %d, want 2", len(agent.Skills))
	}
	if agent.Skills[0] != "review" || agent.Skills[1] != "domain-knowledge" {
		t.Errorf("Skills = %v, want [review, domain-knowledge]", agent.Skills)
	}
}

func TestMergeMarkdownSkillsReplace(t *testing.T) {
	existing := AgentInfo{
		ID:     "myagent",
		Skills: []string{"skill-a", "skill-b"},
	}

	md := AgentInfo{
		Skills: []string{"skill-c"},
	}

	mergeMarkdownIntoExisting(&existing, &md)

	if len(existing.Skills) != 1 || existing.Skills[0] != "skill-c" {
		t.Errorf("Skills should be replaced, got %v", existing.Skills)
	}
}

func TestMergeMarkdownSkillsNilPreserves(t *testing.T) {
	existing := AgentInfo{
		ID:     "myagent",
		Skills: []string{"skill-a"},
	}

	md := AgentInfo{}

	mergeMarkdownIntoExisting(&existing, &md)

	if len(existing.Skills) != 1 || existing.Skills[0] != "skill-a" {
		t.Errorf("nil Skills in overlay should preserve existing, got %v", existing.Skills)
	}
}

// TestParseAgentMarkdownWithContext pins the context-resolution spec
// scenario "Markdown-agent frontmatter sets scoped paths": the `context`
// object round-trips from YAML frontmatter into AgentInfo.Context.
func TestParseAgentMarkdownWithContext(t *testing.T) {
	dir := t.TempDir()
	md := `---
description: Runtime specialist
mode: subagent
context:
  paths:
    - RUNTIME.md
    - AGENTS.${agent}.md
  mode: replace
  nested: false
---

You focus on runtime concerns.
`
	path := filepath.Join(dir, "runtime.md")
	os.WriteFile(path, []byte(md), 0o644)

	agent, err := parseAgentMarkdown(path)
	if err != nil {
		t.Fatalf("parseAgentMarkdown() error = %v", err)
	}

	if agent.Context == nil {
		t.Fatal("Context frontmatter did not parse")
	}
	if len(agent.Context.Paths) != 2 || agent.Context.Paths[0] != "RUNTIME.md" || agent.Context.Paths[1] != "AGENTS.${agent}.md" {
		t.Errorf("Context.Paths = %v, want [RUNTIME.md AGENTS.${agent}.md]", agent.Context.Paths)
	}
	if agent.Context.Mode != "replace" {
		t.Errorf("Context.Mode = %q, want replace", agent.Context.Mode)
	}
	if agent.Context.Nested == nil || *agent.Context.Nested {
		t.Errorf("Context.Nested = %v, want false", agent.Context.Nested)
	}
}

// TestMergeMarkdownContextReplace mirrors the Skills merge cases: a
// declared markdown `context` replaces the inherited object wholly.
func TestMergeMarkdownContextReplace(t *testing.T) {
	existing := AgentInfo{
		ID:      "myagent",
		Context: &contextfile.AgentContext{Paths: []string{"OLD.md"}, Mode: "append"},
	}

	md := AgentInfo{
		Context: &contextfile.AgentContext{Paths: []string{"NEW.md"}, Mode: "replace"},
	}

	mergeMarkdownIntoExisting(&existing, &md)

	if existing.Context == nil || len(existing.Context.Paths) != 1 || existing.Context.Paths[0] != "NEW.md" {
		t.Errorf("Context should be replaced wholly, got %+v", existing.Context)
	}
	if existing.Context.Mode != "replace" {
		t.Errorf("Context.Mode = %q, want replace", existing.Context.Mode)
	}
}

// TestMergeMarkdownContextNilPreserves: a markdown file without a
// `context` block must not clear an inherited one.
func TestMergeMarkdownContextNilPreserves(t *testing.T) {
	existing := AgentInfo{
		ID:      "myagent",
		Context: &contextfile.AgentContext{Paths: []string{"KEEP.md"}, Mode: "replace"},
	}

	md := AgentInfo{Description: "just a description override"}

	mergeMarkdownIntoExisting(&existing, &md)

	if existing.Context == nil || len(existing.Context.Paths) != 1 || existing.Context.Paths[0] != "KEEP.md" {
		t.Errorf("nil Context in overlay should preserve existing, got %+v", existing.Context)
	}
}

func TestConfigOverridesSkills(t *testing.T) {
	agents := map[string]AgentInfo{
		"coder": {
			ID:     "coder",
			Name:   "Coder Agent",
			Mode:   config.AgentModeAgent,
			Skills: []string{"old-skill"},
		},
	}

	cfg := &config.Config{
		Agents: map[config.AgentName]config.Agent{
			"coder": {
				Skills: []string{"new-skill-a", "new-skill-b"},
			},
		},
	}

	applyConfigOverrides(agents, cfg)

	coder := agents["coder"]
	if len(coder.Skills) != 2 {
		t.Fatalf("Skills len = %d, want 2", len(coder.Skills))
	}
	if coder.Skills[0] != "new-skill-a" || coder.Skills[1] != "new-skill-b" {
		t.Errorf("Skills = %v, want [new-skill-a, new-skill-b]", coder.Skills)
	}
}

func TestConfigOverridesSkillsDedup(t *testing.T) {
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
				Skills: []string{"review", "commit", "review"},
			},
		},
	}

	applyConfigOverrides(agents, cfg)

	coder := agents["coder"]
	if len(coder.Skills) != 2 {
		t.Fatalf("Skills should be deduped to 2, got %d: %v", len(coder.Skills), coder.Skills)
	}
}

func TestDiscoverCustomPathMarkdownAgents(t *testing.T) {
	tmpDir := t.TempDir()

	customDir := filepath.Join(tmpDir, "my-agents")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := `---
description: Custom agent from a custom path
mode: subagent
---

Custom agent prompt.
`
	if err := os.WriteFile(filepath.Join(customDir, "custom-agent.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-markdown file in the same dir must be ignored.
	if err := os.WriteFile(filepath.Join(customDir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Absolute path.
	absAgents := discoverCustomPathMarkdownAgents(&config.Config{
		WorkingDir: tmpDir,
		AgentPaths: []string{filepath.Join(tmpDir, "my-agents")},
	})
	if len(absAgents) != 1 {
		t.Fatalf("absolute path: got %d agents, want 1", len(absAgents))
	}
	if absAgents[0].ID != "custom-agent" {
		t.Errorf("absolute path: ID = %q, want custom-agent", absAgents[0].ID)
	}
	if absAgents[0].Mode != config.AgentModeSubagent {
		t.Errorf("absolute path: Mode = %q, want subagent", absAgents[0].Mode)
	}

	// Relative path, resolved against WorkingDir.
	relAgents := discoverCustomPathMarkdownAgents(&config.Config{
		WorkingDir: tmpDir,
		AgentPaths: []string{"my-agents"},
	})
	if len(relAgents) != 1 {
		t.Fatalf("relative path: got %d agents, want 1", len(relAgents))
	}

	// Missing path is skipped without error.
	missingAgents := discoverCustomPathMarkdownAgents(&config.Config{
		WorkingDir: tmpDir,
		AgentPaths: []string{filepath.Join(tmpDir, "does-not-exist")},
	})
	if len(missingAgents) != 0 {
		t.Errorf("missing path: got %d agents, want 0", len(missingAgents))
	}

	// Nil config and empty AgentPaths both yield nothing.
	if got := discoverCustomPathMarkdownAgents(nil); got != nil {
		t.Errorf("nil config: got %v, want nil", got)
	}
	if got := discoverCustomPathMarkdownAgents(&config.Config{WorkingDir: tmpDir}); len(got) != 0 {
		t.Errorf("empty AgentPaths: got %d agents, want 0", len(got))
	}
}

// TestCustomPathAgentsLowestPrecedence verifies that a project agent wins
// over a custom-path agent with the same ID: custom paths are additive and
// have the lowest precedence among discovery sources.
func TestCustomPathAgentsLowestPrecedence(t *testing.T) {
	tmpDir := t.TempDir()

	// Project source: .opencode/agents/dup.md
	projDir := filepath.Join(tmpDir, ".opencode", "agents")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projMD := "---\ndescription: project version\nmode: subagent\n---\nproject body"
	if err := os.WriteFile(filepath.Join(projDir, "dup.md"), []byte(projMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// Custom path source: <tmp>/custom/dup.md (+ a unique agent)
	customDir := filepath.Join(tmpDir, "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customDupMD := "---\ndescription: custom version\nmode: subagent\n---\ncustom body"
	if err := os.WriteFile(filepath.Join(customDir, "dup.md"), []byte(customDupMD), 0o644); err != nil {
		t.Fatal(err)
	}
	uniqueMD := "---\ndescription: only in custom path\nmode: subagent\n---\nunique body"
	if err := os.WriteFile(filepath.Join(customDir, "unique.md"), []byte(uniqueMD), 0o644); err != nil {
		t.Fatal(err)
	}

	agents := map[string]AgentInfo{}
	discoverMarkdownAgents(agents, &config.Config{
		WorkingDir: tmpDir,
		AgentPaths: []string{customDir},
	})

	dup, ok := agents["dup"]
	if !ok {
		t.Fatal("dup agent not discovered")
	}
	if dup.Description != "project version" {
		t.Errorf("Description = %q, want %q (project must win over custom path)", dup.Description, "project version")
	}

	// A custom-path agent with a non-colliding ID is still contributed.
	if _, ok := agents["unique"]; !ok {
		t.Error("unique custom-path agent should be discovered")
	}
}

func TestParseAgentMarkdownAllowTools(t *testing.T) {
	dir := t.TempDir()

	t.Run("allowTools alone", func(t *testing.T) {
		md := `---
description: Narrow runner
mode: subagent
allowTools:
  - struct_output
  - scenario-run_*
---

Body.
`
		path := filepath.Join(dir, "runner.md")
		os.WriteFile(path, []byte(md), 0o644)

		agent, err := parseAgentMarkdown(path)
		if err != nil {
			t.Fatalf("parseAgentMarkdown() error = %v", err)
		}
		if !agent.UsesToolAllowlist() {
			t.Fatal("agent should be in allowlist mode")
		}
		if len(agent.AllowTools) != 2 || agent.AllowTools[0] != "struct_output" {
			t.Errorf("AllowTools = %v, want [struct_output scenario-run_*]", agent.AllowTools)
		}
		if agent.Tools != nil {
			t.Errorf("Tools should stay nil, got %v", agent.Tools)
		}
	})

	t.Run("tools alone", func(t *testing.T) {
		md := `---
description: Deny-list agent
tools:
  bash: false
---

Body.
`
		path := filepath.Join(dir, "denylist.md")
		os.WriteFile(path, []byte(md), 0o644)

		agent, err := parseAgentMarkdown(path)
		if err != nil {
			t.Fatalf("parseAgentMarkdown() error = %v", err)
		}
		if agent.UsesToolAllowlist() {
			t.Error("agent should not be in allowlist mode")
		}
	})

	t.Run("both keys rejected, error names the file", func(t *testing.T) {
		md := `---
description: Confused agent
tools:
  bash: false
allowTools:
  - read
---

Body.
`
		path := filepath.Join(dir, "confused.md")
		os.WriteFile(path, []byte(md), 0o644)

		_, err := parseAgentMarkdown(path)
		if err == nil {
			t.Fatal("parseAgentMarkdown() should reject tools + allowTools in one file")
		}
		if !contains(err.Error(), path) {
			t.Errorf("error = %q, should name the file path %q", err.Error(), path)
		}
		if !contains(err.Error(), "allowTools") {
			t.Errorf("error = %q, should mention allowTools", err.Error())
		}
	})
}

// The predicate table is the security boundary: all five gates must agree on
// what an allowlist grants, and a deny-list agent must be unaffected.
func TestRegistryAllowlistGates(t *testing.T) {
	r := &registry{
		agents: map[string]AgentInfo{
			"narrow": {
				ID:   "narrow",
				Mode: config.AgentModeSubagent,
				AllowTools: []string{
					"read",
					"struct_output",
					"gitlab_*",
				},
			},
			"cron-user": {
				ID:         "cron-user",
				Mode:       config.AgentModeAgent,
				AllowTools: []string{"croncreate"},
			},
			"star": {
				ID:         "star",
				Mode:       config.AgentModeAgent,
				AllowTools: []string{"*"},
			},
			"denylist": {
				ID:   "denylist",
				Mode: config.AgentModeSubagent,
				Tools: map[string]bool{
					"bash":       false,
					"croncreate": true,
				},
			},
		},
	}

	tests := []struct {
		name              string
		agent             string
		tool              string
		wantEnabled       bool
		wantExplicit      bool
		wantPermission    permission.Action
		wantReadPermision permission.Action
	}{
		{"listed exact", "narrow", "read", true, true, permission.ActionAsk, permission.ActionAllow},
		{"listed engine tool", "narrow", "struct_output", true, true, permission.ActionAsk, permission.ActionAllow},
		{"listed MCP wildcard", "narrow", "gitlab_list_issues", true, true, permission.ActionAsk, permission.ActionAllow},
		{"unlisted tool", "narrow", "bash", false, false, permission.ActionDeny, permission.ActionDeny},
		{"unlisted grep is denied too", "narrow", "grep", false, false, permission.ActionDeny, permission.ActionDeny},
		{"cron needs listing", "narrow", "croncreate", false, false, permission.ActionDeny, permission.ActionDeny},
		{"cron listed is explicit", "cron-user", "croncreate", true, true, permission.ActionAsk, permission.ActionAllow},
		// A bare "*" is "everything the deny-list default would give me", and
		// that default never included cron: allowed, but not an opt-in.
		{"star allows everything", "star", "bash", true, false, permission.ActionAsk, permission.ActionAllow},
		{"star does not opt in to cron", "star", "croncreate", true, false, permission.ActionAsk, permission.ActionAllow},
		// Deny-list agents keep their existing semantics.
		{"deny-list unmentioned stays enabled", "denylist", "write", true, false, permission.ActionAsk, permission.ActionAllow},
		{"deny-list explicit false", "denylist", "bash", false, false, permission.ActionDeny, permission.ActionDeny},
		{"deny-list explicit true", "denylist", "croncreate", true, true, permission.ActionAsk, permission.ActionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.IsToolEnabled(tt.agent, tt.tool); got != tt.wantEnabled {
				t.Errorf("IsToolEnabled(%q, %q) = %v, want %v", tt.agent, tt.tool, got, tt.wantEnabled)
			}
			if got := r.IsToolExplicitlyEnabled(tt.agent, tt.tool); got != tt.wantExplicit {
				t.Errorf("IsToolExplicitlyEnabled(%q, %q) = %v, want %v", tt.agent, tt.tool, got, tt.wantExplicit)
			}
			if got := r.EvaluatePermission(tt.agent, tt.tool, ""); got != tt.wantPermission {
				t.Errorf("EvaluatePermission(%q, %q) = %v, want %v", tt.agent, tt.tool, got, tt.wantPermission)
			}
			if got := r.EvaluateReadPermission(tt.agent, tt.tool, ""); got != tt.wantReadPermision {
				t.Errorf("EvaluateReadPermission(%q, %q) = %v, want %v", tt.agent, tt.tool, got, tt.wantReadPermision)
			}
		})
	}

	// A non-empty allowlist means the agent has tools, so it keeps the
	// parallel-tool-use and background-task prompt sections.
	for _, id := range []string{"narrow", "cron-user", "star"} {
		if !r.HasTools(id) {
			t.Errorf("HasTools(%q) = false, want true for an allowlisted agent", id)
		}
	}
}

func TestRegistryHasTools(t *testing.T) {
	tests := []struct {
		name  string
		tools map[string]bool
		want  bool
	}{
		{"nil tools", nil, true},
		{"deny-list without star", map[string]bool{"bash": false}, true},
		{"star false alone means tool-less", map[string]bool{"*": false}, false},
		// The pre-allowTools workaround: "*": false plus explicit grants. Read
		// as tool-less, it withheld the parallel-tool-use and background-task
		// prompt sections from agents that were using tools all along.
		{"star false with a grant has tools", map[string]bool{"*": false, "read": true}, true},
		{"star false with only denials is tool-less", map[string]bool{"*": false, "read": false}, false},
		{"star true", map[string]bool{"*": true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &registry{agents: map[string]AgentInfo{"a": {ID: "a", Tools: tt.tools}}}
			if got := r.HasTools("a"); got != tt.want {
				t.Errorf("HasTools() = %v, want %v", got, tt.want)
			}
		})
	}
	r := &registry{agents: map[string]AgentInfo{}}
	if !r.HasTools("unknown") {
		t.Error("unknown agent should be treated as having tools")
	}
}

// Built-in deny-list goldens: the gates must be bit-identical to pre-allowTools
// behaviour for every agent that does not use the new key. Cron is the one to
// watch — hivemind opts in, everyone else must not.
func TestBuiltinToolGatesUnchanged(t *testing.T) {
	agents := make(map[string]AgentInfo)
	registerBuiltins(agents, &config.Config{Agents: make(map[config.AgentName]config.Agent)})
	r := &registry{agents: agents}

	for _, id := range []string{
		config.AgentCoder, config.AgentHivemind, config.AgentExplorer,
		config.AgentWorkhorse, config.AgentSummarizer, config.AgentDescriptor,
	} {
		if agents[id].UsesToolAllowlist() {
			t.Errorf("builtin %q should not use an allowlist", id)
		}
	}

	tests := []struct {
		agent, tool string
		want        bool
	}{
		{config.AgentCoder, "bash", true},
		{config.AgentCoder, "read", true},
		{config.AgentHivemind, "bash", false},
		{config.AgentHivemind, "lsp", false},
		{config.AgentHivemind, "read", true},
		{config.AgentExplorer, "bash", false},
		{config.AgentExplorer, "grep", true},
		{config.AgentWorkhorse, "task", false},
		{config.AgentWorkhorse, "bash", true},
		{config.AgentSummarizer, "read", false},
		{config.AgentDescriptor, "read", false},
	}
	for _, tt := range tests {
		if got := r.IsToolEnabled(tt.agent, tt.tool); got != tt.want {
			t.Errorf("IsToolEnabled(%q, %q) = %v, want %v", tt.agent, tt.tool, got, tt.want)
		}
	}

	if !r.IsToolExplicitlyEnabled(config.AgentHivemind, "croncreate") {
		t.Error("hivemind should keep its explicit cron opt-in")
	}
	if r.IsToolExplicitlyEnabled(config.AgentCoder, "croncreate") {
		t.Error("coder must not get cron: default-deny needs an explicit opt-in")
	}
	// Tool-less builtins stay tool-less: the HasTools fix must not hand the
	// summarizer and descriptor prompt sections they cannot use.
	if r.HasTools(config.AgentSummarizer) || r.HasTools(config.AgentDescriptor) {
		t.Error("summarizer/descriptor should still report no tools")
	}
	if !r.HasTools(config.AgentHivemind) {
		t.Error("hivemind should report tools")
	}
}

func TestAllowToolsDropsInheritedTools(t *testing.T) {
	t.Run("markdown over builtin", func(t *testing.T) {
		agents := make(map[string]AgentInfo)
		registerBuiltins(agents, &config.Config{Agents: make(map[config.AgentName]config.Agent)})
		existing := agents[config.AgentExplorer]
		if len(existing.Tools) == 0 {
			t.Fatal("precondition: explorer should ship a Tools map")
		}

		md := AgentInfo{
			ID:         config.AgentExplorer,
			AllowTools: []string{"read", "grep", "read"},
			Location:   "/tmp/explorer.md",
		}
		mergeMarkdownIntoExisting(&existing, &md)

		if existing.Tools != nil {
			t.Errorf("inherited Tools should be dropped, got %v", existing.Tools)
		}
		if len(existing.AllowTools) != 2 {
			t.Errorf("AllowTools = %v, want the duplicate dropped", existing.AllowTools)
		}
		if !existing.UsesToolAllowlist() {
			t.Error("explorer should be in allowlist mode")
		}
		if existing.ToolEnabled("bash") {
			t.Error("bash should be denied under the allowlist")
		}
		if !existing.ToolEnabled("grep") {
			t.Error("grep should be allowed")
		}
	})

	t.Run("config over builtin", func(t *testing.T) {
		agents := make(map[string]AgentInfo)
		registerBuiltins(agents, &config.Config{Agents: make(map[config.AgentName]config.Agent)})
		cfg := &config.Config{
			Agents: map[config.AgentName]config.Agent{
				config.AgentHivemind: {AllowTools: []string{"read", "task"}},
			},
		}
		applyConfigOverrides(agents, cfg)

		hivemind := agents[config.AgentHivemind]
		if hivemind.Tools != nil {
			t.Errorf("inherited Tools should be dropped, got %v", hivemind.Tools)
		}
		// The dropped map carried hivemind's cron opt-in — hence the warning
		// the registry logs. The allowlist has to restate it to keep it.
		if hivemind.ToolExplicitlyEnabled("croncreate") {
			t.Error("croncreate should be gone with the dropped Tools map")
		}
		if !hivemind.ToolEnabled("task") {
			t.Error("task should be allowed")
		}
	})

	t.Run("tools over an inherited allowlist", func(t *testing.T) {
		existing := AgentInfo{ID: "narrow", AllowTools: []string{"read"}}
		md := AgentInfo{ID: "narrow", Tools: map[string]bool{"bash": false}, Location: "/tmp/narrow.md"}
		mergeMarkdownIntoExisting(&existing, &md)

		if existing.AllowTools != nil {
			t.Errorf("inherited AllowTools should be dropped, got %v", existing.AllowTools)
		}
		if existing.UsesToolAllowlist() {
			t.Error("agent should be back in deny-list mode")
		}
		if !existing.ToolEnabled("write") {
			t.Error("deny-list mode should re-enable unmentioned tools")
		}
	})
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
