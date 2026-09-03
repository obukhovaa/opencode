package contextfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/opencode-ai/opencode/internal/logging"
)

// Default caps for the nested-context discovery walk and the disclosure
// byte budgets (design D6). Referenced by internal/config's
// viper.SetDefault calls so the config surface and the fallback used when
// no config was loaded can never drift apart.
const (
	DefaultDiscoveryMaxFiles        = 100
	DefaultDiscoveryMaxDepth        = 8
	DefaultDiscoveryMaxFileBytes    = 32768
	DefaultDiscoveryMaxSessionBytes = 131072
)

// DiscoveryConfig is the top-level `contextDiscovery` config object.
// Defined here (not in internal/config) so the config package references
// the leaf and never the reverse (design D1).
type DiscoveryConfig struct {
	Enabled         bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	MaxFiles        int  `json:"maxFiles,omitempty" yaml:"maxFiles,omitempty"`
	MaxDepth        int  `json:"maxDepth,omitempty" yaml:"maxDepth,omitempty"`
	MaxFileBytes    int  `json:"maxFileBytes,omitempty" yaml:"maxFileBytes,omitempty"`
	MaxSessionBytes int  `json:"maxSessionBytes,omitempty" yaml:"maxSessionBytes,omitempty"`
}

// DefaultDiscoveryConfig is the enabled-with-default-caps configuration
// used when no `contextDiscovery` block was loaded (a manually constructed
// config in tests; viper defaults make the block non-nil in real loads).
func DefaultDiscoveryConfig() DiscoveryConfig {
	return DiscoveryConfig{
		Enabled:         true,
		MaxFiles:        DefaultDiscoveryMaxFiles,
		MaxDepth:        DefaultDiscoveryMaxDepth,
		MaxFileBytes:    DefaultDiscoveryMaxFileBytes,
		MaxSessionBytes: DefaultDiscoveryMaxSessionBytes,
	}
}

// WithDefaults fills unset (zero) caps so a config that sets only
// `enabled: true` still gets bounded discovery. Exported for the
// progressive-disclosure wrapper, which enforces MaxFileBytes and
// MaxSessionBytes at activation time.
func (c DiscoveryConfig) WithDefaults() DiscoveryConfig {
	if c.MaxFiles <= 0 {
		c.MaxFiles = DefaultDiscoveryMaxFiles
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = DefaultDiscoveryMaxDepth
	}
	if c.MaxFileBytes <= 0 {
		c.MaxFileBytes = DefaultDiscoveryMaxFileBytes
	}
	if c.MaxSessionBytes <= 0 {
		c.MaxSessionBytes = DefaultDiscoveryMaxSessionBytes
	}
	return c
}

// DiscoveryResult is the outcome of one nested-context walk. Files are
// absolute paths strictly below workDir (depth >= 1), in deterministic
// WalkDir (lexical, depth-first) order. Truncated is set when the
// maxFiles cap cut the walk short — walk order decides which files made
// the cut, and the manifest header notes the truncation.
type DiscoveryResult struct {
	Files     []string
	Truncated bool
}

var (
	// discoveryCache memoizes the walk per workDir for the process
	// lifetime (design D6: once per process per workDir). Editing or
	// adding nested context files still requires a restart, matching the
	// resolver's staleness semantics.
	discoveryCache sync.Map // workDir string -> DiscoveryResult
	discoveryGroup singleflight.Group
)

// discoverySkipDirs mirrors the non-hidden entries of the hardcoded skip
// set in internal/llm/tools/ls.go shouldSkip(); hidden (dot-prefixed)
// directories — which also cover .git and the default `.opencode` data
// directory — are skipped by prefix. `.gitignore` honoring is a
// deliberate v1 non-goal (no gitignore library in go.mod; ripgrep is an
// external binary unsuitable for the prompt-build path); these names plus
// the caps bound over-discovery.
var discoverySkipDirs = map[string]struct{}{
	"__pycache__":  {},
	"node_modules": {},
	"dist":         {},
	"build":        {},
	"target":       {},
	"vendor":       {},
	"bin":          {},
	"obj":          {},
	"zig-out":      {},
	"coverage":     {},
	"logs":         {},
	"venv":         {},
	"env":          {},
	"tmp":          {},
	"temp":         {},
	"cache":        {},
}

