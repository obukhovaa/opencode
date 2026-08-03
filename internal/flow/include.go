package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/logging"
)

// templatePrefix marks a top-level key in an included file as a step
// template rather than a flow, mirroring GitLab CI's hidden-job
// convention (`.job:`). A key without the prefix contributes nothing.
const templatePrefix = "."

// IncludeEntry is one entry of a flow's top-level `include:` list. Only
// the `local:` kind is supported; the key is nevertheless required (rather
// than accepting a bare path string) so adding a second kind later is not
// a breaking change to files written now. See design D1.
//
// Paths are ROOT-relative — resolved against config.WorkingDir, not
// against the including file's directory — exactly as GitLab CI's
// `include:local:` is. Note the asymmetry with an output-schema `$ref`,
// which stays relative to the file that declares it. See design D5.
type IncludeEntry struct {
	Local string `yaml:"local,omitempty"`
}

// UnmarshalYAML rejects unsupported include kinds instead of decoding
// them into an empty entry. An ignored include would leave every
// `extends` that depended on it unresolvable, and the resulting error
// would point at the step rather than at the bad include line.
func (e *IncludeEntry) UnmarshalYAML(node *yaml.Node) error {
	var raw map[string]string
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("include entry must be a mapping with a %q key (got %v)", "local", err)
	}
	keys := sortedKeys(raw)
	for _, k := range keys {
		if k != "local" {
			return fmt.Errorf("unsupported include kind %q: only %q includes are supported", k, "local")
		}
	}
	local := strings.TrimSpace(raw["local"])
	if local == "" {
		return fmt.Errorf("include entry has an empty %q path", "local")
	}
	e.Local = local
	return nil
}

// stepTemplate is the reusable subset of a Step that an included file may
// contribute. It is deliberately its own type rather than an alias of
// Step: `id`, `interactive`, `interaction` (and the orchestrator-only
// `resume_after`, which Step does not model at all) MUST NOT be
// inheritable, and modelling the template as a Step would quietly make
// every one of them — plus every field added later — inheritable. See
// design D4.
//
// Keys records the template's RAW declared top-level keys. Without it an
// explicit zero (`maxTurns: 0`, `agent: ""`, `session: {fork: false}`)
// is indistinguishable from an omitted key on a typed struct, so a later
// template could not override an earlier one back to a zero value.
type stepTemplate struct {
	Name string              `yaml:"-"`
	Keys map[string]struct{} `yaml:"-"`

	Agent         string       `yaml:"agent,omitempty"`
	Session       StepSession  `yaml:"session,omitempty"`
	Prompt        string       `yaml:"prompt,omitempty"`
	Output        *StepOutput  `yaml:"output,omitempty"`
	Rules         []Rule       `yaml:"rules,omitempty"`
	Fallback      *Fallback    `yaml:"fallback,omitempty"`
	MaxTurns      int          `yaml:"maxTurns,omitempty"`
	MaxIterations int          `yaml:"maxIterations,omitempty"`
	Timeout       string       `yaml:"timeout,omitempty"`
	Compact       *StepCompact `yaml:"compact,omitempty"`
}

// inheritableStepKeys is the set of step keys a template may declare, in
// the order the merge applies them. Every other key that Step models —
// `id`, `interactive`, `interaction` — plus the orchestrator-only
// `resume_after` is rejected by name; see nonInheritableStepKeys.
var inheritableStepKeys = []string{
	"agent",
	"prompt",
	"session",
	"output",
	"rules",
	"fallback",
	"maxTurns",
	"maxIterations",
	"timeout",
	"compact",
}

// nonInheritableStepKeys maps each key a template MUST NOT declare to the
// reason, which is quoted in the load error. The reason is part of the
// contract, not decoration: the flow file has a SECOND consumer (the
// c2-agent orchestrator) which parses it with its own structs and never
// resolves templates, so a key it reads must stay in the flow file where
// it can see it.
var nonInheritableStepKeys = map[string]string{
	"id":           "a step's identity must stay in the flow (two flows extending one template would collide, and the flow file would no longer show which steps it has)",
	"interactive":  "the orchestrator reads it to bind a reviewer; a template would leave it seeing a non-interactive step and start a job with nobody bound to answer",
	"interaction":  "the orchestrator reads it to bind a reviewer; a template would leave it seeing a non-interactive step and start a job with nobody bound to answer",
	"resume_after": "it is read by the orchestrator only and is not modelled by the flow engine at all, so a template would silently drop it",
}

