package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	// defaultMaxFlowFileSize is the fallback ceiling on a single flow
	// YAML file when OPENCODE_MAX_FLOW_FILE_SIZE is unset / malformed.
	// Historically 100 KB — raised to 300 KB after real-world flows
	// (multi-lane routers with per-step blocker-resolution preludes)
	// grew past the old limit and were silently dropped from the
	// registry. See docs/flows.md#flow-file-size-limit.
	defaultMaxFlowFileSize = 300 * 1024 // 300KB

	maxNameLength   = 64
	maxStepIDLength = 64
)

// maxFlowFileSize returns the byte ceiling on a single flow YAML file.
// Sourced from OPENCODE_MAX_FLOW_FILE_SIZE (accepts a raw int in bytes
// or an SI-style suffix: `NN`, `NNk`/`NNKB`, `NNm`/`NNMB`; case-
// insensitive). Parsed once on first call. Falls back to
// defaultMaxFlowFileSize when unset, malformed, or non-positive; the
// malformed / non-positive path emits a WARN so operators can spot
// typos in Helm values / env manifests.
func maxFlowFileSize() int {
	maxFlowFileSizeOnce.Do(func() {
		maxFlowFileSizeVal = defaultMaxFlowFileSize
		raw := strings.TrimSpace(os.Getenv("OPENCODE_MAX_FLOW_FILE_SIZE"))
		if raw == "" {
			return
		}
		parsed, err := parseByteSize(raw)
		if err != nil {
			logging.Warn("Invalid OPENCODE_MAX_FLOW_FILE_SIZE; falling back to default",
				"value", raw, "default_bytes", defaultMaxFlowFileSize, "err", err)
			return
		}
		if parsed <= 0 {
			logging.Warn("OPENCODE_MAX_FLOW_FILE_SIZE must be positive; falling back to default",
				"value", raw, "default_bytes", defaultMaxFlowFileSize)
			return
		}
		maxFlowFileSizeVal = parsed
	})
	return maxFlowFileSizeVal
}

var (
	maxFlowFileSizeOnce sync.Once
	maxFlowFileSizeVal  int
)

// parseByteSize parses a byte-size literal. Accepts:
//   - raw integer ("307200") — interpreted as bytes
//   - integer + k/kb/kib suffix ("300k", "300KB") — ×1024
//   - integer + m/mb/mib suffix ("2m", "2MB") — ×1024²
//
// Suffixes are case-insensitive; whitespace between number and suffix
// is tolerated. Fractional / negative values are rejected.
func parseByteSize(raw string) (int, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	mult := 1
	// Longest suffix wins so "kib" doesn't trigger the "k" branch first.
	suffixes := []struct {
		suffix string
		mult   int
	}{
		{"kib", 1024}, {"kb", 1024}, {"k", 1024},
		{"mib", 1024 * 1024}, {"mb", 1024 * 1024}, {"m", 1024 * 1024},
	}
	for _, sfx := range suffixes {
		if strings.HasSuffix(s, sfx.suffix) {
			mult = sfx.mult
			s = strings.TrimSpace(strings.TrimSuffix(s, sfx.suffix))
			break
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not an integer: %v", err)
	}
	return n * mult, nil
}

var (
	kebabCaseRegex = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

	flowCache     map[string]Flow
	flowCacheLock sync.Mutex
	flowCacheInit bool
)

// flowFile represents the raw YAML structure of a flow file.
type flowFile struct {
	Name        string   `yaml:"name"`
	Disabled    bool     `yaml:"disabled,omitempty"`
	Description string   `yaml:"description"`
	Flow        FlowSpec `yaml:"flow"`
}

// Get returns a flow by ID, or ErrFlowNotFound.
func Get(id string) (*Flow, error) {
	flows := state()
	f, ok := flows[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrFlowNotFound, id)
	}
	return &f, nil
}

// All returns all discovered flows.
func All() []Flow {
	flows := state()
	result := make([]Flow, 0, len(flows))
	for _, f := range flows {
		result = append(result, f)
	}
	return result
}

// Invalidate clears the cached flows, forcing re-discovery on next access.
func Invalidate() {
	flowCacheLock.Lock()
	defer flowCacheLock.Unlock()
	flowCache = nil
	flowCacheInit = false
}