// Discover walks the subtree strictly below workDir for files whose
// basename matches a file-type (non-trailing-slash) entry of the global
// contextPaths. Root-level matches stay the job of scoped resolution;
// files inside a trailing-slash entry's subtree keep today's
// inline-everything semantics and are excluded too. The result is cached
// by workDir for the process lifetime; a disabled config returns an empty
// result without touching the cache.
func Discover(workDir string, globalPaths []string, cfg DiscoveryConfig) DiscoveryResult {
	if !cfg.Enabled {
		return DiscoveryResult{}
	}
	if v, ok := discoveryCache.Load(workDir); ok {
		return v.(DiscoveryResult)
	}
	v, _, _ := discoveryGroup.Do(workDir, func() (any, error) {
		if cached, ok := discoveryCache.Load(workDir); ok {
			return cached, nil
		}
		res := walkForContextFiles(workDir, globalPaths, cfg.WithDefaults())
		discoveryCache.Store(workDir, res)
		return res, nil
	})
	return v.(DiscoveryResult)
}

func walkForContextFiles(workDir string, globalPaths []string, cfg DiscoveryConfig) DiscoveryResult {
	basenames := make(map[string]struct{})
	// Exact file entries are root-resolution's matches; trailing-slash
	// entries inline their whole subtree — both are excluded from the
	// progressive-disclosure candidate set.
	rootMatched := make(map[string]struct{})
	inlinedPrefixes := make([]string, 0)
	for _, entry := range globalPaths {
		abs := filepath.Clean(filepath.Join(workDir, entry))
		if strings.HasSuffix(entry, "/") {
			inlinedPrefixes = append(inlinedPrefixes, abs+string(os.PathSeparator))
			continue
		}
		basenames[filepath.Base(entry)] = struct{}{}
		rootMatched[abs] = struct{}{}
	}
	if len(basenames) == 0 {
		return DiscoveryResult{}
	}

	var res DiscoveryResult
	root := filepath.Clean(workDir)
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Per-file/dir errors are isolated: log and keep walking
			// (design D10) — a permission-denied subtree must not cost
			// the rest of the discovery set.
			logging.Warn("Context discovery walk error, skipping entry", "path", path, "error", err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		// depth = directory levels below workDir: 0 for a root-level
		// file, 1 for "a/AGENTS.md". Nested candidates need depth >= 1;
		// files deeper than MaxDepth are out.
		depth := strings.Count(rel, string(os.PathSeparator))
		if d.IsDir() {
			base := d.Name()
			if strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			if _, skip := discoverySkipDirs[base]; skip {
				return filepath.SkipDir
			}
			// Files inside this directory would sit at depth+1.
			if depth+1 > cfg.MaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if _, match := basenames[d.Name()]; !match {
			return nil
		}
		if depth < 1 {
			// The file sits at workDir root: scoped resolution's job,
			// not a disclosure candidate.
			return nil
		}
		if _, matched := rootMatched[path]; matched {
			return nil
		}
		for _, prefix := range inlinedPrefixes {
			if strings.HasPrefix(path, prefix) {
				return nil
			}
		}
		if len(res.Files) >= cfg.MaxFiles {
			res.Truncated = true
			return filepath.SkipAll
		}
		res.Files = append(res.Files, path)
		return nil
	})
	return res
}

// OwnersForPath returns the discovered nested context files whose owning
// directory lies on the upward path from dir to (but excluding) workDir,
// outermost-first — reproducing Claude Code's additive layering without
// mutating the system prompt (design D8). A dir equal to workDir (or
// outside it) owns nothing: the nested set is strictly below the root, so
// e.g. a grep with no path argument — which resolves to workDir — never
// activates anything. Files sharing one owning directory keep their
// discovery (lexical) order.
func OwnersForPath(dir string, discovered []string, workDir string) []string {
	root := filepath.Clean(workDir)
	cur := filepath.Clean(dir)
	if cur == root || !strings.HasPrefix(cur, root+string(os.PathSeparator)) {
		return nil
	}
	byDir := make(map[string][]string, len(discovered))
	for _, f := range discovered {
		d := filepath.Dir(f)
		byDir[d] = append(byDir[d], f)
	}
	// Collected innermost-first while walking up; emitted in reverse so
	// the outermost layer's files come first.
	var levels [][]string
	for cur != root {
		if files, ok := byDir[cur]; ok {
			levels = append(levels, files)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Hit the filesystem root without meeting workDir — the
			// prefix check above makes this unreachable, but guard
			// against an infinite loop regardless.
			return nil
		}
		cur = parent
	}
	var owners []string
	for i := len(levels) - 1; i >= 0; i-- {
		owners = append(owners, levels[i]...)
	}
	return owners
}
