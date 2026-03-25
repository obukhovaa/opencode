package slashcmd

import "strings"

const skillPrefix = "skill:"

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

	name, args, _ := strings.Cut(rest, " ")
	args = strings.TrimSpace(args)

	isSkill := false
	if strings.HasPrefix(name, skillPrefix) {
		isSkill = true
		name = strings.TrimPrefix(name, skillPrefix)
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