func state() map[string]Flow {
	flowCacheLock.Lock()
	defer flowCacheLock.Unlock()
	if !flowCacheInit {
		flowCache = discoverFlows()
		flowCacheInit = true
	}
	return flowCache
}

func discoverFlows() map[string]Flow {
	flows := make(map[string]Flow)

	// Project flows have higher priority — discover first, so they win on conflict
	projectFlows := discoverProjectFlows()
	for _, f := range projectFlows {
		if _, exists := flows[f.ID]; exists {
			logging.Warn("Duplicate flow ID, keeping first occurrence", "id", f.ID, "location", f.Location)
			continue
		}
		flows[f.ID] = f
	}

	// Global flows — only add if not already found
	globalFlows := discoverGlobalFlows()
	for _, f := range globalFlows {
		if _, exists := flows[f.ID]; exists {
			logging.Warn("Flow already defined in project, skipping global", "id", f.ID, "location", f.Location)
			continue
		}
		flows[f.ID] = f
	}

	// Custom flow paths (cfg.FlowPaths) are discovered after the built-in
	// dirs. Their IDs are namespaced (`<namespace>/<basename>`), while
	// built-in discovery derives IDs from file basenames — which can never
	// contain a path separator — so a custom-path flow can never collide
	// with or spoof a shared flow ID. The duplicate guard below is
	// therefore defensive only (e.g. two flowPaths entries resolving to
	// the same directory), mirroring the built-in dup handling above.
	customFlows := discoverCustomPathFlows(config.Get())
	for _, f := range customFlows {
		if _, exists := flows[f.ID]; exists {
			logging.Warn("Duplicate flow ID, keeping first occurrence", "id", f.ID, "location", f.Location)
			continue
		}
		flows[f.ID] = f
	}

	return flows
}

func discoverProjectFlows() []Flow {
	cfg := config.Get()
	var result []Flow

	projectDirs := []string{
		filepath.Join(cfg.WorkingDir, ".opencode", "flows"),
		filepath.Join(cfg.WorkingDir, ".agents", "flows"),
	}

	for _, dir := range projectDirs {
		result = append(result, scanFlowDirectory(dir)...)
	}
	return result
}

func discoverGlobalFlows() []Flow {
	home, err := os.UserHomeDir()
	if err != nil {
		logging.Warn("Could not determine home directory for global flow discovery", "error", err)
		return nil
	}

	var result []Flow
	globalDirs := []string{
		filepath.Join(home, ".config", "opencode", "flows"),
		filepath.Join(home, ".agents", "flows"),
	}

	for _, dir := range globalDirs {
		result = append(result, scanFlowDirectory(dir)...)
	}
	return result
}

// discoverCustomPathFlows scans the directories listed in cfg.FlowPaths
// for flow YAML definitions. It mirrors the agent registry's
// discoverCustomPathMarkdownAgents: "~/" is expanded to the home
// directory and relative paths are resolved against the working
// directory. Missing paths and non-directories are logged and skipped
// rather than failing discovery.
//
// Unlike built-in discovery, flows found here get a namespaced ID
// `<namespace>/<basename>` where the namespace is the basename of the
// flows directory's PARENT — e.g. /workspace/id/flows/fix-failing-tests.yaml
// becomes `id/fix-failing-tests`. Namespaced IDs occupy a disjoint key
// space from shared (slash-free) flow IDs, so team flows are addressable
// without ever shadowing a shared flow. Flows whose namespaced ID fails
// validateFlowID (non-kebab parent dir, over-long ID) are warned about
// and skipped.
func discoverCustomPathFlows(cfg *config.Config) []Flow {
	if cfg == nil || len(cfg.FlowPaths) == 0 {
		return nil
	}

	var result []Flow
	homeDir, _ := os.UserHomeDir()

	for _, flowPath := range cfg.FlowPaths {
		// Expand ~ to the home directory.
		expanded := flowPath
		if strings.HasPrefix(flowPath, "~/") && homeDir != "" {
			expanded = filepath.Join(homeDir, flowPath[2:])
		}

		// Resolve relative paths against the working directory.
		resolved := expanded
		if !filepath.IsAbs(expanded) {
			resolved = filepath.Join(cfg.WorkingDir, expanded)
		}

		// Clean the path so trailing slashes don't skew the namespace
		// derivation below: filepath.Dir on an uncleaned
		// "/workspace/id/flows/" yields ".../flows", which would derive
		// the namespace "flows" for every trailing-slash entry (and
		// collapse multiple teams into one colliding namespace).
		// filepath.Join already cleans the relative branch; absolute
		// entries need it explicitly.
		resolved = filepath.Clean(resolved)

		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			logging.Warn("Flow path not found or not a directory", "path", resolved)
			continue
		}

		namespace := filepath.Base(filepath.Dir(resolved))
		for _, f := range scanFlowDirectory(resolved) {
			nsID := namespace + "/" + f.ID
			if err := validateFlowID(nsID); err != nil {
				logging.Warn("Skipping custom-path flow with invalid namespaced ID",
					"id", nsID, "location", f.Location, "error", err)
				continue
			}
			f.ID = nsID
			result = append(result, f)
		}
	}

	return result
}