// resolveStepIncludes reads the flow's `include:` entries, decodes their
// templates and merges each step's `extends:` into that step. It runs
// inside parseFlowFile BEFORE the flow's own $ref resolution and BEFORE
// validateFlow, so a merged step is validated exactly as an inline one
// and a template cannot smuggle a step shape past validation (design D3).
//
// rawSteps carries each step's RAW declared keys, positionally aligned
// with steps; see stepRawKeys for why a second parse is needed.
func resolveStepIncludes(flowPath string, includes []IncludeEntry, steps []Step, rawSteps []map[string]struct{}) error {
	templates := map[string]*stepTemplate{}
	for _, entry := range includes {
		file := resolveIncludePath(entry.Local)
		loaded, err := loadStepTemplates(file)
		if err != nil {
			return err
		}
		for name, tmpl := range loaded {
			if _, dup := templates[name]; dup {
				logging.Warn("Step template redefined by a later include, last one wins",
					"flow", flowPath, "template", name, "file", file)
			}
			templates[name] = tmpl
		}
	}

	for i := range steps {
		if len(steps[i].Extends) == 0 {
			continue
		}
		applied := make([]*stepTemplate, 0, len(steps[i].Extends))
		for _, name := range steps[i].Extends {
			tmpl, ok := templates[name]
			if !ok {
				return fmt.Errorf("%w: step %q extends %q, which no included file defines (available: %s)",
					ErrUnknownTemplate, steps[i].ID, name, availableTemplates(templates))
			}
			applied = append(applied, tmpl)
		}
		var declared map[string]struct{}
		if i < len(rawSteps) {
			declared = rawSteps[i]
		}
		mergeTemplates(&steps[i], declared, applied)
	}
	return nil
}

// mergeTemplates seeds step from the templates and lets the step's own
// declared keys win. The merge is shallow per top-level key: `output`,
// `rules` and `fallback` are single values, so a step overriding one
// replaces the whole block (design D2). Templates apply left to right,
// later overriding earlier.
//
// declared is the step's set of RAW declared keys and is what decides
// what overrides — NOT the decoded values, which cannot tell an explicit
// zero from an omitted key.
func mergeTemplates(step *Step, declared map[string]struct{}, templates []*stepTemplate) {
	for _, key := range inheritableStepKeys {
		if _, own := declared[key]; own {
			continue
		}
		// Last template declaring the key wins.
		var src *stepTemplate
		for _, tmpl := range templates {
			if _, ok := tmpl.Keys[key]; ok {
				src = tmpl
			}
		}
		if src == nil {
			continue
		}
		switch key {
		case "agent":
			step.Agent = src.Agent
		case "prompt":
			step.Prompt = src.Prompt
		case "session":
			step.Session = src.Session
		case "output":
			step.Output = src.Output
		case "rules":
			step.Rules = src.Rules
		case "fallback":
			step.Fallback = src.Fallback
		case "maxTurns":
			step.MaxTurns = src.MaxTurns
		case "maxIterations":
			step.MaxIterations = src.MaxIterations
		case "timeout":
			step.Timeout = src.Timeout
		case "compact":
			step.Compact = src.Compact
		}
	}
}

// loadStepTemplates reads one included file and returns its templates,
// keyed by their `.`-prefixed name. The per-file size cap
// (OPENCODE_MAX_FLOW_FILE_SIZE) applies to EACH included file, so
// `include` is not a way around it (design D6).
func loadStepTemplates(path string) (map[string]*stepTemplate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading included file %q: %v", ErrInvalidInclude, path, err)
	}
	if limit := maxFlowFileSize(); len(data) > limit {
		return nil, fmt.Errorf("%w: included file %q exceeds %d bytes (raise via OPENCODE_MAX_FLOW_FILE_SIZE)",
			ErrInvalidInclude, path, limit)
	}

	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: parsing included file %q: %v", ErrInvalidInclude, path, err)
	}

	baseDir := filepath.Dir(path)
	templates := map[string]*stepTemplate{}
	for _, name := range sortedKeys(doc) {
		// A template file is a leaf: it contributes templates and
		// nothing else (design D6).
		if name == "include" || name == "extends" {
			return nil, fmt.Errorf("%w: included file %q declares %q; a template file may not itself include or extend",
				ErrInvalidInclude, path, name)
		}
		if !strings.HasPrefix(name, templatePrefix) {
			continue
		}
		node := doc[name]
		tmpl, err := decodeStepTemplate(name, &node, path, baseDir)
		if err != nil {
			return nil, err
		}
		templates[name] = tmpl
	}
	return templates, nil
}

