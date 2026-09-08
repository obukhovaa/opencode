package dialog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/util"
	"gopkg.in/yaml.v3"
)

// hintBracketPattern extracts bracket groups from argument-hint strings.
// Supports both [square] and <angle> brackets (Claude Code docs use angle brackets).
var hintBracketPattern = regexp.MustCompile(`\[([^\]]+)\]|<([^>]+)>`)

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
		cmds, err := loadCommandsFromDir(dir, slashcmd.UserCommandPrefix)
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
			cmds, err := loadCommandsFromDir(dir, slashcmd.ProjectCommandPrefix)
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
		baseCounts[slashcmd.BaseCommandName(cmd.ID)]++
	}

	for i := range commands {
		base := slashcmd.BaseCommandName(commands[i].ID)
		if baseCounts[base] <= 1 {
			continue
		}
		scope := "user"
		if strings.HasPrefix(commands[i].ID, slashcmd.ProjectCommandPrefix) {
			scope = "project"
		}
		commands[i].Title = commands[i].Title + " (" + scope + ")"
	}
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
			CommandInfo: slashcmd.CommandInfo{
				ID:           fullID,
				Title:        title,
				Description:  description,
				Content:      commandContent,
				ArgumentHint: fm.ArgumentHint,
			},
			Handler: StageCommandHandler,
		}

		commands = append(commands, command)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load custom commands from %s: %w", commandsDir, err)
	}

	return commands, nil
}

// CommandRunCustomMsg carries the argument dialog's result to an action
// command's handler. Prompt commands and skills are staged into the editor
// instead (see StageInvocationMsg), so the only consumers are the TUI actions
// that collect arguments through the dialog: /loop and /rename.
type CommandRunCustomMsg struct {
	Args      map[string]string // Map of argument names to values
	CommandID string            // Original command/skill ID for dispatch
}

// ParseArgumentHints maps bracket groups from an argument-hint string to placeholder names.
// Uses name-based matching: [commit-hash] or <commit-hash> maps to $COMMIT_HASH
// (hyphen→underscore, uppercase). Falls back to positional matching if name-based fails.
// If the hint contains no bracket groups and there's only one argument, the raw hint
// is used as the placeholder for that argument.
func ParseArgumentHints(hint string, argNames []string) map[string]string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return nil
	}

	matches := hintBracketPattern.FindAllStringSubmatch(hint, -1)
	if len(matches) == 0 {
		// No brackets. For single-argument skills (bare $ARGUMENTS), use the
		// whole hint string as the placeholder directly.
		if len(argNames) == 1 {
			return map[string]string{argNames[0]: hint}
		}
		return nil
	}

	// Extract the group text from either capture group (square or angle brackets).
	extractGroup := func(match []string) string {
		for i := 1; i < len(match); i++ {
			if match[i] != "" {
				return match[i]
			}
		}
		return ""
	}

	hints := make(map[string]string)

	// Try name-based matching first
	for _, match := range matches {
		hintText := extractGroup(match)
		if hintText == "" {
			continue
		}
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
				hints[argNames[i]] = extractGroup(match)
			}
		}
	}

	return hints
}

// CommandRegistry assembles the slash-command registry for callers that have no
// TUI command table of their own — the non-interactive prompt paths. Interactive
// callers build the registry from their registered commands instead, so the
// commands they can expand are exactly the ones they can run.
func CommandRegistry() slashcmd.Registry {
	commands := slashcmd.BuiltinCommands()

	custom, err := LoadCustomCommands()
	if err != nil {
		logging.Warn("Failed to load custom commands", "error", err)
	} else {
		for _, cmd := range custom {
			commands = append(commands, cmd.CommandInfo)
		}
	}

	return slashcmd.Registry{Commands: commands, Skills: skill.All()}
}

// StagedInvocation renders the editor text for an invocation of name with args.
// Staging always leaves a trailing space after a bare command so the user can
// keep typing arguments or instructions without repositioning the cursor.
func StagedInvocation(name, args string) string {
	if args == "" {
		return "/" + name + " "
	}
	return "/" + name + " " + args
}

// StagedArgs renders the values collected by the argument dialog as the
// argument portion of a staged invocation, per mode. names is the dialog's
// field order, parallel to values.
func StagedArgs(names []string, values []string, mode ArgsMode) string {
	if len(values) == 0 {
		return ""
	}
	if mode == ArgsModeWhole {
		// $ARGUMENTS binds the whole unsplit string, so the single value is
		// written verbatim — quoting it would leave quotes in the prompt.
		return strings.TrimSpace(values[0])
	}
	return skill.QuoteArgs(positionalSlots(names, values))
}

