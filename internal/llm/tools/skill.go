package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/skill"
)

const (
	SkillToolName = "skill"
)

type SkillParams struct {
	Name string `json:"name"`
}

type skillTool struct {
	permissions permission.Service
}

// NewSkillTool creates a new skill tool instance.
func NewSkillTool(permissions permission.Service) BaseTool {
	return &skillTool{
		permissions: permissions,
	}
}

func (s *skillTool) Info() ToolInfo {
	return ToolInfo{
		Name:        SkillToolName,
		Description: buildSkillDescription(),
		Parameters: map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": buildSkillParameterDescription(),
			},
		},
		Required: []string{"name"},
	}
}

func (s *skillTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params SkillParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.Name == "" {
		return NewTextErrorResponse("skill name is required"), nil
	}

	// Get the skill
	skillInfo, err := skill.Get(params.Name)
	if err != nil {
		available := skill.All()
		availableNames := make([]string, 0, len(available))
		for _, s := range available {
			availableNames = append(availableNames, s.Name)
		}

		if len(availableNames) == 0 {
			return NewTextErrorResponse(fmt.Sprintf("Skill %q not found. No skills are currently available.", params.Name)), nil
		}

		return NewTextErrorResponse(fmt.Sprintf("Skill %q not found. Available skills: %s", params.Name, strings.Join(availableNames, ", "))), nil
	}

	// Check permissions
	sessionID, _ := GetContextValues(ctx)
	agentName := GetAgentName(ctx)
	if !s.checkPermission(ctx, sessionID, agentName, params.Name, skillInfo.Description) {
		return NewTextErrorResponse(fmt.Sprintf("Permission denied for skill %q", params.Name)), nil
	}

	// Format output
	baseDir := filepath.Dir(skillInfo.Location)
	output := fmt.Sprintf("## Skill: %s\n\n**Base directory**: %s\n\n%s",
		skillInfo.Name,
		baseDir,
		skillInfo.Content,
	)

	metadata := map[string]string{
		"name": skillInfo.Name,
		"dir":  baseDir,
	}

	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

// checkPermission checks if the skill can be loaded based on permissions.
func (s *skillTool) checkPermission(ctx context.Context, sessionID string, agentName config.AgentName, skillName, description string) bool {
	// Check global and agent-specific permissions
	cfg := config.Get()

	// Get permission action for this skill
	action := evaluateSkillPermission(skillName, agentName, cfg)

	switch action {
	case "allow":
		return true
	case "deny":
		return false
	case "ask":
		// Request permission from user
		return s.permissions.Request(permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    SkillToolName,
			Description: fmt.Sprintf("Load skill: %s - %s", skillName, description),
			Action:      "load",
			Params:      map[string]string{"skill": skillName},
			Path:        ".",
		})
	default:
		// Default to ask if no permission configured
		return s.permissions.Request(permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    SkillToolName,
			Description: fmt.Sprintf("Load skill: %s - %s", skillName, description),
			Action:      "load",
			Params:      map[string]string{"skill": skillName},
			Path:        ".",
		})
	}
}

// evaluateSkillPermission evaluates permission for a skill based on config and agent.
func evaluateSkillPermission(skillName string, agentName config.AgentName, cfg *config.Config) string {
	// Check agent-specific permissions first (higher priority)
	if agentName != "" && cfg.Agents != nil {
		if agentCfg, ok := cfg.Agents[agentName]; ok {
			// Check if skill tool is disabled for this agent
			if agentCfg.Tools != nil {
				if enabled, ok := agentCfg.Tools["skill"]; ok && !enabled {
					return "deny"
				}
			}

			// Check agent-specific skill permissions
			if agentCfg.Permission != nil {
				if skillPerms, ok := agentCfg.Permission["skill"]; ok {
					if action := matchPermissionPattern(skillName, skillPerms); action != "" {
						return action
					}
				}
			}
		}
	}

	// Check global permissions
	if cfg.Permission != nil && cfg.Permission.Skill != nil {
		if action := matchPermissionPattern(skillName, cfg.Permission.Skill); action != "" {
			return action
		}
	}

	// Default to "ask" if no permission configured
	return "ask"
}

