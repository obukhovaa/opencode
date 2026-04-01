package dialog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/tui/util"
	"gopkg.in/yaml.v3"
)

// Command prefix constants
const (
	UserCommandPrefix    = "user:"
	ProjectCommandPrefix = "project:"
)

// namedArgPattern is a regex pattern to find named arguments in the format $NAME
var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

// hintBracketPattern extracts bracket groups from argument-hint strings
var hintBracketPattern = regexp.MustCompile(`\[([^\]]+)\]`)

// commandFrontmatter represents the YAML frontmatter of a custom command
type commandFrontmatter struct {
	Title        string `yaml:"title"`
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
}

// parseCommandMarkdown parses a markdown file with optional YAML frontmatter.
// Returns the frontmatter fields and the body content after the closing ---.
func parseCommandMarkdown(raw []byte) (commandFrontmatter, string) {
	content := string(raw)

	if !strings.HasPrefix(content, "---\n") {
		return commandFrontmatter{}, content
	}

	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end == -1 {
		// Check for --- at end of file with no trailing newline
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - 3
		} else {
			return commandFrontmatter{}, content
		}
	}

	var fm commandFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		logging.Warn("Failed to parse command frontmatter", "error", err)
		return commandFrontmatter{}, content
	}

	body := ""
	if end+4 < len(rest) {
		body = strings.TrimLeft(rest[end+4:], "\n")
	}

	return fm, body
}

// LoadCustomCommands loads custom commands from all discovery locations
func LoadCustomCommands() ([]Command, error) {
	cfg := config.Get()
	if cfg == nil {
		return nil, fmt.Errorf("config not loaded")
	}

	var commands []Command
	seen := make(map[string]bool)

	home, homeErr := os.UserHomeDir()
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" && homeErr == nil {
		xdgConfigHome = filepath.Join(home, ".config")
	}

	// User commands (global), lowest priority first
	userDirs := []string{}
	if xdgConfigHome != "" {
		userDirs = append(userDirs, filepath.Join(xdgConfigHome, "opencode", "commands"))
	}
	if homeErr == nil {
		userDirs = append(userDirs,
			filepath.Join(home, ".opencode", "commands"),
			filepath.Join(home, ".agents", "commands"),
		)
		if !isClaudeCommandsDisabled() {
			userDirs = append(userDirs, filepath.Join(home, ".claude", "commands"))
		}
	}

	for _, dir := range userDirs {
		cmds, err := loadCommandsFromDir(dir, UserCommandPrefix)
		if err != nil {
			logging.Warn("Failed to load user commands", "dir", dir, "error", err)
		} else {
			for _, cmd := range cmds {
				if seen[cmd.ID] {
					continue
				}
				seen[cmd.ID] = true
				commands = append(commands, cmd)
			}
		}
	}

	// Project commands (walk up from working dir to worktree root)
	workingDir := cfg.WorkingDir
	worktreeRoot := getWorktreeRoot(workingDir)
	current := workingDir
	for {
		projectDirs := []string{
			filepath.Join(current, ".opencode", "commands"),
			filepath.Join(current, ".agents", "commands"),
		}
		if !isClaudeCommandsDisabled() {
			projectDirs = append(projectDirs, filepath.Join(current, ".claude", "commands"))
		}
		for _, dir := range projectDirs {
			cmds, err := loadCommandsFromDir(dir, ProjectCommandPrefix)
			if err != nil {
				logging.Warn("Failed to load project commands", "dir", dir, "error", err)
			} else {
				for _, cmd := range cmds {
					if seen[cmd.ID] {
						continue
					}
					seen[cmd.ID] = true
					commands = append(commands, cmd)
				}
			}
		}

		if current == worktreeRoot || current == filepath.Dir(current) {
			break
		}
		current = filepath.Dir(current)
	}

	addScopeHints(commands)

	return commands, nil
}

// addScopeHints adds a scope hint (project/user) to command titles when
// the same base name exists in both scopes.
func addScopeHints(commands []Command) {
	// Count how many times each base name (without prefix) appears
	baseCounts := make(map[string]int)
	for _, cmd := range commands {
		baseCounts[baseCommandName(cmd.ID)]++
	}

	for i := range commands {
		base := baseCommandName(commands[i].ID)
		if baseCounts[base] <= 1 {
			continue
		}
		scope := "user"
		if strings.HasPrefix(commands[i].ID, ProjectCommandPrefix) {
			scope = "project"
		}
		commands[i].Title = commands[i].Title + " (" + scope + ")"
	}
}

// baseCommandName strips the user:/project: prefix from a command ID.
func baseCommandName(id string) string {
	if after, ok := strings.CutPrefix(id, UserCommandPrefix); ok {
		return after
	}
	if after, ok := strings.CutPrefix(id, ProjectCommandPrefix); ok {
		return after
	}
	return id
}

func isClaudeCommandsDisabled() bool {
	return os.Getenv("OPENCODE_DISABLE_CLAUDE_SKILLS") == "true"
}

func getWorktreeRoot(workingDir string) string {
	current := workingDir
	for {
		gitDir := filepath.Join(current, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return workingDir
		}
		current = parent
	}
}

