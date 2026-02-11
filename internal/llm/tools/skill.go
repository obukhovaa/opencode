package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
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
	registry    agentregistry.Registry
}

// NewSkillTool creates a new skill tool instance.
func NewSkillTool(permissions permission.Service, reg agentregistry.Registry) BaseTool {
	return &skillTool{
		permissions: permissions,
		registry:    reg,
	}
}

func (s *skillTool) Info() ToolInfo {
	return ToolInfo{
		Name:        SkillToolName,
		Description: s.buildSkillDescription(),
		Parameters: map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": s.buildSkillParameterDescription(),
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
	if !s.checkPermission(sessionID, string(agentName), params.Name, skillInfo.Description) {
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
func (s *skillTool) checkPermission(sessionID string, agentName string, skillName, description string) bool {
	action := s.registry.EvaluatePermission(agentName, SkillToolName, skillName)

	switch action {
	case permission.ActionAllow:
		return true
	case permission.ActionDeny:
		return false
	default:
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

func (s *skillTool) filterSkillsByPermission(skills []skill.Info) []skill.Info {
	filtered := make([]skill.Info, 0, len(skills))
	for _, sk := range skills {
		action := s.registry.EvaluatePermission("", SkillToolName, sk.Name)
		if action != permission.ActionDeny {
			filtered = append(filtered, sk)
		}
	}
	return filtered
}

func (s *skillTool) buildSkillDescription() string {
	skills := skill.All()
	accessibleSkills := s.filterSkillsByPermission(skills)

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

func (s *skillTool) buildSkillParameterDescription() string {
	skills := skill.All()
	accessibleSkills := s.filterSkillsByPermission(skills)

	if len(accessibleSkills) == 0 {
		return "The skill identifier from available_skills"
	}

	examples := make([]string, 0, 3)
	for i := 0; i < len(accessibleSkills) && i < 3; i++ {
		examples = append(examples, fmt.Sprintf("'%s'", accessibleSkills[i].Name))
	}

	if len(examples) > 0 {
		return fmt.Sprintf("The skill identifier from available_skills (e.g., %s, ...)", strings.Join(examples, ", "))
	}

	return "The skill identifier from available_skills"
}
