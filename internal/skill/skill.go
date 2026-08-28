package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

	// Skills discovery refused to register, alongside skillCache and guarded
	// by the same lock.
	skillDropped []Dropped
)

// Dropped records a SKILL.md that discovery found but did not register, and
// why. Every reason here means the skill is invisible to every agent, so the
// set is aggregated into one WARN at the end of discovery and kept for
// Diagnostics: a per-file warning scattered through a busy startup log is how
// a whole family of skills goes missing without anyone noticing.
type Dropped struct {
	Path   string // SKILL.md path, or the configured path that did not resolve
	Reason string
}

// Info represents a skill with its metadata and content.
type Info struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	UserInvocable *bool          `yaml:"user-invocable,omitempty"`
	ArgumentHint  string         `yaml:"argument-hint,omitempty"`
	Metadata      map[string]any `yaml:"metadata,omitempty"`
	Location      string         `yaml:"-"` // File path, not in frontmatter
	Content       string         `yaml:"-"` // Markdown content, not in frontmatter
}

// IsUserInvocable returns whether the skill can be invoked by users via slash commands.
// Defaults to true when not explicitly set.
func (i *Info) IsUserInvocable() bool {
	if i.UserInvocable == nil {
		return true
	}
	return *i.UserInvocable
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
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Diagnostics returns the skills discovery refused to register, in discovery
// order. Empty once every SKILL.md on the configured paths loaded cleanly.
func Diagnostics() []Dropped {
	state() // ensure discovery has run
	skillCacheLock.RLock()
	defer skillCacheLock.RUnlock()
	out := make([]Dropped, len(skillDropped))
	copy(out, skillDropped)
	return out
}

// state returns the cached skill registry, initializing it if necessary.
func state() map[string]Info {
	skillCacheOnce.Do(func() {
		skills, dropped := discoverSkills()
		skillCacheLock.Lock()
		skillCache, skillDropped = skills, dropped
		skillCacheLock.Unlock()
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
	skillDropped = nil
	skillCacheOnce = sync.Once{}
}

// wrapSkillContent wraps expanded skill content in <skill_content> tags so the
// LLM recognises it as already-loaded and won't re-invoke the skill tool.
func WrapSkillContent(name, content string) string {
	return fmt.Sprintf("<skill_content name=%q>\n%s\n</skill_content>", name, content)
}

// discoverSkills discovers all skills from various locations, returning the
// registry and every skill it had to drop.
func discoverSkills() (map[string]Info, []Dropped) {
	skills := make(map[string]Info)
	var dropped []Dropped

	cfg := config.Get()
	if cfg == nil {
		logging.Warn("Config not initialized, skipping skill discovery")
		return skills, dropped
	}

	workingDir := cfg.WorkingDir
	if workingDir == "" {
		logging.Warn("Working directory not set, skipping skill discovery")
		return skills, dropped
	}

	// Get git worktree root
	worktreeRoot := getWorktreeRoot(workingDir)

	// add registers a skill unless its name is already taken, recording the
	// shadowed loser either way: a duplicate is a silently missing skill, and
	// which copy wins depends on discovery order alone.
	add := func(skill Info) {
		if existing, ok := skills[skill.Name]; ok {
			dropped = append(dropped, Dropped{
				Path: skill.Location,
				Reason: fmt.Sprintf("duplicate skill name %q — already registered from %s, "+
					"which takes precedence", skill.Name, existing.Location),
			})
			return
		}
		skills[skill.Name] = skill
	}

	// Discover project-level skills (walk up from working dir to worktree)
	projectSkills, projectDropped := discoverProjectSkills(workingDir, worktreeRoot)
	dropped = append(dropped, projectDropped...)
	for _, skill := range projectSkills {
		add(skill)
	}

	// Discover global skills (project skills take precedence)
	globalSkills, globalDropped := discoverGlobalSkills()
	dropped = append(dropped, globalDropped...)
	for _, skill := range globalSkills {
		add(skill)
	}

	// Discover skills from custom paths (earlier discoveries take precedence)
	if cfg.Skills != nil && len(cfg.Skills.Paths) > 0 {
		customSkills, customDropped := discoverCustomPaths(cfg.Skills.Paths, workingDir)
		dropped = append(dropped, customDropped...)
		for _, skill := range customSkills {
			add(skill)
		}
	}

	logging.Debug("Discovered skills", "count", len(skills))
	if len(dropped) > 0 {
		details := make([]string, 0, len(dropped))
		for _, d := range dropped {
			details = append(details, fmt.Sprintf("%s (%s)", d.Path, d.Reason))
		}
		logging.Warn("Skills were dropped during discovery and are unavailable to every agent",
			"count", len(dropped),
			"registered", len(skills),
			"dropped", strings.Join(details, "; "))
	}
	return skills, dropped
}

// discoverProjectSkills scans project-level skill directories.
func discoverProjectSkills(workingDir, worktreeRoot string) ([]Info, []Dropped) {
	var skills []Info
	var dropped []Dropped

	scan := func(baseDir, pattern string) {
		found, bad := scanDirectory(baseDir, pattern)
		skills = append(skills, found...)
		dropped = append(dropped, bad...)
	}

	// Walk up from working directory to worktree root
	current := workingDir
	for {
		// Scan .opencode/{skill,skills}/**/SKILL.md
		scan(filepath.Join(current, ".opencode"), "{skill,skills}/**/SKILL.md")

		// Scan .agents/skills/**/SKILL.md
		scan(filepath.Join(current, ".agents"), "skills/**/SKILL.md")

		// Scan .claude/skills/**/SKILL.md (unless disabled)
		if !isClaudeSkillsDisabled() {
			scan(filepath.Join(current, ".claude"), "skills/**/SKILL.md")
		}

		// Stop if we've reached the worktree root or filesystem root
		if current == worktreeRoot || current == filepath.Dir(current) {
			break
		}
		current = filepath.Dir(current)
	}

	return skills, dropped
}

// discoverGlobalSkills scans global skill directories.
func discoverGlobalSkills() ([]Info, []Dropped) {
	var skills []Info
	var dropped []Dropped

	homeDir, err := os.UserHomeDir()
	if err != nil {
		logging.Warn("Failed to get user home directory", "error", err)
		return skills, dropped
	}

	scan := func(baseDir, pattern string) {
		found, bad := scanDirectory(baseDir, pattern)
		skills = append(skills, found...)
		dropped = append(dropped, bad...)
	}

	// Scan ~/.config/opencode/{skill,skills}/**/SKILL.md
	scan(filepath.Join(homeDir, ".config", "opencode"), "{skill,skills}/**/SKILL.md")

	// Scan ~/.agents/skills/**/SKILL.md
	scan(filepath.Join(homeDir, ".agents"), "skills/**/SKILL.md")

	// Scan ~/.claude/skills/**/SKILL.md (unless disabled)
	if !isClaudeSkillsDisabled() {
		scan(filepath.Join(homeDir, ".claude"), "skills/**/SKILL.md")
	}

	return skills, dropped
}

// discoverCustomPaths scans custom skill paths from config.
func discoverCustomPaths(paths []string, workingDir string) ([]Info, []Dropped) {
	var skills []Info
	var dropped []Dropped

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

		// Check if directory exists. A configured path that does not resolve
		// takes every skill under it with it — in a workspace that assembles
		// team repos at runtime this is the difference between "the team has
		// no skills" and "the team's checkout never landed".
		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			dropped = append(dropped, Dropped{
				Path:   resolved,
				Reason: fmt.Sprintf("configured skills path %q is not an existing directory", skillPath),
			})
			// Aggregated into the single discovery WARN below; a per-path
			// warning here would double-report it.
			logging.Debug("Skill path not found or not a directory", "path", resolved)
			continue
		}

		// Scan for SKILL.md files
		pathSkills, pathDropped := scanDirectory(resolved, "**/SKILL.md")
		skills = append(skills, pathSkills...)
		dropped = append(dropped, pathDropped...)
	}

	return skills, dropped
}

// scanDirectory scans a directory for SKILL.md files matching the pattern,
// returning the skills it parsed and the files it could not.
func scanDirectory(baseDir, pattern string) ([]Info, []Dropped) {
	var skills []Info
	var dropped []Dropped

	// Check if base directory exists
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return skills, dropped
	}

	// Use doublestar for glob matching
	fsys := os.DirFS(baseDir)
	matches, err := doublestar.Glob(fsys, pattern)
	if err != nil {
		logging.Warn("Failed to glob skill directory", "dir", baseDir, "pattern", pattern, "error", err)
		return skills, dropped
	}

	for _, match := range matches {
		fullPath := filepath.Join(baseDir, match)
		skill, err := parseSkillFile(fullPath)
		if err != nil {
			var skillErr *SkillError
			reason := err.Error()
			if errors.As(err, &skillErr) {
				// The path is reported separately; keep the reason readable.
				reason = skillErr.Message
				if skillErr.Err != nil {
					reason = fmt.Sprintf("%s: %v", skillErr.Message, skillErr.Err)
				}
			}
			dropped = append(dropped, Dropped{Path: fullPath, Reason: reason})
			// Aggregated into the single discovery WARN in discoverSkills.
			logging.Debug("Failed to parse skill file", "path", fullPath, "error", err)
			continue
		}
		skills = append(skills, *skill)
	}

	return skills, dropped
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
