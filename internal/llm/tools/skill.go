package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	// agentID scopes the <available_skills> listing to the skills THIS agent
	// may load. Empty means "no agent context" (only global permissions
	// apply), which is what every caller used to get: the listing advertised
	// every globally-permitted skill to every agent, so an agent that denied
	// a whole family of skills still paid for their descriptions on every
	// request.
	agentID string
}

// NewSkillTool creates a new skill tool instance scoped to agentID.
func NewSkillTool(permissions permission.Service, reg agentregistry.Registry, agentID string) BaseTool {
	return &skillTool{
		permissions: permissions,
		registry:    reg,
		agentID:     agentID,
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
	if s.registry == nil {
		// No permission model is wired (schema/description introspection):
		// there is nothing to filter against, so list what discovery found.
		return skills
	}
	filtered := make([]skill.Info, 0, len(skills))
	for _, sk := range skills {
		action := s.registry.EvaluatePermission(s.agentID, SkillToolName, sk.Name)
		if action != permission.ActionDeny {
			filtered = append(filtered, sk)
		}
	}
	return filtered
}

// skillListingLimits bounds the <available_skills> block. Zero on either
// field means "unbounded" for that dimension.
type skillListingLimits struct {
	maxDescriptionChars int // per-skill description cap
	maxTotalChars       int // whole-block cap, including the wrapper tags
}

// skillLimitsFromConfig reads the listing budget from config, tolerating an
// absent skills section (every field then falls back to the same defaults the
// loader would have applied).
func skillLimitsFromConfig() skillListingLimits {
	lim := skillListingLimits{maxDescriptionChars: config.DefaultSkillMaxDescriptionChars}
	if cfg := config.Get(); cfg != nil && cfg.Skills != nil {
		lim.maxDescriptionChars = cfg.Skills.MaxDescriptionChars
		lim.maxTotalChars = cfg.Skills.MaxListingChars
	}
	return lim
}

// truncateDescription clips a description to max characters, preferring the
// last word boundary so the tail does not end mid-token. Trigger terms belong
// at the head of a description precisely because this is where it is cut.
func truncateDescription(description string, max int) string {
	if max <= 0 || utf8.RuneCountInString(description) <= max {
		return description
	}
	runes := []rune(description)[:max]
	// Back up to the last word boundary, but only when one sits reasonably
	// near the cut — a tail with no spaces would otherwise surrender most of
	// its budget to the search.
	if idx := lastWordBoundary(runes); idx >= max*3/4 {
		runes = runes[:idx]
	}
	return strings.TrimRight(string(runes), " \t\n.,;:") + "…"
}

// lastWordBoundary returns the index of the last whitespace rune, or -1.
func lastWordBoundary(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		switch runes[i] {
		case ' ', '\t', '\n':
			return i
		}
	}
	return -1
}

// renderSkillEntry renders one <skill> element of the listing.
//
// Deliberately no <location>: the path is useless before the skill is loaded
// (Run reports the base directory with the content), and across a 60-skill
// inventory the location lines alone were 17% of the whole block.
func renderSkillEntry(sk skill.Info, maxDescriptionChars int) string {
	var sb strings.Builder
	sb.WriteString("  <skill>\n")
	fmt.Fprintf(&sb, "    <name>%s</name>\n", sk.Name)
	fmt.Fprintf(&sb, "    <description>%s</description>\n", truncateDescription(sk.Description, maxDescriptionChars))
	if sk.ArgumentHint != "" {
		fmt.Fprintf(&sb, "    <args>%s</args>\n", sk.ArgumentHint)
	}
	sb.WriteString("  </skill>\n")
	return sb.String()
}

// renderAvailableSkills renders the <available_skills> block, dropping whole
// entries from the end once the block would exceed lim.maxTotalChars.
//
// skills must already be sorted (skill.All sorts by name) so the same
// inventory always yields the same listing — a budget that silently kept a
// different subset per process would make a missing skill unreproducible.
// Dropping is disclosed in-band: omitted skills remain loadable by name, so
// the model needs to know the list it can see is partial.
func renderAvailableSkills(skills []skill.Info, lim skillListingLimits) string {
	const closing = "</available_skills>"

	entries := make([]string, 0, len(skills))
	shown := 0
	used := 0
	if lim.maxTotalChars > 0 {
		// Reserve room for the tags that wrap the entries; the note is only
		// emitted when something is dropped, and its own length is charged
		// against the reserve rather than the entries.
		used = utf8.RuneCountInString(closing) + 256
	}
	for _, sk := range skills {
		entry := renderSkillEntry(sk, lim.maxDescriptionChars)
		if lim.maxTotalChars > 0 && used+utf8.RuneCountInString(entry) > lim.maxTotalChars {
			break
		}
		used += utf8.RuneCountInString(entry)
		entries = append(entries, entry)
		shown++
	}

	var sb strings.Builder
	if shown < len(skills) {
		fmt.Fprintf(&sb, "<available_skills showing=\"%d\" total=\"%d\">\n", shown, len(skills))
	} else {
		sb.WriteString("<available_skills>\n")
	}
	for _, e := range entries {
		sb.WriteString(e)
	}
	if shown < len(skills) {
		fmt.Fprintf(&sb, "  <note>%d of %d skills are omitted from this listing to stay within the "+
			"configured skill listing budget. An omitted skill can still be loaded by name if you "+
			"know it; ask the user to name it when a task needs a skill you cannot see.</note>\n",
			len(skills)-shown, len(skills))
	}
	sb.WriteString(closing)
	return sb.String()
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
	sb.WriteString("- Do not invoke a skill that is already loaded in the conversation\n")
	sb.WriteString("- A description may be truncated with an ellipsis; load the skill to see the whole thing\n\n")
	sb.WriteString(renderAvailableSkills(accessibleSkills, skillLimitsFromConfig()))

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
