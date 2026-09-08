package slashcmd

import (
	"strings"
	"unicode"
)

// SkillPrefix namespaces a skill inside the slash-command namespace:
// /skill:<name>.
const SkillPrefix = "skill:"

type ParsedCommand struct {
	Name    string
	Args    string
	IsSkill bool
	Raw     string
}

func Parse(input string) *ParsedCommand {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return nil
	}

	rest := input[1:]
	if rest == "" {
		return nil
	}

	// The name ends at the first whitespace rune, not the first space, so a
	// tab-separated argument is handled the same as a space-separated one.
	name, args := rest, ""
	if idx := strings.IndexFunc(rest, unicode.IsSpace); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx:])
	}

	isSkill := false
	if strings.HasPrefix(name, SkillPrefix) {
		isSkill = true
		name = strings.TrimPrefix(name, SkillPrefix)
	}

	if name == "" {
		return nil
	}

	return &ParsedCommand{
		Name:    name,
		Args:    args,
		IsSkill: isSkill,
		Raw:     input,
	}
}
