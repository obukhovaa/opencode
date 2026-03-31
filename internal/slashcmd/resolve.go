package slashcmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

type ActionType int

const (
	ActionCommand ActionType = iota
	ActionSkill
	ActionNotFound
)

var (
	ErrNotUserInvocable = errors.New("skill is not user-invocable")
	ErrTUIOnly          = errors.New("command is only available in interactive mode")
)

type ResolvedAction struct {
	Type    ActionType
	Command *dialog.Command
	Skill   *skill.Info
	Prompt  string
	Args    string
}

var tuiOnlyCommands = map[string]bool{
	"compact":      true,
	"agents":       true,
	"auto-approve": true,
}

func Resolve(parsed *ParsedCommand, commands []dialog.Command, skills []skill.Info, interactive bool) (*ResolvedAction, error) {
	if parsed == nil {
		return &ResolvedAction{Type: ActionNotFound}, nil
	}

	if parsed.IsSkill {
		return resolveSkill(parsed, skills)
	}

	return resolveCommand(parsed, commands, interactive)
}

func resolveCommand(parsed *ParsedCommand, commands []dialog.Command, interactive bool) (*ResolvedAction, error) {
	var matched *dialog.Command

	for i := range commands {
		cmd := &commands[i]
		if cmd.ID == parsed.Name {
			matched = cmd
			break
		}
		base := baseCommandName(cmd.ID)
		if base == parsed.Name {
			matched = cmd
			break
		}
	}

	if matched == nil {
		return &ResolvedAction{Type: ActionNotFound}, nil
	}

	if !interactive && tuiOnlyCommands[matched.ID] {
		return nil, fmt.Errorf("%w: '%s'", ErrTUIOnly, matched.ID)
	}

	return &ResolvedAction{
		Type:    ActionCommand,
		Command: matched,
		Args:    parsed.Args,
	}, nil
}

func resolveSkill(parsed *ParsedCommand, skills []skill.Info) (*ResolvedAction, error) {
	for i := range skills {
		s := &skills[i]
		if s.Name == parsed.Name {
			if !s.IsUserInvocable() {
				return nil, fmt.Errorf("%w: '%s', set `user-invocable: true` in its SKILL.md frontmatter", ErrNotUserInvocable, s.Name)
			}
			return &ResolvedAction{
				Type:  ActionSkill,
				Skill: s,
				Args:  parsed.Args,
			}, nil
		}
	}
	return &ResolvedAction{Type: ActionNotFound}, nil
}

func baseCommandName(id string) string {
	if after, ok := strings.CutPrefix(id, dialog.UserCommandPrefix); ok {
		return after
	}
	if after, ok := strings.CutPrefix(id, dialog.ProjectCommandPrefix); ok {
		return after
	}
	return id
}

func BuildPrompt(action *ResolvedAction, sessionID string) string {
	switch action.Type {
	case ActionCommand:
		return "" // Command prompt is built via handler
	case ActionSkill:
		baseDir := filepath.Dir(action.Skill.Location)
		return skill.SubstituteContent(action.Skill.Content, skill.SubstituteParams{
			Args:      action.Args,
			SkillDir:  baseDir,
			SessionID: sessionID,
		})
	default:
		return ""
	}
}

func SubstituteArgs(content string, args string) string {
	return strings.ReplaceAll(content, "$ARGUMENTS", args)
}

// HasOnlyArgumentsPlaceholder checks if the content has only $ARGUMENTS as the
// sole named placeholder (no other $FOO, $BAR, etc). Returns false if there
// are multiple different named placeholders.
func HasOnlyArgumentsPlaceholder(content string) bool {
	matches := namedArgPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return false
	}
	for _, m := range matches {
		if m[1] != "ARGUMENTS" {
			return false
		}
	}
	return true
}
