package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	maxFlowFileSize = 100 * 1024 // 100KB
	maxNameLength   = 64
	maxStepIDLength = 64
)

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

	if len(data) > maxFlowFileSize {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrInvalidYAML, maxFlowFileSize)
	}

	var ff flowFile
	if err := yaml.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}

	// Derive ID from filename (basename without extension)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	id := strings.TrimSuffix(base, ext)

	if err := validateFlowID(id); err != nil {
		return nil, err
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

// validateFlowID checks the flow ID (derived from filename) is valid kebab-case.
func validateFlowID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty ID", ErrInvalidFlowName)
	}
	if len(id) > maxNameLength {
		return fmt.Errorf("%w: %q exceeds %d characters", ErrInvalidFlowName, id, maxNameLength)
	}
	if !kebabCaseRegex.MatchString(id) {
		return fmt.Errorf("%w: %q must be kebab-case (lowercase alphanumeric with hyphens)", ErrInvalidFlowName, id)
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
