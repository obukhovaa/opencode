package dialog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
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

// commandFrontmatter represents the YAML frontmatter of a custom command
type commandFrontmatter struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
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
	}

	for _, dir := range userDirs {
		cmds, err := loadCommandsFromDir(dir, UserCommandPrefix)
		if err != nil {
			logging.Warn("Failed to load user commands", "dir", dir, "error", err)
		} else {
			commands = append(commands, cmds...)
		}
	}

	// Project commands
	workingDir := cfg.WorkingDir
	projectDirs := []string{
		filepath.Join(workingDir, ".opencode", "commands"),
		filepath.Join(workingDir, ".agents", "commands"),
	}

	for _, dir := range projectDirs {
		cmds, err := loadCommandsFromDir(dir, ProjectCommandPrefix)
		if err != nil {
			logging.Warn("Failed to load project commands", "dir", dir, "error", err)
		} else {
			commands = append(commands, cmds...)
		}
	}

	return commands, nil
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
			ID:          fullID,
			Title:       title,
			Description: description,
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

// CommandRunCustomMsg is sent when a custom command is executed
type CommandRunCustomMsg struct {
	Content string
	Args    map[string]string // Map of argument names to values
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

		return util.CmdHandler(ShowMultiArgumentsDialogMsg{
			CommandID: cmd.ID,
			Content:   commandContent,
			ArgNames:  argNames,
		})
	}

	return util.CmdHandler(CommandRunCustomMsg{
		Content: commandContent,
		Args:    nil,
	})
}
