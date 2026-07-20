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

	"github.com/opencode-ai/opencode/internal/bridge"
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
	Disabled        bool             `yaml:"disabled,omitempty"`
	Color           string           `yaml:"color,omitempty"`
	Model           string           `yaml:"model,omitempty"`
	MaxTokens       int64            `yaml:"maxTokens,omitempty"`
	MaxTurns        int              `yaml:"maxTurns,omitempty"`
	ReasoningEffort string           `yaml:"reasoningEffort,omitempty"`
	TaskBudget      int64            `yaml:"taskBudget,omitempty"`
	Prompt          string           `yaml:"-"`
	Skills          []string         `yaml:"skills,omitempty"`
	Permission      map[string]any   `yaml:"permission,omitempty"`
	Tools           map[string]bool  `yaml:"tools,omitempty"`
	Output          *Output          `yaml:"output,omitempty"`
	Location        string           `yaml:"-"`
	ParallelToolUse *bool            `yaml:"parallelToolUse,omitempty"`
	// Interactive is set in-memory by AgentFactory.NewAgent when the
	// agent is being constructed for a flow step with `interactive: true`.
	// NOT persisted via YAML — agent-level interactiveness is derived
	// from the flow step, not from the agent definition.
	//
	// When true, GetAgentPrompt replaces the terse "use struct_output
	// for your final response" prompt with one that encourages
	// multi-turn dialogue via the chat bridge first and reserves
	// struct_output for the END of the conversation. The agent
	// effectively becomes a human-in-the-loop collaborator instead of
	// a one-shot answerer.
	Interactive bool `yaml:"-"`
	// BoundPeers is the resolved chat-bridge peer list for the
	// current interactive flow step. Populated by AgentFactory.NewAgent
	// for `interactive: true` steps; nil otherwise. NOT persisted via
	// YAML — derived per-step from interaction.target. Plumbs into
	// GetAgentPromptWithOptions so the "## Reviewer details" section
	// of the interactive prompt knows the mention handle / channel /
	// peerId without flow authors having to template ${args.reviewer.*}
	// into the YAML prompt.
	BoundPeers []bridge.PeerRef `yaml:"-"`
}

type Registry interface {
	Get(id string) (AgentInfo, bool)
	List() []AgentInfo
	ListByMode(mode config.AgentMode) []AgentInfo
	// Resolves agent specific permission action for a given tool
	EvaluatePermission(agentID, toolName, input string) permission.Action
	// EvaluateReadPermission resolves permission for read-category tools
	// (read, grep, glob, ls). Falls back from specific tool → "read" → "*" → allow.
	EvaluateReadPermission(agentID, toolName, input string) permission.Action
	// ReadDenyPatterns returns file patterns with "deny" action from the
	// read-category permission chain for the given agent and tool.
	ReadDenyPatterns(agentID, toolName string) []string
	IsToolEnabled(agentID, toolName string) bool
	// IsToolExplicitlyEnabled returns true only when the agent's tools map
	// names this tool with an explicit `true` value (or a matching wildcard
	// set to true). Used by tools that are default-deny — the registry's
	// regular IsToolEnabled returns true for unmentioned tools, which is
	// wrong for tools like cron that should require opt-in.
	IsToolExplicitlyEnabled(agentID, toolName string) bool
	HasTools(agentID string) bool
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
	removeDisabledAgents(agents)

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
		args := []any{"agentID", a.ID, "mode", a.Mode, "model", a.Model, "path", path, "tools", tools, "permissions", permissions}
		if len(a.Skills) > 0 {
			args = append(args, "skills", a.Skills)
		}
		if a.TaskBudget > 0 {
			args = append(args, "taskBudget", a.TaskBudget)
		}
		logging.Info("Agent discovered", args...)
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

func (r *registry) EvaluateReadPermission(agentID, toolName, input string) permission.Action {
	a, ok := r.agents[agentID]
	if !ok {
		return permission.EvaluateReadToolPermission(toolName, input, nil, r.globalPerms)
	}

	if !permission.IsToolEnabled(toolName, a.Tools) {
		return permission.ActionDeny
	}

	return permission.EvaluateReadToolPermission(toolName, input, a.Permission, r.globalPerms)
}

func (r *registry) ReadDenyPatterns(agentID, toolName string) []string {
	a, ok := r.agents[agentID]
	if !ok {
		return permission.ReadDenyPatterns(toolName, nil, r.globalPerms)
	}
	return permission.ReadDenyPatterns(toolName, a.Permission, r.globalPerms)
}

func (r *registry) IsToolEnabled(agentID, toolName string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return true
	}
	return permission.IsToolEnabled(toolName, a.Tools)
}

// IsToolExplicitlyEnabled returns true only when the agent's tools map
// contains an explicit entry for this tool that is set to true (either by
// exact match or by a matching wildcard). Unlike IsToolEnabled it returns
// false for tools the agent never mentions — used for default-deny tools
// (e.g. cron) that must require opt-in.
func (r *registry) IsToolExplicitlyEnabled(agentID, toolName string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return false
	}
	if enabled, ok := a.Tools[toolName]; ok {
		return enabled
	}
	for pattern, enabled := range a.Tools {
		if pattern == "*" {
			continue // wildcard "*" doesn't count as explicit opt-in
		}
		if permission.MatchWildcard(pattern, toolName) {
			return enabled
		}
	}
	return false
}