func scanFlowDirectory(dir string) []Flow {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		logging.Warn("Failed to read flow directory", "dir", dir, "error", err)
		return nil
	}

	var flows []Flow
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)
		f, err := parseFlowFile(path)
		if err != nil {
			logging.Warn("Failed to parse flow file", "path", path, "error", err)
			continue
		}
		flows = append(flows, *f)
	}
	return flows
}

func parseFlowFile(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading flow file: %w", err)
	}

	if limit := maxFlowFileSize(); len(data) > limit {
		return nil, fmt.Errorf("%w: file exceeds %d bytes (raise via OPENCODE_MAX_FLOW_FILE_SIZE)", ErrInvalidYAML, limit)
	}

	var ff flowFile
	if err := yaml.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}

	// Reject typos in the session block. The typed decode silently
	// drops unknown keys (e.g. `resume_on_fail` instead of
	// `resume_on_failure`), which would leave the flow on the default
	// behavior with no signal to the author. Parse the raw YAML
	// separately and enforce the allow-list. Keep this check narrow —
	// global strict-mode decoding would break legitimate top-level
	// extension keys (`description`, future additions).
	if err := validateFlowSessionKeys(data); err != nil {
		return nil, err
	}

	// Derive ID from filename (basename without extension)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	id := strings.TrimSuffix(base, ext)

	if err := validateFlowID(id); err != nil {
		return nil, err
	}

	// Resolve $ref in step output schemas
	baseDir := filepath.Dir(path)
	for i, step := range ff.Flow.Steps {
		if step.Output != nil && step.Output.Schema != nil {
			resolved, err := format.ResolveSchemaRef(step.Output.Schema, baseDir)
			if err != nil {
				return nil, fmt.Errorf("resolving output schema $ref for step %q: %w", step.ID, err)
			}
			ff.Flow.Steps[i].Output.Schema = resolved
		}
	}

	flow := Flow{
		ID:          id,
		Name:        ff.Name,
		Disabled:    ff.Disabled,
		Description: ff.Description,
		Spec:        ff.Flow,
		Location:    path,
	}

	if err := validateFlow(&flow); err != nil {
		return nil, fmt.Errorf("validating flow %q: %w", id, err)
	}

	return &flow, nil
}

// validateFlowID checks a flow ID is valid: either a single kebab-case
// segment (shared flows — the ID is the file basename, which can never
// contain a path separator) or exactly two kebab-case segments joined
// by one "/" (namespaced custom-path flows, `<namespace>/<basename>` —
// see discoverCustomPathFlows). Because the two forms are disjoint, a
// namespaced ID can never collide with or spoof a shared flow ID. The
// maxNameLength cap applies to the full ID, separator included.
func validateFlowID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty ID", ErrInvalidFlowName)
	}
	if len(id) > maxNameLength {
		return fmt.Errorf("%w: %q exceeds %d characters", ErrInvalidFlowName, id, maxNameLength)
	}
	segments := strings.Split(id, "/")
	if len(segments) > 2 {
		return fmt.Errorf("%w: %q may contain at most one '/' (<namespace>/<name>)", ErrInvalidFlowName, id)
	}
	for _, segment := range segments {
		if !kebabCaseRegex.MatchString(segment) {
			return fmt.Errorf("%w: %q must be kebab-case (lowercase alphanumeric with hyphens), optionally namespaced as <namespace>/<name>", ErrInvalidFlowName, id)
		}
	}
	return nil
}

