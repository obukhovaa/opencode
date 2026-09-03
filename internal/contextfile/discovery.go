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

	// SkipDirs lists extra directories the walk must skip, absolute or
	// relative to workDir. NOT a user-facing config field (excluded from
	// the .opencode.json schema on purpose): callers populate it from the
	// configured data directory via config.EffectiveContextDiscovery so a
	// non-hidden data dir (`data.directory`) is never walked. Both
	// consumers — the prompt manifest and the disclosure wrapper — MUST go
	// through that accessor so manifest and injection agree.
	SkipDirs []string `json:"-" yaml:"-"`
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
// WalkDir (lexical, depth-first) order. Labels carries each file's
// manifest label (frontmatter description or first markdown heading),
// computed ONCE here — at discovery time, after the containment checks —
// so RenderManifest never touches the disk at prompt-build time and the
// manifest is structurally byte-stable for the process lifetime.
// Truncated is set when the maxFiles cap cut the walk short — walk order
// decides which files made the cut, and the manifest header notes the
// truncation.
type DiscoveryResult struct {
	Files     []string
	Labels    map[string]string // abs path -> label ("" entries omitted)
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
	// The containment reference for candidates: same Clean → EvalSymlinks
	// order containedInWorkDir uses. When the root itself cannot be
	// resolved, fall back to the cleaned root — candidates resolved below
	// it still compare correctly on lexical prefix.
	resolvedRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = r
	}
	// Extra skip directories (the configured data directory): compare by
	// absolute cleaned path so both relative ("opencode-data") and
	// absolute entries work.
	skipPaths := make(map[string]struct{}, len(cfg.SkipDirs))
	for _, dir := range cfg.SkipDirs {
		if dir == "" {
			continue
		}
		abs := dir
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		skipPaths[filepath.Clean(abs)] = struct{}{}
	}
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
			if _, skip := skipPaths[path]; skip {
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
		// Only regular files are candidates. A symlink (IsDir()==false,
		// so it reaches this branch) is rejected outright: a repo can
		// commit `docs/AGENTS.md -> ~/.ssh/id_rsa` and both the manifest
		// label extraction and the activation-time read would otherwise
		// follow it with no permission prompt.
		if !d.Type().IsRegular() {
			logging.Warn("Context discovery skipped non-regular file", "path", path, "type", d.Type().String())
			return nil
		}
		// Defense in depth: even a regular file can sit behind a
		// symlinked parent directory. Resolve the candidate and require
		// it stays strictly inside the resolved workDir — the same
		// order containedInWorkDir uses.
		resolved, resErr := filepath.EvalSymlinks(path)
		if resErr != nil || !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
			logging.Warn("Context discovery skipped file escaping the working directory", "path", path, "resolved", resolved, "workDir", resolvedRoot, "error", resErr)
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
		// Label computed here — once, behind the containment checks —
		// and cached with the walk result (see DiscoveryResult.Labels).
		if label := extractLabel(path); label != "" {
			if res.Labels == nil {
				res.Labels = make(map[string]string)
			}
			res.Labels[path] = label
		}
		return nil
	})
	return res
}

// ResolvedWithinRoot reports whether absPath — after symlink resolution —
// lies strictly inside root (itself symlink-resolved), using the same
// Clean → EvalSymlinks order containedInWorkDir applies to scoped
// entries. Fail-closed: a path that cannot be resolved (missing file,
// broken link) is NOT within the root. The disclosure wrapper re-checks
// this at activation time because discovery results are cached for the
// process lifetime and the filesystem can change underneath them.
func ResolvedWithinRoot(absPath, root string) bool {
	resolvedRoot := filepath.Clean(root)
	if r, err := filepath.EvalSymlinks(resolvedRoot); err == nil {
		resolvedRoot = r
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absPath))
	if err != nil {
		return false
	}
	return strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator))
}

