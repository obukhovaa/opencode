package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	maxNameLength        = 64
	maxDescriptionLength = 1024
	maxContentSize       = 100 * 1024 // 100KB
)

var (
	// Skill name validation regex: ^[a-z0-9]+(-[a-z0-9]+)*$
	nameRegex = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

	// Cached skill registry
	skillCache     map[string]Info
	skillCacheLock sync.RWMutex
	skillCacheOnce sync.Once
)

// Info represents a skill with its metadata and content.
type Info struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	Location      string            `yaml:"-"` // File path, not in frontmatter
	Content       string            `yaml:"-"` // Markdown content, not in frontmatter
}

// Error types
var (
	ErrSkillNotFound      = errors.New("skill not found")
	ErrInvalidName        = errors.New("invalid skill name")
	ErrInvalidDescription = errors.New("invalid skill description")
	ErrNameMismatch       = errors.New("skill name does not match directory name")
	ErrInvalidFrontmatter = errors.New("invalid skill frontmatter")
	ErrContentTooLarge    = errors.New("skill content exceeds maximum size")
)

// SkillError wraps an error with additional context.
type SkillError struct {
	Path    string
	Message string
	Err     error
}

func (e *SkillError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Path, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

func (e *SkillError) Unwrap() error {
	return e.Err
}

// Get returns a skill by name.
func Get(name string) (*Info, error) {
	skills := state()
	skill, ok := skills[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	return &skill, nil
}

// All returns all available skills.
func All() []Info {
	skills := state()
	result := make([]Info, 0, len(skills))
	for _, skill := range skills {
		result = append(result, skill)
	}
	return result
}

// state returns the cached skill registry, initializing it if necessary.
func state() map[string]Info {
	skillCacheOnce.Do(func() {
		skillCache = discoverSkills()
	})

	skillCacheLock.RLock()
	defer skillCacheLock.RUnlock()
	return skillCache
}

// Invalidate clears the skill cache, forcing rediscovery on next access.
func Invalidate() {
	skillCacheLock.Lock()
	defer skillCacheLock.Unlock()
	skillCache = nil
	skillCacheOnce = sync.Once{}
}

// discoverSkills discovers all skills from various locations.
func discoverSkills() map[string]Info {
	skills := make(map[string]Info)

	cfg := config.Get()
	if cfg == nil {
		logging.Warn("Config not initialized, skipping skill discovery")
		return skills
	}

	workingDir := cfg.WorkingDir
	if workingDir == "" {
		logging.Warn("Working directory not set, skipping skill discovery")
		return skills
	}

	// Get git worktree root
	worktreeRoot := getWorktreeRoot(workingDir)

	// Discover project-level skills (walk up from working dir to worktree)
	projectSkills := discoverProjectSkills(workingDir, worktreeRoot)
	for _, skill := range projectSkills {
		if existing, ok := skills[skill.Name]; ok {
			logging.Warn("Duplicate skill name found, using first occurrence",
				"name", skill.Name,
				"existing", existing.Location,
				"duplicate", skill.Location)
			continue
		}
		skills[skill.Name] = skill
	}

	// Discover global skills
	globalSkills := discoverGlobalSkills()
	for _, skill := range globalSkills {
		if _, ok := skills[skill.Name]; ok {
			// Project skills take precedence over global
			continue
		}
		skills[skill.Name] = skill
	}

	// Discover skills from custom paths
	if cfg.Skills != nil && len(cfg.Skills.Paths) > 0 {
		customSkills := discoverCustomPaths(cfg.Skills.Paths, workingDir)
		for _, skill := range customSkills {
			if _, ok := skills[skill.Name]; ok {
				// Earlier discoveries take precedence
				continue
			}
			skills[skill.Name] = skill
		}
	}

	logging.Debug("Discovered skills", "count", len(skills))
	return skills
}

// discoverProjectSkills scans project-level skill directories.
func discoverProjectSkills(workingDir, worktreeRoot string) []Info {
	var skills []Info

	// Walk up from working directory to worktree root
	current := workingDir
	for {
		// Scan .opencode/{skill,skills}/**/SKILL.md
		opencodeSkills := scanDirectory(filepath.Join(current, ".opencode"), "{skill,skills}/**/SKILL.md")
		skills = append(skills, opencodeSkills...)

		// Scan .claude/skills/**/SKILL.md (unless disabled)
		if !isClaudeSkillsDisabled() {
			claudeSkills := scanDirectory(filepath.Join(current, ".claude"), "skills/**/SKILL.md")
			skills = append(skills, claudeSkills...)
		}

		// Stop if we've reached the worktree root or filesystem root
		if current == worktreeRoot || current == filepath.Dir(current) {
			break
		}
		current = filepath.Dir(current)
	}

	return skills
}

// discoverGlobalSkills scans global skill directories.
func discoverGlobalSkills() []Info {
	var skills []Info

	homeDir, err := os.UserHomeDir()
	if err != nil {
		logging.Warn("Failed to get user home directory", "error", err)
		return skills
	}

	// Scan ~/.config/opencode/{skill,skills}/**/SKILL.md
	configDir := filepath.Join(homeDir, ".config", "opencode")
	opencodeSkills := scanDirectory(configDir, "{skill,skills}/**/SKILL.md")
	skills = append(skills, opencodeSkills...)

	// Scan ~/.claude/skills/**/SKILL.md (unless disabled)
	if !isClaudeSkillsDisabled() {
		claudeDir := filepath.Join(homeDir, ".claude")
		claudeSkills := scanDirectory(claudeDir, "skills/**/SKILL.md")
		skills = append(skills, claudeSkills...)
	}

	return skills
}

// discoverCustomPaths scans custom skill paths from config.
func discoverCustomPaths(paths []string, workingDir string) []Info {
	var skills []Info

	homeDir, _ := os.UserHomeDir()

	for _, skillPath := range paths {
		// Expand ~ to home directory
		expanded := skillPath
		if strings.HasPrefix(skillPath, "~/") && homeDir != "" {
			expanded = filepath.Join(homeDir, skillPath[2:])
		}

		// Resolve relative paths
		resolved := expanded
		if !filepath.IsAbs(expanded) {
			resolved = filepath.Join(workingDir, expanded)
		}

		// Check if directory exists
		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			logging.Warn("Skill path not found or not a directory", "path", resolved)
			continue
		}

		// Scan for SKILL.md files
		pathSkills := scanDirectory(resolved, "**/SKILL.md")
		skills = append(skills, pathSkills...)
	}

	return skills
}

// scanDirectory scans a directory for SKILL.md files matching the pattern.
func scanDirectory(baseDir, pattern string) []Info {
	var skills []Info

	// Check if base directory exists
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return skills
	}

	// Use doublestar for glob matching
	fsys := os.DirFS(baseDir)
	matches, err := doublestar.Glob(fsys, pattern)
	if err != nil {
		logging.Warn("Failed to glob skill directory", "dir", baseDir, "pattern", pattern, "error", err)
		return skills
	}

	for _, match := range matches {
		fullPath := filepath.Join(baseDir, match)
		skill, err := parseSkillFile(fullPath)
		if err != nil {
			logging.Warn("Failed to parse skill file", "path", fullPath, "error", err)
			continue
		}
		skills = append(skills, *skill)
	}

	return skills
}

