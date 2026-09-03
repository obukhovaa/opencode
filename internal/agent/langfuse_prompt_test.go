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

	// A label with no path of its own is NOT a parse error: this layer may
	// be re-labelling a path a lower-priority layer declared, which is the
	// normal way to point one environment at a `staging` prompt. Whether
	// the label is orphaned is only knowable once every layer has merged,
	// so normalisePromptSources judges it — see
	// TestNormalisePromptSources.
	t.Run("a label without a path parses, to be judged after merging", func(t *testing.T) {
		dir := t.TempDir()
		path := writeAgentMarkdown(t, dir, "planner.md", `---
langfusePromptLabel: staging
---

You are a planner.
`)
		got, err := parseAgentMarkdown(path)
		if err != nil {
			t.Fatalf("parseAgentMarkdown() error = %v, want nil", err)
		}
		if got.LangfusePromptLabel != "staging" {
			t.Errorf("LangfusePromptLabel = %q, want %q", got.LangfusePromptLabel, "staging")
		}
		if got.Prompt == "" {
			t.Error("Prompt is empty — the body is still the inline prompt here")
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

// TestNormalisePromptSources covers the post-merge cleanup that judges a
// prompt source once every definition layer has contributed.
func TestNormalisePromptSources(t *testing.T) {
	t.Run("a whitespace-only path is trimmed away", func(t *testing.T) {
		// ValidateAgentPromptSource compares the path trimmed, so "  "
		// validates as "no reference" — but every consumer checks it
		// untrimmed and would send "  " to Langfuse, failing agent
		// construction over a key validation had already discounted.
		agents := map[string]AgentInfo{
			"planner": {ID: "planner", Prompt: "inline", LangfusePromptPath: "   "},
		}
		normalisePromptSources(agents)
		if got := agents["planner"].LangfusePromptPath; got != "" {
			t.Errorf("LangfusePromptPath = %q, want empty", got)
		}
		if got := agents["planner"].Prompt; got != "inline" {
			t.Errorf("Prompt = %q, want it untouched", got)
		}
	})

	t.Run("surrounding whitespace is trimmed off a real path", func(t *testing.T) {
		agents := map[string]AgentInfo{
			"planner": {ID: "planner", LangfusePromptPath: "  agents/planner/system\n"},
		}
		normalisePromptSources(agents)
		if got := agents["planner"].LangfusePromptPath; got != "agents/planner/system" {
			t.Errorf("LangfusePromptPath = %q, want it trimmed", got)
		}
	})

	t.Run("an orphaned label is dropped, not fatal", func(t *testing.T) {
		// A label with no path selects a version of nothing. It is legal
		// per layer (that is what makes a label-only override possible), so
		// it can only be judged here — and ignoring it loudly beats
		// refusing to boot over a stray key on an otherwise fine agent.
		agents := map[string]AgentInfo{
			"planner": {ID: "planner", Prompt: "inline", LangfusePromptLabel: "staging"},
		}
		normalisePromptSources(agents)
		if got := agents["planner"].LangfusePromptLabel; got != "" {
			t.Errorf("LangfusePromptLabel = %q, want it dropped", got)
		}
	})

	t.Run("a label with a path is left alone", func(t *testing.T) {
		agents := map[string]AgentInfo{
			"planner": {ID: "planner", LangfusePromptPath: "agents/planner/system", LangfusePromptLabel: "staging"},
		}
		normalisePromptSources(agents)
		got := agents["planner"]
		if got.LangfusePromptPath != "agents/planner/system" || got.LangfusePromptLabel != "staging" {
			t.Errorf("got path=%q label=%q, want both preserved", got.LangfusePromptPath, got.LangfusePromptLabel)
		}
	})
}

// TestApplyConfigOverrides_LabelOnlyOverride pins that a JSON entry can
// re-label a path another layer declared.
//
// Every other agent key is a partial override; the prompt source used to be
// the sole exception — a label-only JSON entry failed config validation with
// "langfusePromptLabel requires langfusePromptPath", contradicting what the
// user wrote, and would have been dropped by the merge even if it had passed.
func TestApplyConfigOverrides_LabelOnlyOverride(t *testing.T) {
	agents := map[string]AgentInfo{
		"planner": {
			ID:                  "planner",
			LangfusePromptPath:  "agents/planner/system",
			LangfusePromptLabel: "production",
		},
	}
	cfg := &config.Config{
		Agents: map[config.AgentName]config.Agent{
			"planner": {LangfusePromptLabel: "staging"},
		},
	}
	applyConfigOverrides(agents, cfg)
	normalisePromptSources(agents)

	got := agents["planner"]
	if got.LangfusePromptPath != "agents/planner/system" {
		t.Errorf("LangfusePromptPath = %q, want the inherited path kept", got.LangfusePromptPath)
	}
	if got.LangfusePromptLabel != "staging" {
		t.Errorf("LangfusePromptLabel = %q, want the override applied", got.LangfusePromptLabel)
	}
	if got.Prompt != "" {
		t.Errorf("Prompt = %q, want empty — a label-only override must not resurrect an inline prompt", got.Prompt)
	}
}