// loadCommandsFromDir loads commands from a specific directory with the given prefix
func loadCommandsFromDir(commandsDir string, prefix string) ([]Command, error) {
	if _, err := os.Stat(commandsDir); os.IsNotExist(err) {
		return nil, nil
	}

	var commands []Command

	err := filepath.Walk(commandsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read command file %s: %w", path, err)
		}

		fm, body := parseCommandMarkdown(raw)

		commandID := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))

		relPath, err := filepath.Rel(commandsDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		commandIDPath := strings.ReplaceAll(filepath.Dir(relPath), string(filepath.Separator), ":")
		if commandIDPath != "." {
			commandID = commandIDPath + ":" + commandID
		}

		fullID := prefix + commandID

		title := fm.Title
		if title == "" {
			title = fullID
		}

		description := fm.Description
		if description == "" {
			description = fmt.Sprintf("Custom command from %s", relPath)
		}

		commandContent := body
		command := Command{
			ID:           fullID,
			Title:        title,
			Description:  description,
			Content:      commandContent,
			ArgumentHint: fm.ArgumentHint,
			Handler: func(cmd Command) tea.Cmd {
				return ParameterizedCommandHandler(commandContent, &cmd)
			},
		}

		commands = append(commands, command)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load custom commands from %s: %w", commandsDir, err)
	}

	return commands, nil
}

// CommandRunCustomMsg is sent when a custom command or skill is executed
type CommandRunCustomMsg struct {
	Content   string
	Args      map[string]string // Map of argument names to values
	CommandID string            // Original command/skill ID for dispatch
}

func ParameterizedCommandHandler(commandContent string, cmd *Command) tea.Cmd {
	matches := namedArgPattern.FindAllStringSubmatch(commandContent, -1)
	if len(matches) > 0 {
		argNames := make([]string, 0)
		argMap := make(map[string]bool)

		for _, match := range matches {
			argName := match[1] // Group 1 is the name without $
			if !argMap[argName] {
				argMap[argName] = true
				argNames = append(argNames, argName)
			}
		}

		argHints := ParseArgumentHints(cmd.ArgumentHint, argNames)

		return util.CmdHandler(ShowMultiArgumentsDialogMsg{
			CommandID: cmd.ID,
			Content:   commandContent,
			ArgNames:  argNames,
			ArgHints:  argHints,
		})
	}

	return util.CmdHandler(CommandRunCustomMsg{
		Content: commandContent,
		Args:    nil,
	})
}

// ParseArgumentHints maps bracket groups from an argument-hint string to placeholder names.
// Uses name-based matching: [commit-hash] maps to $COMMIT_HASH (hyphen→underscore, uppercase).
// Falls back to positional matching if name-based fails.
func ParseArgumentHints(hint string, argNames []string) map[string]string {
	if hint == "" {
		return nil
	}

	matches := hintBracketPattern.FindAllStringSubmatch(hint, -1)
	if len(matches) == 0 {
		return nil
	}

	hints := make(map[string]string)

	// Try name-based matching first
	for _, match := range matches {
		hintText := match[1]
		// Convert hint to placeholder name: lowercase-with-hyphens → UPPERCASE_WITH_UNDERSCORES
		normalized := strings.ToUpper(strings.ReplaceAll(hintText, "-", "_"))
		for _, name := range argNames {
			if name == normalized {
				hints[name] = hintText
			}
		}
	}

	// If name-based matching didn't match everything, try positional
	if len(hints) == 0 {
		for i, match := range matches {
			if i < len(argNames) {
				hints[argNames[i]] = match[1]
			}
		}
	}

	return hints
}

// ParameterizedSkillHandler checks if skill content has $PLACEHOLDER or positional
// argument patterns ($0, $ARGUMENTS). If yes, returns a tea.Cmd to show the
// argument dialog. If no, returns nil.
func ParameterizedSkillHandler(s *skill.Info) tea.Cmd {
	// Check for named $UPPERCASE placeholders
	matches := namedArgPattern.FindAllStringSubmatch(s.Content, -1)
	if len(matches) > 0 {
		argNames := make([]string, 0)
		argMap := make(map[string]bool)

		for _, match := range matches {
			argName := match[1]
			if !argMap[argName] {
				argMap[argName] = true
				argNames = append(argNames, argName)
			}
		}

		argHints := ParseArgumentHints(s.ArgumentHint, argNames)

		return util.CmdHandler(ShowMultiArgumentsDialogMsg{
			CommandID: "skill:" + s.Name,
			Content:   s.Content,
			ArgNames:  argNames,
			ArgHints:  argHints,
		})
	}

	// Check for positional argument patterns ($0, $ARGUMENTS, $ARGUMENTS[N])
	if skill.HasArgumentPatterns(s.Content) {
		argNames := []string{"ARGUMENTS"}
		argHints := ParseArgumentHints(s.ArgumentHint, argNames)

		return util.CmdHandler(ShowMultiArgumentsDialogMsg{
			CommandID: "skill:" + s.Name,
			Content:   s.Content,
			ArgNames:  argNames,
			ArgHints:  argHints,
		})
	}

	return nil
}
