package clitool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

// DisableEnv turns the feature off from the environment (CI images, tests).
const DisableEnv = "OPENCODE_DISABLE_CLI_TOOLS"

// Diagnostic records a manifest that did not become a tool: invalid (fail
// closed) or shadowed by a higher-precedence file of the same name.
type Diagnostic struct {
	Path     string
	Reason   string
	Shadowed bool
}

// Set is the result of one discovery pass.
type Set struct {
	// Manifests are the valid tools, sorted by name.
	Manifests []*Manifest
	// Diagnostics lists invalid and shadowed manifests.
	Diagnostics []Diagnostic
	// Disabled is true when the feature was switched off (nothing scanned).
	Disabled bool
	// Dirs lists the directories that were scanned, in precedence order.
	Dirs []string
}

var (
	cacheOnce sync.Once
	cacheLock sync.RWMutex
	cached    *Set
)

// Tools returns the cached manifests for the current configuration.
func Tools() []*Manifest { return state().Manifests }

// Diagnostics returns the cached diagnostics for the current configuration.
func Diagnostics() []Diagnostic { return state().Diagnostics }

// Current returns the cached discovery result.
func Current() *Set { return state() }

// Lookup finds a cached manifest by tool name.
func Lookup(name string) (*Manifest, bool) {
	for _, m := range Tools() {
		if m.Name == name {
			return m, true
		}
	}
	return nil, false
}

// Invalidate clears the cache, forcing rediscovery on next access.
func Invalidate() {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	cached = nil
	cacheOnce = sync.Once{}
}

func state() *Set {
	cacheOnce.Do(func() {
		cfg := config.Get()
		var set *Set
		if cfg == nil || cfg.WorkingDir == "" {
			set = &Set{}
		} else {
			set = Discover(context.Background(), cfg.WorkingDir, cfg.CLITools)
			set.logSummary()
		}
		cacheLock.Lock()
		cached = set
		cacheLock.Unlock()
	})
	cacheLock.RLock()
	defer cacheLock.RUnlock()
	return cached
}

// IsDisabled reports whether discovery is switched off by config or env.
func IsDisabled(cfg *config.CLIToolsConfig) bool {
	if cfg != nil && cfg.Disabled {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv(DisableEnv)), "true")
}

// Discover scans every manifest location for workingDir, in precedence
// order, and returns the valid tools plus diagnostics. The first manifest
// for a name wins. It runs each valid manifest's `--help` capture (bounded)
// before returning. Uncached: callers wanting the process-wide set use
// Tools().
func Discover(ctx context.Context, workingDir string, cfg *config.CLIToolsConfig) *Set {
	set := &Set{}
	if IsDisabled(cfg) {
		set.Disabled = true
		return set
	}
	byName := map[string]*Manifest{}
	add := func(m *Manifest) {
		if existing, ok := byName[m.Name]; ok {
			set.Diagnostics = append(set.Diagnostics, Diagnostic{
				Path:     m.Location,
				Reason:   fmt.Sprintf("duplicate tool name %q — already defined by %s, which takes precedence", m.Name, existing.Location),
				Shadowed: true,
			})
			return
		}
		byName[m.Name] = m
	}
	for _, dir := range Dirs(workingDir, cfg) {
		set.Dirs = append(set.Dirs, dir)
		found, bad := scanDir(dir, workingDir)
		set.Diagnostics = append(set.Diagnostics, bad...)
		for _, m := range found {
			add(m)
		}
	}
	for _, m := range byName {
		set.Manifests = append(set.Manifests, m)
	}
	sort.Slice(set.Manifests, func(i, j int) bool { return set.Manifests[i].Name < set.Manifests[j].Name })

	// Help captures run concurrently so N tools cost ~one timeout, not N.
	var wg sync.WaitGroup
	for _, m := range set.Manifests {
		if m.Help == nil || !m.CommandFound() {
			continue
		}
		wg.Add(1)
		go func(m *Manifest) {
			defer wg.Done()
			m.CaptureHelp(ctx)
		}(m)
	}
	wg.Wait()
	return set
}

