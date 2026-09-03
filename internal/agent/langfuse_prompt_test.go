package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func writeAgentMarkdown(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestParseAgentMarkdown_LangfusePromptReference(t *testing.T) {
	t.Run("frontmatter reference with an empty body", func(t *testing.T) {
		dir := t.TempDir()
		path := writeAgentMarkdown(t, dir, "planner.md", `---
description: Plans work
langfusePromptPath: agents/planner/system
langfusePromptLabel: staging
---
`)
		agent, err := parseAgentMarkdown(path)
		if err != nil {
			t.Fatalf("parseAgentMarkdown() error = %v", err)
		}
		if agent.LangfusePromptPath != "agents/planner/system" {
			t.Errorf("LangfusePromptPath = %q", agent.LangfusePromptPath)
		}
		if agent.LangfusePromptLabel != "staging" {
			t.Errorf("LangfusePromptLabel = %q", agent.LangfusePromptLabel)
		}
		if agent.Prompt != "" {
			t.Errorf("Prompt = %q, want empty", agent.Prompt)
		}
	})

	t.Run("the label defaults later, not here", func(t *testing.T) {
		dir := t.TempDir()
		path := writeAgentMarkdown(t, dir, "planner.md", `---
langfusePromptPath: agents/planner/system
---
`)
		agent, err := parseAgentMarkdown(path)
		if err != nil {
			t.Fatalf("parseAgentMarkdown() error = %v", err)
		}
		if agent.LangfusePromptLabel != "" {
			t.Errorf("LangfusePromptLabel = %q, want empty — the prompt client owns the default", agent.LangfusePromptLabel)
		}
	})

	// For a markdown agent the body IS the inline prompt, so a file with
	// both a body and a reference is the mutual-exclusivity error.
	t.Run("a body alongside a reference is rejected", func(t *testing.T) {
		dir := t.TempDir()
		path := writeAgentMarkdown(t, dir, "planner.md", `---
langfusePromptPath: agents/planner/system
---

You are a planner.
`)
		_, err := parseAgentMarkdown(path)
		if err == nil {
			t.Fatal("parseAgentMarkdown() error = nil, want a mutual-exclusivity error")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error = %v, want one naming the conflict", err)
		}
	})

	t.Run("a label without a path is rejected", func(t *testing.T) {
		dir := t.TempDir()
		path := writeAgentMarkdown(t, dir, "planner.md", `---
langfusePromptLabel: staging
---

You are a planner.
`)
		_, err := parseAgentMarkdown(path)
		if err == nil {
			t.Fatal("parseAgentMarkdown() error = nil, want an error")
		}
		if !strings.Contains(err.Error(), "requires langfusePromptPath") {
			t.Errorf("error = %v, want one naming the missing path", err)
		}
	})

	t.Run("an ordinary body-only agent is unaffected", func(t *testing.T) {
		dir := t.TempDir()
		path := writeAgentMarkdown(t, dir, "planner.md", `---
description: Plans work
---

You are a planner.
`)
		agent, err := parseAgentMarkdown(path)
		if err != nil {
			t.Fatalf("parseAgentMarkdown() error = %v", err)
		}
		if agent.Prompt != "You are a planner." {
			t.Errorf("Prompt = %q", agent.Prompt)
		}
		if agent.LangfusePromptPath != "" {
			t.Errorf("LangfusePromptPath = %q, want empty", agent.LangfusePromptPath)
		}
	})
}

// TestPromptSourceOverridesAreExclusive pins that a registry entry never
// ends up holding both an inline prompt and a reference. Whichever source a
// later layer declares replaces the earlier one wholly — otherwise a JSON
// override switching an existing markdown agent to a managed prompt would
// leave the body in place and the reference would look ignored.
func TestPromptSourceOverridesAreExclusive(t *testing.T) {
	t.Run("a config reference clears an inherited inline prompt", func(t *testing.T) {
		agents := map[string]AgentInfo{
			"planner": {ID: "planner", Prompt: "inline from markdown"},
		}
		applyConfigOverrides(agents, &config.Config{
			Agents: map[config.AgentName]config.Agent{
				"planner": {
					LangfusePromptPath:  "agents/planner/system",
					LangfusePromptLabel: "staging",
				},
			},
		})
		got := agents["planner"]
		if got.Prompt != "" {
			t.Errorf("Prompt = %q, want empty — the reference replaces the inline source", got.Prompt)
		}
		if got.LangfusePromptPath != "agents/planner/system" || got.LangfusePromptLabel != "staging" {
			t.Errorf("reference = (%q, %q)", got.LangfusePromptPath, got.LangfusePromptLabel)
		}
	})

	t.Run("a config inline prompt clears an inherited reference", func(t *testing.T) {
		agents := map[string]AgentInfo{
			"planner": {ID: "planner", LangfusePromptPath: "agents/planner/system", LangfusePromptLabel: "staging"},
		}
		applyConfigOverrides(agents, &config.Config{
			Agents: map[config.AgentName]config.Agent{
				"planner": {Prompt: "inline wins"},
			},
		})
		got := agents["planner"]
		if got.Prompt != "inline wins" {
			t.Errorf("Prompt = %q", got.Prompt)
		}
		if got.LangfusePromptPath != "" || got.LangfusePromptLabel != "" {
			t.Errorf("reference = (%q, %q), want both cleared", got.LangfusePromptPath, got.LangfusePromptLabel)
		}
	})

	t.Run("a markdown reference clears an inherited inline prompt", func(t *testing.T) {
		existing := AgentInfo{ID: "planner", Prompt: "built-in prompt"}
		mergeMarkdownIntoExisting(&existing, &AgentInfo{
			LangfusePromptPath: "agents/planner/system",
		})
		if existing.Prompt != "" {
			t.Errorf("Prompt = %q, want empty", existing.Prompt)
		}
		if existing.LangfusePromptPath != "agents/planner/system" {
			t.Errorf("LangfusePromptPath = %q", existing.LangfusePromptPath)
		}
	})

	t.Run("a markdown body clears an inherited reference", func(t *testing.T) {
		existing := AgentInfo{ID: "planner", LangfusePromptPath: "agents/planner/system"}
		mergeMarkdownIntoExisting(&existing, &AgentInfo{Prompt: "body wins"})
		if existing.Prompt != "body wins" {
			t.Errorf("Prompt = %q", existing.Prompt)
		}
		if existing.LangfusePromptPath != "" {
			t.Errorf("LangfusePromptPath = %q, want cleared", existing.LangfusePromptPath)
		}
	})
}
