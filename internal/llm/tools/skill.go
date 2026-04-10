package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/skill"
)

const (
	SkillToolName        = "skill"
	skillFileSampleLimit = 10
)

type SkillParams struct {
	Name string `json:"name"`
	Args string `json:"args"`
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
			"args": map[string]any{
				"type":        "string",
				"description": "Optional arguments to pass to the skill. Substituted into $ARGUMENTS, $ARGUMENTS[N], $0, $1, etc. in the skill content. Shell markup !`command` in the skill is expanded after substitution.",
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

	sessionID, _ := GetContextValues(ctx)
	agentName := GetAgentID(ctx)
	if !s.checkPermission(ctx, sessionID, string(agentName), params.Name, skillInfo.Description) {
		return NewTextErrorResponse(fmt.Sprintf("Permission denied for skill %q", params.Name)), nil
	}

	baseDir := filepath.Dir(skillInfo.Location)
	files := sampleSkillFiles(baseDir, skillFileSampleLimit)

	// Apply argument substitution and shell markup expansion
	processedContent := skill.SubstituteContent(strings.TrimSpace(skillInfo.Content), skill.SubstituteParams{
		Args:      params.Args,
		SkillDir:  baseDir,
		SessionID: sessionID,
	})
	processedContent = shell.ExpandMarkup(ctx, processedContent, config.WorkingDirectory())

	var sb strings.Builder
	fmt.Fprintf(&sb, "<skill_content name=%q>\n", skillInfo.Name)
	fmt.Fprintf(&sb, "Base directory for this skill: %s\n\n", baseDir)
	sb.WriteString(processedContent)
	if len(files) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString("Bundled files (sampled):\n")
		sb.WriteString("<skill_files>\n")
		for _, f := range files {
			fmt.Fprintf(&sb, "<file>%s</file>\n", f)
		}
		sb.WriteString("</skill_files>\n")
	}
	sb.WriteString("</skill_content>")

	metadata := map[string]string{
		"name": skillInfo.Name,
		"dir":  baseDir,
	}
	return WithResponseMetadata(NewTextResponse(sb.String()), metadata), nil
}

func (s *skillTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (s *skillTool) IsBaseline() bool { return true }

// sampleSkillFiles lists up to limit files in the skill directory, excluding SKILL.md.
func sampleSkillFiles(dir string, limit int) []string {
	if files, err := sampleSkillFilesWithRipgrep(dir, limit); err == nil {
		return files
	}

	var files []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}

	for _, entry := range entries {
		if len(files) >= limit {
			break
		}
		if entry.IsDir() {
			subFiles := collectFiles(filepath.Join(dir, entry.Name()), limit-len(files))
			files = append(files, subFiles...)
		} else {
			if entry.Name() == "SKILL.md" {
				continue
			}
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}

	return files
}

func sampleSkillFilesWithRipgrep(dir string, limit int) ([]string, error) {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(rgPath, "--files", "--hidden", dir)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		if filepath.Base(line) == "SKILL.md" {
			continue
		}
		files = append(files, line)
		if len(files) >= limit {
			break
		}
	}

	return files, nil
}

// collectFiles recursively collects files from a directory up to the limit.
func collectFiles(dir string, limit int) []string {
	var files []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}

	for _, entry := range entries {
		if len(files) >= limit {
			break
		}
		fullPath := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			subFiles := collectFiles(fullPath, limit-len(files))
			files = append(files, subFiles...)
		} else {
			files = append(files, fullPath)
		}
	}

	return files
}

// checkPermission checks if the skill can be loaded based on permissions.
func (s *skillTool) checkPermission(ctx context.Context, sessionID string, agentName string, skillName, description string) bool {
	action := s.registry.EvaluatePermission(agentName, SkillToolName, skillName)

	switch action {
	case permission.ActionAllow:
		return true
	case permission.ActionDeny:
		return false
	default:
		return s.permissions.Request(ctx, permission.CreatePermissionRequest{
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
		return "Load a specialized skill that provides domain-specific instructions and workflows. No skills are currently available."
	}

	var sb strings.Builder
	sb.WriteString("Load a specialized skill that provides domain-specific instructions and workflows.\n\n")
	sb.WriteString("When you recognize that a task matches one of the available skills listed below, use this tool to load the full skill instructions.\n\n")
	sb.WriteString("The skill will inject detailed instructions, workflows, and access to bundled resources (scripts, references, templates) into the conversation context.\n\n")
	sb.WriteString("Tool output includes a `<skill_content name=\"...\">` block with the loaded content.\n\n")
	sb.WriteString("The following skills provide specialized sets of instructions for particular tasks.\n")
	sb.WriteString("Invoke this tool to load a skill when a task matches one of the available skills listed below:\n\n")
	sb.WriteString("Important:\n")
	sb.WriteString("- If you see a <skill_content> tag in the current conversation turn, the skill has ALREADY been loaded - follow the instructions directly instead of calling this tool again\n")
	sb.WriteString("- Do not invoke a skill that is already loaded in the conversation\n\n")
	sb.WriteString("<available_skills>\n")

	for _, sk := range accessibleSkills {
		baseDir := filepath.Dir(sk.Location)
		fmt.Fprintf(&sb, "  <skill>\n")
		fmt.Fprintf(&sb, "    <name>%s</name>\n", sk.Name)
		fmt.Fprintf(&sb, "    <description>%s</description>\n", sk.Description)
		if sk.ArgumentHint != "" {
			fmt.Fprintf(&sb, "    <args>%s</args>\n", sk.ArgumentHint)
		}
		fmt.Fprintf(&sb, "    <location>file://%s</location>\n", baseDir)
		fmt.Fprintf(&sb, "  </skill>\n")
	}

	sb.WriteString("</available_skills>")

	return sb.String()
}

func (s *skillTool) buildSkillParameterDescription() string {
	skills := skill.All()
	accessibleSkills := s.filterSkillsByPermission(skills)

	if len(accessibleSkills) == 0 {
		return "The name of the skill from available_skills"
	}

	examples := make([]string, 0, 3)
	for i := 0; i < len(accessibleSkills) && i < 3; i++ {
		examples = append(examples, fmt.Sprintf("'%s'", accessibleSkills[i].Name))
	}

	if len(examples) > 0 {
		return fmt.Sprintf("The name of the skill from available_skills (e.g., %s, ...)", strings.Join(examples, ", "))
	}

	return "The name of the skill from available_skills"
}