// matchPermissionPattern matches a skill name against permission patterns.
func matchPermissionPattern(skillName string, patterns map[string]string) string {
	// Check for exact match first
	if action, ok := patterns[skillName]; ok {
		return action
	}

	// Check for wildcard patterns (excluding global "*")
	for pattern, action := range patterns {
		if pattern != "*" && matchWildcard(pattern, skillName) {
			return action
		}
	}

	// Check for global wildcard last
	if action, ok := patterns["*"]; ok {
		return action
	}

	return ""
}

// matchWildcard matches a string against a wildcard pattern.
// Supports * as wildcard (e.g., "internal-*" matches "internal-docs", "internal-tools").
func matchWildcard(pattern, str string) bool {
	if pattern == "*" {
		return true
	}

	if !strings.Contains(pattern, "*") {
		return pattern == str
	}

	// Simple wildcard matching
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		prefix := parts[0]
		suffix := parts[1]

		if prefix != "" && !strings.HasPrefix(str, prefix) {
			return false
		}
		if suffix != "" && !strings.HasSuffix(str, suffix) {
			return false
		}
		return true
	}

	return false
}

// buildSkillDescription builds the dynamic tool description with available skills.
func buildSkillDescription() string {
	skills := skill.All()

	// Filter skills by global permissions (agent-specific filtering happens at runtime)
	accessibleSkills := filterSkillsByPermission(skills)

	if len(accessibleSkills) == 0 {
		return "Load a skill to get detailed instructions for a specific task. No skills are currently available."
	}

	var sb strings.Builder
	sb.WriteString("Load a skill to get detailed instructions for a specific task. ")
	sb.WriteString("Skills provide specialized knowledge and step-by-step guidance. ")
	sb.WriteString("Use this when a task matches an available skill's description. ")
	sb.WriteString("Only the skills listed here are available:\n")
	sb.WriteString("<available_skills>\n")

	for _, s := range accessibleSkills {
		fmt.Fprintf(&sb, "  <skill>\n")
		fmt.Fprintf(&sb, "    <name>%s</name>\n", s.Name)
		fmt.Fprintf(&sb, "    <description>%s</description>\n", s.Description)
		fmt.Fprintf(&sb, "  </skill>\n")
	}

	sb.WriteString("</available_skills>")

	return sb.String()
}

// buildSkillParameterDescription builds the parameter description with examples.
func buildSkillParameterDescription() string {
	skills := skill.All()
	accessibleSkills := filterSkillsByPermission(skills)

	if len(accessibleSkills) == 0 {
		return "The skill identifier from available_skills"
	}

	// Get up to 3 examples
	examples := make([]string, 0, 3)
	for i := 0; i < len(accessibleSkills) && i < 3; i++ {
		examples = append(examples, fmt.Sprintf("'%s'", accessibleSkills[i].Name))
	}

	if len(examples) > 0 {
		return fmt.Sprintf("The skill identifier from available_skills (e.g., %s, ...)", strings.Join(examples, ", "))
	}

	return "The skill identifier from available_skills"
}

// filterSkillsByPermission filters skills based on global permissions.
// Note: Agent-specific filtering happens at runtime when the tool is executed.
func filterSkillsByPermission(skills []skill.Info) []skill.Info {
	cfg := config.Get()

	// If no permission config, show all skills
	if cfg.Permission == nil || cfg.Permission.Skill == nil {
		return skills
	}

	filtered := make([]skill.Info, 0, len(skills))
	for _, s := range skills {
		// Use empty agent name to check only global permissions
		action := evaluateSkillPermission(s.Name, "", cfg)
		// Only hide skills that are explicitly denied globally
		if action != "deny" {
			filtered = append(filtered, s)
		}
	}

	return filtered
}
