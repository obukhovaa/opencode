package agent

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

type Output struct {
	Schema map[string]any `json:"schema,omitempty" yaml:"schema,omitempty"`
}

type AgentInfo struct {
	ID              string           `yaml:"-"`
	Name            string           `yaml:"name,omitempty"`
	Description     string           `yaml:"description,omitempty"`
	Mode            config.AgentMode `yaml:"mode,omitempty"`
	Native          bool             `yaml:"native,omitempty"`
	Hidden          bool             `yaml:"hidden,omitempty"`
	Color           string           `yaml:"color,omitempty"`
	Model           string           `yaml:"model,omitempty"`
	MaxTokens       int64            `yaml:"maxTokens,omitempty"`
	ReasoningEffort string           `yaml:"reasoningEffort,omitempty"`
	Prompt          string           `yaml:"-"`
	Permission      map[string]any   `yaml:"permission,omitempty"`
	Tools           map[string]bool  `yaml:"tools,omitempty"`
	Output          *Output          `yaml:"output,omitempty"`
	Location        string           `yaml:"-"`
}

type Registry interface {
	Get(id string) (AgentInfo, bool)
	List() []AgentInfo
	ListByMode(mode config.AgentMode) []AgentInfo
	// Resolves agent specific permission action for a given tool
	EvaluatePermission(agentID, toolName, input string) permission.Action
	IsToolEnabled(agentID, toolName string) bool
	GlobalPermissions() map[string]any
}

type registry struct {
	agents      map[string]AgentInfo
	globalPerms map[string]any
}

var (
	registryInstance Registry
	registryOnce     sync.Once
)

func GetRegistry() Registry {
	registryOnce.Do(func() {
		registryInstance = newRegistry()
	})
	return registryInstance
}

func InvalidateRegistry() {
	registryOnce = sync.Once{}
	registryInstance = nil
}

func newRegistry() Registry {
	cfg := config.Get()
	agents := make(map[string]AgentInfo)

	registerBuiltins(agents, cfg)
	discoverMarkdownAgents(agents, cfg)
	applyConfigOverrides(agents, cfg)

	globalPerms := buildGlobalPerms(cfg)

	for _, a := range agents {
		path := "default"
		if a.Location != "" {
			path = a.Location
		}
		var tools any
		if len(a.Tools) == 0 {
			tools = "default"
		} else {
			tools = a.Tools
		}
		var permissions any
		if len(a.Permission) == 0 {
			permissions = "default"
		} else {
			permissions = a.Permission
		}
		logging.Info("Agent discovered", "agentID", a.ID, "mode", a.Mode, "model", a.Model, "path", path, "tools", tools, "permissions", permissions)
	}
	return &registry{
		agents:      agents,
		globalPerms: globalPerms,
	}
}

func (r *registry) Get(id string) (AgentInfo, bool) {
	a, ok := r.agents[id]
	return a, ok
}