// decodeStepTemplate decodes one template, rejecting keys that may not be
// inherited, and resolves its output-schema $ref against the TEMPLATE
// file's own directory — before the merge, because after merging there is
// no per-key provenance left and the flow's own $ref loop knows only the
// flow's directory (design D3).
func decodeStepTemplate(name string, node *yaml.Node, path, baseDir string) (*stepTemplate, error) {
	// Decode into a raw map first so an unknown key is VISIBLE: a typed
	// decode drops it silently, which is exactly how `flow.session`
	// typos used to pass unnoticed.
	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: template %q in %q must be a mapping of step keys: %v",
			ErrInvalidTemplate, name, path, err)
	}

	keys := map[string]struct{}{}
	for _, key := range sortedKeys(raw) {
		if key == "include" || key == "extends" {
			return nil, fmt.Errorf("%w: template %q in %q declares %q; templates are leaves and may not compose (design D6)",
				ErrInvalidTemplate, name, path, key)
		}
		if reason, forbidden := nonInheritableStepKeys[key]; forbidden {
			return nil, fmt.Errorf("%w: template %q in %q must not declare %q: %s",
				ErrInvalidTemplate, name, path, key, reason)
		}
		if !isInheritableStepKey(key) {
			return nil, fmt.Errorf("%w: template %q in %q declares unknown step key %q (inheritable keys: %s)",
				ErrInvalidTemplate, name, path, key, strings.Join(inheritableStepKeys, ", "))
		}
		keys[key] = struct{}{}
	}

	tmpl := &stepTemplate{}
	if err := node.Decode(tmpl); err != nil {
		return nil, fmt.Errorf("%w: template %q in %q: %v", ErrInvalidTemplate, name, path, err)
	}
	tmpl.Name = name
	tmpl.Keys = keys

	if tmpl.Output != nil && tmpl.Output.Schema != nil {
		resolved, err := format.ResolveSchemaRef(tmpl.Output.Schema, baseDir)
		if err != nil {
			return nil, fmt.Errorf("%w: resolving output schema $ref for template %q in %q: %v",
				ErrInvalidTemplate, name, path, err)
		}
		// Copy rather than mutate: the same template value may be
		// merged into several steps, and callers must not share the
		// resolved map with the file-level decode.
		tmpl.Output = &StepOutput{Schema: resolved}
	}
	return tmpl, nil
}

// resolveIncludePath resolves an include's `local:` path. Absolute paths
// are honoured as-is; a relative path is ROOT-relative, resolved against
// config.WorkingDir — the same base discoverCustomPathFlows uses for
// relative flowPaths entries (design D5).
//
// config.Get() is used rather than config.WorkingDirectory(), which
// panics when no config is loaded: parseFlowFile is reachable from tests
// and from tooling that never calls config.Load. With no config the base
// is empty and a relative include falls back to the process working
// directory.
func resolveIncludePath(local string) string {
	if filepath.IsAbs(local) {
		return filepath.Clean(local)
	}
	base := ""
	if cfg := config.Get(); cfg != nil {
		base = cfg.WorkingDir
	}
	if base == "" {
		return filepath.Clean(local)
	}
	return filepath.Join(base, local)
}

// stepRawKeys re-parses the flow bytes to recover each step's RAW
// declared top-level keys, positionally aligned with the typed steps.
//
// The typed decode cannot supply this: on a struct an explicit zero is
// indistinguishable from an absent key, and the affected fields are
// exactly the inheritable ones — `maxTurns: 0`, `maxIterations: 0`,
// `timeout: ""`, `agent: ""`, `prompt: ""`, and `session: {fork: false}`
// (Session is a VALUE type, so a false fork reads as unset). Only
// `compact` is safe, being a pointer.
//
// A custom Step.UnmarshalYAML could record the same key set but could not
// perform the merge — the templates live outside the step's YAML node and
// are unreachable from it — so the merge would still happen here. A
// second parse of the same bytes is the established pattern in this file:
// validateFlowSessionKeys already does it.
func stepRawKeys(data []byte) []map[string]struct{} {
	var raw struct {
		Flow struct {
			Steps []map[string]yaml.Node `yaml:"steps"`
		} `yaml:"flow"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Structural errors are surfaced by the typed decode in
		// parseFlowFile; nothing to add here.
		return nil
	}
	result := make([]map[string]struct{}, len(raw.Flow.Steps))
	for i, step := range raw.Flow.Steps {
		keys := make(map[string]struct{}, len(step))
		for key := range step {
			keys[key] = struct{}{}
		}
		result[i] = keys
	}
	return result
}

// stepsDeclareExtends reports whether any step declares `extends:`. A
// step extending with no include at all must fail loudly rather than run
// with an empty prompt, so that case still enters resolveStepIncludes.
func stepsDeclareExtends(steps []Step) bool {
	for _, step := range steps {
		if len(step.Extends) > 0 {
			return true
		}
	}
	return false
}

func isInheritableStepKey(key string) bool {
	for _, k := range inheritableStepKeys {
		if k == key {
			return true
		}
	}
	return false
}

func availableTemplates(templates map[string]*stepTemplate) string {
	if len(templates) == 0 {
		return "none — the flow declares no include, or its includes define no `.`-prefixed template"
	}
	return strings.Join(sortedKeys(templates), ", ")
}

// sortedKeys returns a map's keys in sorted order so error messages are
// deterministic when several keys are at fault.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