// validateFlow validates the entire flow definition.
func validateFlow(f *Flow) error {
	if len(f.Spec.Steps) == 0 {
		return ErrNoSteps
	}

	// Build a set of step IDs for reference validation
	stepIDs := make(map[string]bool, len(f.Spec.Steps))
	for _, step := range f.Spec.Steps {
		if err := validateStepID(step.ID); err != nil {
			return err
		}
		if stepIDs[step.ID] {
			return fmt.Errorf("%w: %q", ErrDuplicateStepID, step.ID)
		}
		stepIDs[step.ID] = true

		// MaxTurns is optional; 0 means "inherit from agent" (Go zero-value /
		// field omitted in YAML). Negative values are always invalid.
		if step.MaxTurns < 0 {
			return fmt.Errorf("%w: step %q maxTurns must be >= 0 (got %d; 0 means inherit from agent)", ErrInvalidMaxTurns, step.ID, step.MaxTurns)
		}
		// MaxIterations is optional; 0 means unbounded. Negative values are invalid.
		if step.MaxIterations < 0 {
			return fmt.Errorf("%w: step %q maxIterations must be >= 0 (got %d; 0 means unbounded)", ErrInvalidMaxIterations, step.ID, step.MaxIterations)
		}
		// Timeout, when set, must parse cleanly and be non-negative. The
		// runtime falls back gracefully on parse errors (env-var default,
		// then unwrapped ctx), but surfacing the typo at load time is
		// much more debuggable than discovering the silent fallback at
		// step run time.
		if _, err := step.TimeoutDuration(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidYAML, err)
		}
	}

	// Validate rule and fallback references
	thenTargets := make(map[string]int) // track how many rules target each step
	for _, step := range f.Spec.Steps {
		for _, rule := range step.Rules {
			if !stepIDs[rule.Then] {
				return fmt.Errorf("%w: step %q rule references %q", ErrInvalidRule, step.ID, rule.Then)
			}
			thenTargets[rule.Then]++
		}
		if step.Fallback != nil && step.Fallback.To != "" {
			if !stepIDs[step.Fallback.To] {
				return fmt.Errorf("%w: step %q fallback references %q", ErrInvalidFallback, step.ID, step.Fallback.To)
			}
		}
	}

	// Warn about potential convergence (multiple rules targeting same step)
	for targetID, count := range thenTargets {
		if count > 1 {
			logging.Warn("Multiple rules target the same step (potential diamond convergence, first-to-arrive wins)",
				"flow", f.ID, "target_step", targetID, "source_count", count)
		}
	}

	return nil
}

// knownSessionKeys is the allow-list of keys permitted under
// `flow.session:` in a flow YAML. Adding a new field to FlowSession
// MUST also extend this set; otherwise the new key will be silently
// dropped during typed decode and the author gets no signal that the
// flow runtime didn't see their config.
var knownSessionKeys = map[string]struct{}{
	"prefix":            {},
	"resume_on_failure": {},
}

// validateFlowSessionKeys parses the raw flow YAML, reaches into
// `flow.session:`, and returns ErrInvalidYAML if any key in that
// block is not in knownSessionKeys. Returns nil for a missing or
// non-map `flow.session` block (the typed decode catches structural
// errors elsewhere).
func validateFlowSessionKeys(data []byte) error {
	var raw struct {
		Flow struct {
			Session map[string]any `yaml:"session"`
		} `yaml:"flow"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Structural parse errors are surfaced by the typed
		// yaml.Unmarshal call above; here we just bail and let that
		// path produce the user-visible error.
		return nil
	}
	for key := range raw.Flow.Session {
		if _, ok := knownSessionKeys[key]; !ok {
			return fmt.Errorf("%w: unknown key %q in flow.session", ErrInvalidYAML, key)
		}
	}
	return nil
}

// validateStepID checks a step ID is valid kebab-case and within length limit.
func validateStepID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty step ID", ErrInvalidStepID)
	}
	if len(id) > maxStepIDLength {
		return fmt.Errorf("%w: %q exceeds %d characters", ErrInvalidStepID, id, maxStepIDLength)
	}
	if !kebabCaseRegex.MatchString(id) {
		return fmt.Errorf("%w: %q must be kebab-case", ErrInvalidStepID, id)
	}
	return nil
}