// Dirs lists the manifest directories for workingDir in precedence order:
// project (.opencode/tools, .agents/tools from the working dir up to the git
// worktree root), global (~/.config/opencode/tools, ~/.agents/tools), then
// cliTools.paths. Only existing directories are returned.
func Dirs(workingDir string, cfg *config.CLIToolsConfig) []string {
	var dirs []string
	seen := map[string]bool{}
	push := func(d string) {
		clean := filepath.Clean(d)
		if seen[clean] {
			return
		}
		if info, err := os.Stat(clean); err != nil || !info.IsDir() {
			return
		}
		seen[clean] = true
		dirs = append(dirs, clean)
	}

	root := worktreeRoot(workingDir)
	current := workingDir
	for {
		push(filepath.Join(current, ".opencode", "tools"))
		push(filepath.Join(current, ".agents", "tools"))
		if current == root || current == filepath.Dir(current) {
			break
		}
		current = filepath.Dir(current)
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		push(filepath.Join(home, ".config", "opencode", "tools"))
		push(filepath.Join(home, ".agents", "tools"))
	}
	if cfg != nil {
		for _, p := range cfg.Paths {
			expanded := p
			if strings.HasPrefix(p, "~/") && home != "" {
				expanded = filepath.Join(home, p[2:])
			}
			if !filepath.IsAbs(expanded) {
				expanded = filepath.Join(workingDir, expanded)
			}
			push(expanded)
		}
	}
	return dirs
}

// scanDir loads every *.yaml / *.yml / *.json manifest directly inside dir.
func scanDir(dir, workingDir string) ([]*Manifest, []Diagnostic) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var found []*Manifest
	var bad []Diagnostic
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml", ".json":
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		m, err := Load(path, workingDir)
		if err != nil {
			bad = append(bad, Diagnostic{Path: path, Reason: err.Error()})
			continue
		}
		found = append(found, m)
	}
	return found, bad
}

// worktreeRoot walks up from dir to the nearest directory containing .git;
// falls back to dir itself.
func worktreeRoot(dir string) string {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
		current = parent
	}
}

// maxLoggedDiagnostics bounds the aggregated WARN line.
const maxLoggedDiagnostics = 10

func (s *Set) logSummary() {
	if s.Disabled {
		logging.Debug("CLI tools disabled by config or environment")
		return
	}
	var invalid []Diagnostic
	shadowed := 0
	for _, d := range s.Diagnostics {
		if d.Shadowed {
			shadowed++
			continue
		}
		invalid = append(invalid, d)
	}
	if len(s.Manifests) > 0 {
		names := make([]string, 0, len(s.Manifests))
		for _, m := range s.Manifests {
			names = append(names, m.Name)
		}
		logging.Info("Discovered CLI tools", "count", len(names), "tools", strings.Join(names, ", "))
	}
	if shadowed > 0 {
		logging.Debug("CLI tool manifests shadowed by a higher-precedence file", "count", shadowed)
	}
	if len(invalid) > 0 {
		shown := invalid
		if len(shown) > maxLoggedDiagnostics {
			shown = shown[:maxLoggedDiagnostics]
		}
		details := make([]string, 0, len(shown))
		for _, d := range shown {
			details = append(details, fmt.Sprintf("%s (%s)", d.Path, d.Reason))
		}
		summary := strings.Join(details, "; ")
		if len(invalid) > len(shown) {
			summary += fmt.Sprintf("; and %d more (see `opencode tools list`)", len(invalid)-len(shown))
		}
		logging.Warn("CLI tool manifests failed validation and were not loaded (fail closed)",
			"count", len(invalid), "loaded", len(s.Manifests), "invalid", summary)
	}
}

// OverrideForTest replaces the cached discovery result with the given
// manifests (no filesystem scan) and returns a function that restores
// normal discovery. Tests of the toolset builder and the MCP bridge use it
// to inject manifests without a working-directory fixture.
func OverrideForTest(manifests []*Manifest) (restore func()) {
	Invalidate()
	cacheOnce.Do(func() {})
	cacheLock.Lock()
	cached = &Set{Manifests: manifests}
	cacheLock.Unlock()
	return Invalidate
}
