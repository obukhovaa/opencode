package slashcmd

import (
	"embed"
	"strings"
)

//go:embed commands/*.md
var commandPrompts embed.FS

// CommandInfo holds the data fields of a slash command.
// It is intentionally free of TUI dependencies so it can be used by both
// interactive (TUI) and non-interactive (CLI/flow) code paths.
type CommandInfo struct {
	ID           string
	Title        string
	Description  string
	Content      string
	ArgumentHint string
	TUIOnly      bool // true for commands that only work in interactive mode
}

const (
	UserCommandPrefix    = "user:"
	ProjectCommandPrefix = "project:"
)

func readPrompt(path string) string {
	data, err := commandPrompts.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// BuiltinCommands returns the canonical list of built-in slash commands.
func BuiltinCommands() []CommandInfo {
	return []CommandInfo{
		{
			ID:          "init",
			Title:       "Initialize Project",
			Description: "Create/Update the AGENTS.md memory file",
			Content:     readPrompt("commands/init.md"),
		},
		{
			ID:           "review",
			Title:        "Review Code",
			Description:  "Review a given work using provided commit hash or branch",
			Content:      readPrompt("commands/review.md"),
			ArgumentHint: "[commit, branch, pr, uncommitted]",
		},
		{
			ID:          "commit",
			Title:       "Commit and Push",
			Description: "Commit changes to git using conventional commits and push",
			Content:     readPrompt("commands/commit.md"),
		},
		{
			ID:          "compact",
			Title:       "Compact Session",
			Description: "Summarize the current session and create a new one with the summary",
			TUIOnly:     true,
		},
		{
			ID:          "agents",
			Title:       "List Agents",
			Description: "List all available agents and their configuration",
			TUIOnly:     true,
		},
		{
			ID:          "auto-approve",
			Title:       "Toggle Auto-Approve",
			Description: "Toggle auto-approve mode for the current session (skip permission dialogs)",
			TUIOnly:     true,
		},
	}
}

// BaseCommandName strips the user:/project: prefix from a command ID.
func BaseCommandName(id string) string {
	if after, ok := strings.CutPrefix(id, UserCommandPrefix); ok {
		return after
	}
	if after, ok := strings.CutPrefix(id, ProjectCommandPrefix); ok {
		return after
	}
	return id
}
