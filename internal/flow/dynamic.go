package flow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/fileutil"
)

type dynamicAttendedKey struct{}
type dynamicDefinitionKey struct{}

// DynamicManifest is persisted before any step starts. Step transcripts live in DB.
type DynamicManifest struct {
	Version       int
	Standalone    bool
	SessionPrefix string
	RootSessionID string
	Agent         string
	Complexity    string
	Definition    Flow
	Prompt        string
	Steering      []string
	Terminated    bool
}

func DynamicDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, config.ProfileAgentsDirName(), "flows", "dynamic"), nil
}
func manifestPath(dir, root string) string {
	hash := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(hash[:])+".json")
}
func SaveDynamic(dir string, m DynamicManifest) error {
	if m.RootSessionID == "" {
		return fmt.Errorf("root session required")
	}
	m.Version = 1
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".dynamic-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), manifestPath(dir, m.RootSessionID))
}
func LoadDynamic(dir, root string) (DynamicManifest, error) {
	var m DynamicManifest
	data, err := os.ReadFile(manifestPath(dir, root))
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Version != 1 || m.RootSessionID != root {
		return m, fmt.Errorf("invalid dynamic flow manifest")
	}
	return m, nil
}

// ParseDynamic accepts a self-contained definition. References/includes cannot
// read arbitrary files while planning and primary step identity is immutable.
func ParseDynamic(data, id, primary, size string, subagents map[string]bool) (*Flow, error) {
	limits, ok := config.DynamicFlowLimits(size)
	if !ok {
		return nil, fmt.Errorf("invalid flow complexity %q", size)
	}
	if len(data) > maxFlowFileSize() {
		return nil, fmt.Errorf("dynamic definition exceeds size limit")
	}
	var ff flowFile
	dec := yaml.NewDecoder(bytes.NewBufferString(data))
	dec.KnownFields(true)
	if err := dec.Decode(&ff); err != nil {
		var fields *yaml.TypeError
		if errors.As(err, &fields) {
			for _, detail := range fields.Errors {
				if strings.Contains(detail, "field description not found in type flow.FlowSpec") {
					return nil, fmt.Errorf("%w; hint: description belongs at the document root, alongside flow, not under flow. For generated flows, omit description", err)
				}
			}
		}
		return nil, err
	}
	if len(ff.Include) > 0 {
		return nil, fmt.Errorf("dynamic flows must be self-contained")
	}
	if len(ff.Flow.Steps) > limits.MaxSteps {
		return nil, fmt.Errorf("%s permits at most %d steps", size, limits.MaxSteps)
	}
	for i := range ff.Flow.Steps {
		step := &ff.Flow.Steps[i]
		if strings.Contains(step.Prompt, "!`") {
			return nil, fmt.Errorf("step %s: generated prompts cannot execute shell markup; use the permission-checked bash tool inside the step", step.ID)
		}
		if step.Agent == "" {
			step.Agent = primary
		}
		if step.Agent != primary && !subagents[step.Agent] {
			return nil, fmt.Errorf("step %s must use %s or a configured subagent", step.ID, primary)
		}
		if step.Model != "" || step.ReasoningEffort != "" || step.Context != nil || step.Compact != nil {
			return nil, fmt.Errorf("step %s cannot override model, context or compaction", step.ID)
		}
		if len(step.Extends) > 0 || step.LangfusePromptPath != "" || step.LangfusePromptLabel != "" {
			return nil, fmt.Errorf("step %s must carry its own prompt", step.ID)
		}
		if step.MaxTurns == 0 {
			step.MaxTurns = limits.MaxTurns
		}
		if step.MaxTurns < 1 || step.MaxTurns > limits.MaxTurns {
			return nil, fmt.Errorf("step %s exceeds turn limit %d", step.ID, limits.MaxTurns)
		}
		if step.MaxIterations == 0 {
			step.MaxIterations = limits.MaxIterations
		}
		if step.MaxIterations < 1 || step.MaxIterations > limits.MaxIterations {
			return nil, fmt.Errorf("step %s exceeds iteration limit %d", step.ID, limits.MaxIterations)
		}
		if step.Interactive || step.Interaction != nil {
			return nil, fmt.Errorf("TUI steps use the regular question tool; omit bridge interaction")
		}
		if step.Output != nil {
			b, _ := json.Marshal(step.Output)
			if strings.Contains(string(b), `"$ref"`) {
				return nil, fmt.Errorf("output schemas must be self-contained")
			}
		}
		if step.Fallback == nil {
			step.Fallback = &Fallback{OnTurnsExhausted: OnTurnsExhaustedFail}
		} else {
			step.Fallback.OnTurnsExhausted = OnTurnsExhaustedFail
		}
		if step.Fallback != nil && (step.Fallback.Retry > limits.MaxIterations) {
			return nil, fmt.Errorf("fallback retry exceeds complexity limit")
		}
	}
	ff.Flow.Session.ResumeOnFailure = true
	ff.Flow.MaxFallbackEntries = limits.MaxIterations
	f := &Flow{ID: id, Name: ff.Name, Description: ff.Description, Spec: ff.Flow}
	if err := validateFlowID(id); err != nil {
		return nil, err
	}
	if err := validateFlow(f); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, s := range f.Spec.Steps {
			if s.ID == id {
				for _, r := range s.Rules {
					visit(r.Then)
				}
				if s.Fallback != nil {
					visit(s.Fallback.To)
				}
			}
		}
	}
	visit(f.Spec.Steps[0].ID)
	for _, s := range f.Spec.Steps {
		if !seen[s.ID] {
			return nil, fmt.Errorf("step %s is unreachable; add explicit routing rules", s.ID)
		}
	}
	return f, nil
}

// ValidateDynamicAmendment requires all entered steps to retain their complete
// definition and position. New topology can refer to them but cannot rewrite history.
func ValidateDynamicAmendment(old, next *Flow, entered map[string]bool) error {
	if old.ID != next.ID {
		return fmt.Errorf("flow identity cannot change")
	}
	for i, step := range old.Spec.Steps {
		if entered[step.ID] && (i >= len(next.Spec.Steps) || !reflect.DeepEqual(step, next.Spec.Steps[i])) {
			return fmt.Errorf("entered step %s is immutable", step.ID)
		}
	}
	return nil
}

// HasPendingWork reports unfinished or routed-but-not-entered steps.
func HasPendingWork(states []db.FlowState, f *Flow) bool { return hasResumableWork(states, f, true) }

// acquireRunOwnership is shared by standalone and TUI execution. Unlike a stale
// running database row, an OS lock distinguishes a live local owner from a crash.
func acquireRunOwnership(root string) (*fileutil.Lock, error) {
	dir, err := DynamicDirectory()
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	scope := root
	if cfg := config.Get(); cfg != nil {
		scope = config.WorkingDirectory() + "|" + cfg.Data.Directory + "|" + root
	}
	return fileutil.Acquire(manifestPath(dir, scope) + ".run.lock")
}