func (r *registry) List() []AgentInfo {
	result := make([]AgentInfo, 0, len(r.agents))
	for _, a := range r.agents {
		result = append(result, a)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Mode != result[j].Mode {
			return result[i].Mode == config.AgentModeAgent
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (r *registry) ListByMode(mode config.AgentMode) []AgentInfo {
	var result []AgentInfo
	for _, a := range r.agents {
		if a.Mode == mode && !a.Hidden {
			result = append(result, a)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

func (r *registry) EvaluatePermission(agentID, toolName, input string) permission.Action {
	a, ok := r.agents[agentID]
	if !ok {
		return permission.EvaluateToolPermission(toolName, input, nil, r.globalPerms)
	}

	if !permission.IsToolEnabled(toolName, a.Tools) {
		return permission.ActionDeny
	}

	return permission.EvaluateToolPermission(toolName, input, a.Permission, r.globalPerms)
}

func (r *registry) IsToolEnabled(agentID, toolName string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return true
	}
	return permission.IsToolEnabled(toolName, a.Tools)
}

func (r *registry) GlobalPermissions() map[string]any {
	return r.globalPerms
}

func registerBuiltins(agents map[string]AgentInfo, cfg *config.Config) {
	builtins := []AgentInfo{
		{
			ID:          config.AgentCoder,
			Name:        "Coder Agent",
			Description: "The default coding agent. Has full access to all tools for development work.",
			Mode:        config.AgentModeAgent,
			Native:      true,
		},
		{
			ID:          config.AgentHivemind,
			Name:        "Hivemind Agent",
			Description: "Supervisory agent for coordinating work across different subagents to achieve complex goals.",
			Mode:        config.AgentModeAgent,
			Native:      true,
			Tools: map[string]bool{
				"bash":      false,
				"edit":      false,
				"multiedit": false,
				"write":     false,
				"delete":    false,
				"patch":     false,
				"lsp":       false,
			},
		},
		{
			ID:          config.AgentExplorer,
			Name:        "Explorer Agent",
			Description: "Fast agent specialized for exploring codebases. Use this when you need to quickly find files by patterns, search code for keywords, or answer questions about the codebase. Do not use it to run bash commands. When calling this agent, specify the desired thoroughness level: \"quick\" for basic searches, \"medium\" for moderate exploration, or \"very thorough\" for comprehensive analysis.",
			Mode:        config.AgentModeSubagent,
			Native:      true,
			Tools: map[string]bool{
				"bash":      false,
				"edit":      false,
				"multiedit": false,
				"write":     false,
				"delete":    false,
				"patch":     false,
				"task":      false,
			},
		},
		{
			ID:          config.AgentWorkhorse,
			Name:        "Workhorse Agent",
			Description: "Autonomous coding agent that receives a task and works until completion. Has full tool access like the coder agent, including bash commands. Use this for tasks that require writing or modifying code.",
			Mode:        config.AgentModeSubagent,
			Native:      true,
			Tools: map[string]bool{
				"task": false,
			},
		},
		{
			ID:          config.AgentSummarizer,
			Name:        "Summarizer Agent",
			Description: "Summarizes conversation history for context compaction.",
			Mode:        config.AgentModeSubagent,
			Native:      true,
			Hidden:      true,
			Tools: map[string]bool{
				"*": false,
			},
		},
		{
			ID:          config.AgentDescriptor,
			Name:        "Descriptor Agent",
			Description: "Generates short session titles.",
			Mode:        config.AgentModeSubagent,
			Native:      true,
			Hidden:      true,
			Tools: map[string]bool{
				"*": false,
			},
		},
	}

	for _, b := range builtins {
		if agentCfg, ok := cfg.Agents[b.ID]; ok {
			b.Model = string(agentCfg.Model)
			b.MaxTokens = agentCfg.MaxTokens
			b.ReasoningEffort = agentCfg.ReasoningEffort
		}
		agents[b.ID] = b
	}
}

func discoverMarkdownAgents(agents map[string]AgentInfo, cfg *config.Config) {
	globalAgents := discoverGlobalMarkdownAgents()
	for _, a := range globalAgents {
		if existing, ok := agents[a.ID]; ok && existing.Native {
			mergeMarkdownIntoExisting(&existing, &a)
			agents[a.ID] = existing
		} else {
			agents[a.ID] = a
		}
	}

	projectAgents := discoverProjectMarkdownAgents(cfg.WorkingDir)
	for _, a := range projectAgents {
		if existing, ok := agents[a.ID]; ok {
			mergeMarkdownIntoExisting(&existing, &a)
			agents[a.ID] = existing
		} else {
			agents[a.ID] = a
		}
	}
}

func applyConfigOverrides(agents map[string]AgentInfo, cfg *config.Config) {
	if cfg.Agents == nil {
		return
	}
	for name, agentCfg := range cfg.Agents {
		existing, ok := agents[name]
		if !ok {
			existing = AgentInfo{
				ID:   name,
				Mode: config.AgentModeSubagent,
			}
		}

		if agentCfg.Model != "" {
			existing.Model = string(agentCfg.Model)
		}
		if agentCfg.MaxTokens > 0 {
			existing.MaxTokens = agentCfg.MaxTokens
		}
		if agentCfg.ReasoningEffort != "" {
			existing.ReasoningEffort = agentCfg.ReasoningEffort
		}
		if agentCfg.Name != "" {
			existing.Name = agentCfg.Name
		}
		if agentCfg.Description != "" {
			existing.Description = agentCfg.Description
		}
		if agentCfg.Mode != "" {
			existing.Mode = agentCfg.Mode
		}
		if agentCfg.Color != "" {
			existing.Color = agentCfg.Color
		}
		if agentCfg.Prompt != "" {
			existing.Prompt = agentCfg.Prompt
		}
		if agentCfg.Hidden {
			existing.Hidden = true
		}
		if agentCfg.Permission != nil {
			existing.Permission = mergePermissions(existing.Permission, agentCfg.Permission)
		}
		if agentCfg.Tools != nil {
			if existing.Tools == nil {
				existing.Tools = make(map[string]bool)
			}
			maps.Copy(existing.Tools, agentCfg.Tools)
		}
		if agentCfg.Output != nil && agentCfg.Output.Schema != nil {
			if existing.Output == nil {
				existing.Output = &Output{}
			}
			existing.Output.Schema = agentCfg.Output.Schema
		}

		agents[name] = existing
	}
}

func mergePermissions(base, overlay map[string]any) map[string]any {
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}
	merged := make(map[string]any, len(base))
	maps.Copy(merged, base)
	maps.Copy(merged, overlay)
	return merged
}

func mergeMarkdownIntoExisting(existing, md *AgentInfo) {
	if md.Name != "" {
		existing.Name = md.Name
	}
	if md.Description != "" {
		existing.Description = md.Description
	}
	if md.Mode != "" {
		existing.Mode = md.Mode
	}
	if md.Color != "" {
		existing.Color = md.Color
	}
	if md.Model != "" {
		existing.Model = md.Model
	}
	if md.Prompt != "" {
		existing.Prompt = md.Prompt
	}
	if md.Permission != nil {
		existing.Permission = mergePermissions(existing.Permission, md.Permission)
	}
	if md.Tools != nil {
		if existing.Tools == nil {
			existing.Tools = make(map[string]bool)
		}
		maps.Copy(existing.Tools, md.Tools)
	}
	if md.Output != nil && md.Output.Schema != nil {
		if existing.Output == nil {
			existing.Output = &Output{}
		}
		existing.Output.Schema = md.Output.Schema
	}
	if md.Hidden {
		existing.Hidden = true
	}
	existing.Location = md.Location
}

func buildGlobalPerms(cfg *config.Config) map[string]any {
	perms := make(map[string]any)
	if cfg.Permission != nil {
		if cfg.Permission.Skill != nil {
			perms["skill"] = cfg.Permission.Skill
		}
		maps.Copy(perms, cfg.Permission.Rules)
	}
	return perms
}

func discoverGlobalMarkdownAgents() []AgentInfo {
	var agents []AgentInfo

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return agents
	}

	globalDirs := []string{
		filepath.Join(homeDir, ".config", "opencode", "agents"),
		filepath.Join(homeDir, ".agents", "types"),
	}

	for _, dir := range globalDirs {
		found := scanAgentDirectory(dir)
		agents = append(agents, found...)
	}
	return agents
}

func discoverProjectMarkdownAgents(workingDir string) []AgentInfo {
	var agents []AgentInfo
	if workingDir == "" {
		return agents
	}

	projectDirs := []string{
		filepath.Join(workingDir, ".opencode", "agents"),
		filepath.Join(workingDir, ".agents", "types"),
	}

	for _, dir := range projectDirs {
		found := scanAgentDirectory(dir)
		agents = append(agents, found...)
	}
	return agents
}

func scanAgentDirectory(dir string) []AgentInfo {
	var agents []AgentInfo

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return agents
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		logging.Warn("Failed to read agent directory", "dir", dir, "error", err)
		return agents
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		fullPath := filepath.Join(dir, name)
		agent, err := parseAgentMarkdown(fullPath)
		if err != nil {
			logging.Warn("Failed to parse agent markdown", "path", fullPath, "error", err)
			continue
		}
		agents = append(agents, *agent)
	}
	return agents
}

func parseAgentMarkdown(path string) (*AgentInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent file: %w", err)
	}

	content := string(data)

	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return nil, fmt.Errorf("missing frontmatter delimiter in %s", path)
	}

	lines := strings.Split(content, "\n")
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return nil, fmt.Errorf("missing frontmatter end delimiter in %s", path)
	}

	frontmatter := strings.Join(lines[1:endIdx], "\n")
	body := ""
	if endIdx+1 < len(lines) {
		body = strings.TrimSpace(strings.Join(lines[endIdx+1:], "\n"))
	}

	var agent AgentInfo
	if err := yaml.Unmarshal([]byte(frontmatter), &agent); err != nil {
		return nil, fmt.Errorf("invalid YAML frontmatter in %s: %w", path, err)
	}

	baseName := strings.TrimSuffix(filepath.Base(path), ".md")
	agent.ID = baseName
	if agent.Name == "" {
		agent.Name = baseName
	}
	agent.Prompt = body
	agent.Location = path

	if agent.Mode == "" {
		agent.Mode = config.AgentModeSubagent
	}

	return &agent, nil
}