// canonicalDiscoveryPath normalizes a path for owner/exclusion matching
// with the SAME normalization tryMarkProcessed uses for resolver dedup:
// EvalSymlinks plus unconditional lowercasing — macOS's default
// filesystem is case-insensitive, and a model-supplied tool path
// routinely differs in case from the WalkDir spelling. Paths that do not
// (fully) exist — a write into a new subdirectory, a differently-cased
// spelling on a case-sensitive filesystem — resolve their deepest
// existing ancestor so both sides of a comparison canonicalize
// consistently (macOS TempDirs live under the /var -> /private/var link).
func canonicalDiscoveryPath(p string) string {
	return strings.ToLower(resolveExistingPrefix(filepath.Clean(p)))
}

// resolveExistingPrefix EvalSymlinks the deepest existing ancestor of
// path and re-appends the untouched remainder.
func resolveExistingPrefix(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := filepath.Dir(path)
	if dir == path {
		return path
	}
	return filepath.Join(resolveExistingPrefix(dir), filepath.Base(path))
}

// FilterDiscovered subtracts from the discovered set every file the
// agent/step scoped context layers already inline into the system prompt
// (exact path entries and trailing-slash subtrees), mirroring the
// walk-level exclusion of global contextPaths matches. Layer
// participation follows ResolveForAgent exactly: a step `replace` drops
// the agent layer, so a file named only there is NOT inlined and stays a
// disclosure candidate. Both the manifest renderer and the disclosure
// wrapper MUST apply this filter with the same inputs so listing and
// injection agree; the result is deterministic per (agentCtx, stepCtx,
// vars), keeping the manifest byte-stable within a session.
func FilterDiscovered(discovered []string, agentCtx *AgentContext, stepCtx *StepContext, workDir string, vars TemplateVars) []string {
	if len(discovered) == 0 || (agentCtx == nil && stepCtx == nil) {
		return discovered
	}
	files := make(map[string]struct{})
	var prefixes []string
	add := func(entries []string) {
		for _, e := range entries {
			abs := filepath.Clean(filepath.Join(workDir, e))
			canon := canonicalDiscoveryPath(abs)
			if strings.HasSuffix(e, "/") {
				prefixes = append(prefixes, canon+string(os.PathSeparator))
				continue
			}
			files[canon] = struct{}{}
		}
	}
	replaced := false
	if stepCtx != nil && stepCtx.Paths != nil {
		add(filterScopedEntries(stepCtx.Paths, vars, workDir))
		replaced = normalizeMode(stepCtx.Mode, "step", vars.FlowStep) == ModeReplace
	}
	if !replaced && agentCtx != nil && agentCtx.Paths != nil {
		add(filterScopedEntries(agentCtx.Paths, vars, workDir))
	}
	if len(files) == 0 && len(prefixes) == 0 {
		return discovered
	}
	out := make([]string, 0, len(discovered))
	for _, f := range discovered {
		canon := canonicalDiscoveryPath(f)
		if _, inlined := files[canon]; inlined {
			continue
		}
		underPrefix := false
		for _, p := range prefixes {
			if strings.HasPrefix(canon, p) {
				underPrefix = true
				break
			}
		}
		if underPrefix {
			continue
		}
		out = append(out, f)
	}
	return out
}

// OwnersForPath returns the discovered nested context files whose owning
// directory lies on the upward path from dir to (but excluding) workDir,
// outermost-first — reproducing Claude Code's additive layering without
// mutating the system prompt (design D8). A dir equal to workDir (or
// outside it) owns nothing: the nested set is strictly below the root, so
// e.g. a grep with no path argument — which resolves to workDir — never
// activates anything. Files sharing one owning directory keep their
// discovery (lexical) order.
//
// Both sides of the comparison — the discovered owning directories and
// the model-supplied target dir — are canonicalized with the same
// EvalSymlinks+lowercase normalization the resolver's dedup uses
// (tryMarkProcessed): on the default case-insensitive macOS/Windows
// filesystems, the inner tool call succeeds with a differently-cased
// path, and a byte-exact comparison would silently miss the injection.
func OwnersForPath(dir string, discovered []string, workDir string) []string {
	root := canonicalDiscoveryPath(workDir)
	cur := canonicalDiscoveryPath(dir)
	if cur == root || !strings.HasPrefix(cur, root+string(os.PathSeparator)) {
		return nil
	}
	byDir := make(map[string][]string, len(discovered))
	for _, f := range discovered {
		d := canonicalDiscoveryPath(filepath.Dir(f))
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