// positionalSlots orders values so that each one lands in the argument slot its
// placeholder reads.
//
// For named placeholders the dialog order *is* the slot order, so values pass
// through. For numeric placeholders the declared indices need not start at 0 or
// be contiguous — a skill may reference only $2 — so the values are placed at
// their own index and the gaps are filled with empty arguments. Without the
// padding, a lone $2 would be staged in slot 0 and bind to nothing.
func positionalSlots(names []string, values []string) []string {
	indices := make([]int, len(names))
	highest := -1
	for i, name := range names {
		idx, err := strconv.Atoi(name)
		if err != nil {
			// Named placeholders: dialog order is slot order.
			return values
		}
		indices[i] = idx
		if idx > highest {
			highest = idx
		}
	}
	if highest < 0 {
		return values
	}

	slots := make([]string, highest+1)
	for i, idx := range indices {
		if i < len(values) {
			slots[idx] = values[i]
		}
	}
	return slots
}

// maxPositionalIndex bounds the $N / $ARGUMENTS[N] index that can size the
// staged argument list. No dialog can collect this many fields, so anything at
// or above it is a typo rather than a parameter.
const maxPositionalIndex = 32

// argumentFields derives the argument-dialog fields declared by content.
//
// allowNamed distinguishes the two content dialects: custom commands support
// named $FOO placeholders, skills do not — for a skill, $HOME or $PATH in a
// shell snippet is a shell reference, not a parameter.
// See: https://code.claude.com/docs/en/skills#available-string-substitutions
//
// The returned names are also the binding order: the expander binds names[i] to
// positional argument i.
func argumentFields(content string, argumentHint string, allowNamed bool) (names []string, hints map[string]string, mode ArgsMode, ok bool) {
	if allowNamed {
		if named := slashcmd.NamedPlaceholders(content); len(named) > 0 {
			return named, ParseArgumentHints(argumentHint, named), ArgsModePositional, true
		}
	}

	if indices := skill.ExtractPositionalIndices(content); len(indices) > 0 {
		// The highest declared index sizes the staged argument list, and
		// $ARGUMENTS[N] accepts any number of digits, so a typo'd or hostile
		// $ARGUMENTS[500000000] would have the staging path allocate a
		// half-billion-element slice. Indices past the cap are ignored rather
		// than rejected: they cannot bind to anything a dialog could collect.
		bounded := make([]int, 0, len(indices))
		for _, idx := range indices {
			if idx < maxPositionalIndex {
				bounded = append(bounded, idx)
			}
		}
		if len(bounded) == 0 {
			return nil, nil, ArgsModePositional, false
		}
		names = make([]string, len(bounded))
		for i, idx := range bounded {
			names[i] = strconv.Itoa(idx)
		}
		return names, ParseArgumentHints(argumentHint, names), ArgsModePositional, true
	}

	if strings.Contains(content, "$ARGUMENTS") {
		names = []string{"ARGUMENTS"}
		return names, ParseArgumentHints(argumentHint, names), ArgsModeWhole, true
	}

	return nil, nil, ArgsModePositional, false
}

// StageSkillHandler stages a user-invocable skill: it opens the argument dialog
// when the skill declares placeholders, and otherwise stages the bare
// invocation for the user to complete.
func StageSkillHandler(s *skill.Info) tea.Cmd {
	name := slashcmd.SkillPrefix + s.Name
	names, hints, mode, ok := argumentFields(s.Content, s.ArgumentHint, false)
	if !ok {
		return util.CmdHandler(StageInvocationMsg{Text: StagedInvocation(name, "")})
	}
	return util.CmdHandler(ShowMultiArgumentsDialogMsg{
		CommandID: name,
		Content:   s.Content,
		ArgNames:  names,
		ArgHints:  hints,
		Mode:      mode,
	})
}

// StageCommandHandler is the Handler for every prompt command — the builtins
// that carry content and all user:/project: custom commands. Action commands
// keep their own handlers and are never staged.
func StageCommandHandler(cmd Command) tea.Cmd {
	names, hints, mode, ok := argumentFields(cmd.Content, cmd.ArgumentHint, true)
	if !ok {
		return util.CmdHandler(StageInvocationMsg{Text: StagedInvocation(cmd.ID, "")})
	}
	return util.CmdHandler(ShowMultiArgumentsDialogMsg{
		CommandID: cmd.ID,
		Content:   cmd.Content,
		ArgNames:  names,
		ArgHints:  hints,
		Mode:      mode,
	})
}