func (r *registry) HasTools(agentID string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return true
	}
	if v, exists := a.Tools["*"]; exists && !v {
		return false
	}
	return true
}

func (r *registry) GlobalPermissions() map[string]any {
	return r.globalPerms
}

func (info *AgentInfo) AllowsParallelToolUse() bool {
	if info.ParallelToolUse == nil {
		return true
	}
	return *info.ParallelToolUse
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
				// Cron tools are default-deny across the fleet (see
				// IsToolExplicitlyEnabled). Hivemind opts in here so the
				// coordinator can schedule recurring tasks out of the box.
				"croncreate": true,
				"crondelete": true,
				"cronlist":   true,
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
				"task":      false,
				"websearch": false,
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
			b.TaskBudget = agentCfg.TaskBudget
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

	// Custom agent paths have the lowest precedence among discovery
	// sources (mirroring skills' custom-path handling): they contribute
	// only agents whose ID isn't already provided by a builtin, global,
	// or project source. Config overrides (applyConfigOverrides) still
	// win over everything, since they run after this.
	customAgents := discoverCustomPathMarkdownAgents(cfg)
	for _, a := range customAgents {
		if _, ok := agents[a.ID]; ok {
			continue
		}
		agents[a.ID] = a
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
		if agentCfg.MaxTurns > 0 {
			existing.MaxTurns = agentCfg.MaxTurns
		}
		if agentCfg.ReasoningEffort != "" {
			existing.ReasoningEffort = agentCfg.ReasoningEffort
		}
		if agentCfg.TaskBudget > 0 {
			existing.TaskBudget = agentCfg.TaskBudget
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
		if agentCfg.Disabled {
			existing.Disabled = true
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
		if agentCfg.ParallelToolUse != nil {
			existing.ParallelToolUse = agentCfg.ParallelToolUse
		}
		if agentCfg.Skills != nil {
			existing.Skills = deduplicateSkills(agentCfg.Skills, name)
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
	if md.MaxTurns > 0 {
		existing.MaxTurns = md.MaxTurns
	}
	if md.TaskBudget > 0 {
		existing.TaskBudget = md.TaskBudget
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
	if md.Disabled {
		existing.Disabled = true
	}
	if md.ParallelToolUse != nil {
		existing.ParallelToolUse = md.ParallelToolUse
	}
	if md.Skills != nil {
		existing.Skills = deduplicateSkills(md.Skills, existing.ID)
	}
	existing.Location = md.Location
}

// deduplicateSkills returns a new slice with duplicate skill names removed,
// preserving the order of first occurrence. Logs a warning for each duplicate found.
func deduplicateSkills(skills []string, agentID string) []string {
	if len(skills) == 0 {
		return skills
	}
	seen := make(map[string]bool, len(skills))
	result := make([]string, 0, len(skills))
	for _, name := range skills {
		if seen[name] {
			logging.Warn("Duplicate skill in agent definition, ignoring", "agentID", agentID, "skill", name)
			continue
		}
		seen[name] = true
		result = append(result, name)
	}
	return result
}

func removeDisabledAgents(agents map[string]AgentInfo) {
	for id, a := range agents {
		if a.Disabled {
			logging.Info("Agent disabled, removing from registry", "agentID", id)
			delete(agents, id)
		}
	}
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

// discoverCustomPathMarkdownAgents scans the directories listed in
// cfg.AgentPaths for markdown agent definitions. It mirrors the skills
// package's discoverCustomPaths: "~/" is expanded to the home directory and
// relative paths are resolved against the working directory. Missing paths and
// non-directories are logged and skipped rather than failing discovery.
func discoverCustomPathMarkdownAgents(cfg *config.Config) []AgentInfo {
	if cfg == nil || len(cfg.AgentPaths) == 0 {
		return nil
	}

	var agents []AgentInfo
	homeDir, _ := os.UserHomeDir()

	for _, agentPath := range cfg.AgentPaths {
		// Expand ~ to the home directory.
		expanded := agentPath
		if strings.HasPrefix(agentPath, "~/") && homeDir != "" {
			expanded = filepath.Join(homeDir, agentPath[2:])
		}

		// Resolve relative paths against the working directory.
		resolved := expanded
		if !filepath.IsAbs(expanded) {
			resolved = filepath.Join(cfg.WorkingDir, expanded)
		}

		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			logging.Warn("Agent path not found or not a directory", "path", resolved)
			continue
		}

		agents = append(agents, scanAgentDirectory(resolved)...)
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

	// For compatibility with upstream opencode
	if agent.Mode == "primary" {
		agent.Mode = config.AgentModeAgent
	}

	if agent.Mode == "" {
		agent.Mode = config.AgentModeSubagent
	}

	return &agent, nil
}
