package slashcmd

import (
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/skill"
)

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
	Command *CommandInfo
	Skill   *skill.Info
	Prompt  string
	Args    string
}

func Resolve(parsed *ParsedCommand, commands []CommandInfo, skills []skill.Info, interactive bool) (*ResolvedAction, error) {
	if parsed == nil {
		return &ResolvedAction{Type: ActionNotFound}, nil
	}

	if parsed.IsSkill {
		return resolveSkill(parsed, skills)
	}

	return resolveCommand(parsed, commands, interactive)
}

func resolveCommand(parsed *ParsedCommand, commands []CommandInfo, interactive bool) (*ResolvedAction, error) {
	var matched *CommandInfo

	for i := range commands {
		cmd := &commands[i]
		if cmd.ID == parsed.Name {
			matched = cmd
			break
		}
		base := BaseCommandName(cmd.ID)
		if base == parsed.Name {
			matched = cmd
			break
		}
	}

	if matched == nil {
		return &ResolvedAction{Type: ActionNotFound}, nil
	}

	if !interactive && matched.TUIOnly {
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