// parseSkillFile parses a SKILL.md file and returns a skill Info.
func parseSkillFile(path string) (*Info, error) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &SkillError{Path: path, Message: "failed to read file", Err: err}
	}

	// Check content size
	if len(data) > maxContentSize {
		return nil, &SkillError{Path: path, Message: "content too large", Err: ErrContentTooLarge}
	}

	// Split frontmatter and content
	frontmatter, content, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, &SkillError{Path: path, Message: "failed to parse frontmatter", Err: err}
	}

	// Parse YAML frontmatter
	var skill Info
	if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
		return nil, &SkillError{Path: path, Message: "invalid YAML frontmatter", Err: err}
	}

	// Validate frontmatter
	if err := validateFrontmatter(&skill); err != nil {
		return nil, &SkillError{Path: path, Message: "invalid frontmatter", Err: err}
	}

	// Validate name matches directory
	expectedName := filepath.Base(filepath.Dir(path))
	if skill.Name != expectedName {
		return nil, &SkillError{
			Path:    path,
			Message: fmt.Sprintf("name mismatch: expected %s, got %s", expectedName, skill.Name),
			Err:     ErrNameMismatch,
		}
	}

	// Set location and content
	skill.Location = path
	skill.Content = strings.TrimSpace(content)

	return &skill, nil
}

// splitFrontmatter splits a markdown file into frontmatter and content.
func splitFrontmatter(data string) (frontmatter, content string, err error) {
	// Check for frontmatter delimiters
	if !strings.HasPrefix(data, "---\n") && !strings.HasPrefix(data, "---\r\n") {
		return "", "", fmt.Errorf("missing frontmatter delimiter")
	}

	// Find end of frontmatter
	lines := strings.Split(data, "\n")
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			endIdx = i
			break
		}
	}

	if endIdx == -1 {
		return "", "", fmt.Errorf("missing frontmatter end delimiter")
	}

	// Extract frontmatter and content
	frontmatter = strings.Join(lines[1:endIdx], "\n")
	if endIdx+1 < len(lines) {
		content = strings.Join(lines[endIdx+1:], "\n")
	}

	return frontmatter, content, nil
}

// validateFrontmatter validates the skill frontmatter.
func validateFrontmatter(skill *Info) error {
	// Validate name
	if err := validateName(skill.Name); err != nil {
		return err
	}

	// Validate description
	if err := validateDescription(skill.Description); err != nil {
		return err
	}

	return nil
}

// validateName validates a skill name.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidName)
	}

	if len(name) > maxNameLength {
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalidName, maxNameLength)
	}

	if !nameRegex.MatchString(name) {
		return fmt.Errorf("%w: must match pattern ^[a-z0-9]+(-[a-z0-9]+)*$", ErrInvalidName)
	}

	return nil
}

// validateDescription validates a skill description.
func validateDescription(description string) error {
	if description == "" {
		return fmt.Errorf("%w: description is required", ErrInvalidDescription)
	}

	if len(description) > maxDescriptionLength {
		return fmt.Errorf("%w: description exceeds %d characters", ErrInvalidDescription, maxDescriptionLength)
	}

	return nil
}

// getWorktreeRoot returns the git worktree root, or the working directory if not in a git repo.
func getWorktreeRoot(workingDir string) string {
	// Try to find .git directory by walking up
	current := workingDir
	for {
		gitDir := filepath.Join(current, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return current
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root
			return workingDir
		}
		current = parent
	}
}

// isClaudeSkillsDisabled checks if Claude skills discovery is disabled.
func isClaudeSkillsDisabled() bool {
	// Check environment variable
	return os.Getenv("OPENCODE_DISABLE_CLAUDE_SKILLS") == "true"
}
