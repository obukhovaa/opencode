package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

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
	// Report EVERY non-local key, not the first one encountered: a
	// GitLab-style `project:` include carries a `file:` alongside it, and
	// naming only one of them points the author at the wrong line (
	// `unsupported include kind "file"` for a `project:` entry).
	var unsupported []string
	for _, k := range sortedKeys(raw) {
		if k != "local" {
			unsupported = append(unsupported, fmt.Sprintf("%q", k))
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("unsupported include %s %s: only %q includes are supported",
			pluralise(len(unsupported), "kind", "kinds"), strings.Join(unsupported, ", "), "local")
	}
	local := strings.TrimSpace(raw["local"])
	if local == "" {
		return fmt.Errorf("include entry has an empty %q path", "local")
	}
	e.Local = local
	return nil
}

// stepTemplate is one template contributed by an included file. It is its
// own type rather than an alias of Step so the template's provenance
// (Name) and its RAW declared keys (Keys) travel with the values.
//
// Keys is what makes the merge correct: without it an explicit zero
// (`maxTurns: 0`, `agent: ""`, `session: {fork: false}`) is
// indistinguishable from an omitted key on a typed struct, so a later
// template could not override an earlier one back to a zero value.
//
// step holds the decoded values. Decoding into a Step is safe BECAUSE the
// two-part key rule (see decodeStepTemplate) runs first and rejects
// `id` / `interactive` / `interaction` / `resume_after` by name — so the
// decode never sees them. Holding a Step is also what makes a field added
// to Step later inheritable by DEFAULT: there is no curated list of
// inheritable fields to update, and mergeTemplates copies by yaml key via
// reflection rather than a per-field switch that could be forgotten.
type stepTemplate struct {
	Name string
	Keys map[string]struct{}
	step Step
}

// stepFieldIndexByYAMLKey maps each of Step's yaml keys to its struct
// field index. Derived by reflection so the set of known step fields —
// and therefore the set of inheritable ones — follows the Step type
// automatically. opencode's Step gains fields regularly while the
// orchestrator's struct rarely changes, so the maintenance burden belongs
// on the small, rarely-changing rejected set, not on a list that must be
// curated every time the engine grows a field.
//
// Two rules here mirror yaml.v3 exactly, and both are load-bearing:
//
//   - UNEXPORTED fields are skipped even if tagged. yaml.v3 ignores them,
//     and reflect cannot Set them: including one would turn a template
//     naming that key into `reflect: reflect.Value.Set using value
//     obtained using unexported field` — a PANIC during flow discovery,
//     which has no recover() anywhere between here and discoverFlows, so
//     it takes the process down rather than skipping one flow.
//   - An exported field with NO yaml tag falls back to the lowercased
//     field name, which is what yaml.v3 does. Without the fallback such a
//     field would be settable inline (`retries: 3` decodes fine) yet
//     rejected as "not a step field" in a template — silently breaking
//     the "new step fields are inheritable by default" guarantee for
//     exactly the field an author forgot to tag.
var stepFieldIndexByYAMLKey = sync.OnceValue(func() map[string]int {
	return yamlFieldIndexes(reflect.TypeOf(Step{}))
})

// yamlFieldIndexes derives the yaml-key → field-index map for a struct
// type, applying the two rules above. Split out from
// stepFieldIndexByYAMLKey so the rules can be tested against a probe type
// carrying an unexported tagged field and an untagged exported one —
// shapes Step does not currently have, and which would otherwise make the
// IsExported filter and the lowercase fallback untestable (and therefore
// deletable without any test noticing).
func yamlFieldIndexes(t reflect.Type) map[string]int {
	fields := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		key := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if key == "-" {
			continue
		}
		if key == "" {
			key = strings.ToLower(field.Name)
		}
		fields[key] = i
	}
	return fields
}

// inheritableStepKeys returns the step keys a template may contribute:
// every known Step field except the ones rejected by name and `extends`
// itself (templates are leaves — design D6). Sorted, so error messages
// and merge iteration are deterministic.
func inheritableStepKeys() []string {
	all := stepFieldIndexByYAMLKey()
	keys := make([]string, 0, len(all))
	for key := range all {
		if _, forbidden := nonInheritableStepKeys[key]; forbidden {
			continue
		}
		if key == "extends" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// nonInheritableStepKeys maps each key a template MUST NOT declare to the
// reason, which is quoted in the load error. The reason is part of the
// contract, not decoration: the flow file has a SECOND consumer (the
// c2-agent orchestrator) which parses it with its own structs and never
// resolves templates, so a key it reads must stay in the flow file where
// it can see it.
//
// This set — not an allow-list of inheritable fields — is the whole rule's
// second half. It names the four keys a second program reads and is
// expected to change roughly never; everything else Step models is
// inheritable, including fields added after this was written.
var nonInheritableStepKeys = map[string]string{
	"id":           "a step's identity must stay in the flow (two flows extending one template would collide, and the flow file would no longer show which steps it has)",
	"interactive":  "the orchestrator reads it to bind a reviewer; a template would leave it seeing a non-interactive step and start a job with nobody bound to answer",
	"interaction":  "the orchestrator reads it to bind a reviewer; a template would leave it seeing a non-interactive step and start a job with nobody bound to answer",
	"resume_after": "the orchestrator reads it to compute a postpone-resume deadline; a template would leave it seeing no opt-in, so a postponed step waits indefinitely or resumes on the wrong bound",
}

// resolveStepIncludes reads the flow's `include:` entries, decodes their
// templates and merges each step's `extends:` into that step. It runs
// inside parseFlowFile BEFORE the flow's own $ref resolution and BEFORE
// validateFlow, so a merged step is validated exactly as an inline one
// and a template cannot smuggle a step shape past validation (design D3).
//
// data is the flow file's original bytes, re-parsed here to recover each
// step's RAW declared keys; see stepRawKeys for why that second parse is
// needed and stepsRawKeysAligned for why its element count is verified
// rather than trusted.
func resolveStepIncludes(flowPath string, includes []IncludeEntry, steps []Step, data []byte) error {
	rawSteps, err := stepsRawKeysAligned(steps, data)
	if err != nil {
		return err
	}

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
		// Indexed without a bounds check on purpose: alignment is
		// guaranteed by stepsRawKeysAligned above, and a length check
		// HERE would silently substitute an empty key set — which reads
		// as "the step declared nothing" and inverts override
		// precedence, letting a template overwrite a key the step set.
		mergeTemplates(&steps[i], rawSteps[i], applied)
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
//
// The copy is by yaml key through reflection rather than a per-field
// switch: a field added to Step is then inheritable with no code to
// update here, which is the point of rejecting keys by name instead of
// curating an inheritable set.
//
// Inherited values are DEEP-copied. A shallow copy would give every step
// extending one template the same *StepOutput, Output.Schema map, Rules
// backing array, *Fallback and *Compact — and flows live in the
// process-wide flowCache shared by every concurrent job, so the first
// per-step mutation anyone adds later would corrupt sibling steps and
// other jobs at once, with -race silent while nothing writes. Deep-copying
// at load time costs nothing measurable and removes the class.
func mergeTemplates(step *Step, declared map[string]struct{}, templates []*stepTemplate) {
	fields := stepFieldIndexByYAMLKey()
	dst := reflect.ValueOf(step).Elem()
	for _, key := range inheritableStepKeys() {
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
		idx, ok := fields[key]
		if !ok {
			continue
		}
		target := dst.Field(idx)
		// Belt and braces next to the IsExported() filter in
		// stepFieldIndexByYAMLKey: Set on an unexported field panics, and
		// a panic here kills flow discovery outright (no recover between
		// parseFlowFile and discoverFlows).
		if !target.CanSet() {
			logging.Warn("Skipping inherited step key: field is not settable",
				"key", key, "template", src.Name)
			continue
		}
		target.Set(deepCopyValue(reflect.ValueOf(src.step).Field(idx)))
	}
}

// deepCopyValue returns a value that shares no mutable state with v.
// Generic by reflection rather than a per-type switch, for the same reason
// the merge is: a field added to Step must not need code here.
//
// Unexported struct fields are skipped — reflect cannot read or set them —
// which is safe because every type reachable from Step is plain data.
func deepCopyValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopyValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopyValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(deepCopyValue(iter.Key()), deepCopyValue(iter.Value()))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopyValue(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			out.Field(i).Set(deepCopyValue(v.Field(i)))
		}
		return out
	default:
		return v
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

// decodeStepTemplate decodes one template and resolves its output-schema
// $ref against the TEMPLATE file's own directory — before the merge,
// because after merging there is no per-key provenance left and the
// flow's own $ref loop knows only the flow's directory (design D3).
//
// Template keys are checked by a TWO-PART rule, not an allow-list of
// inheritable fields:
//
//  1. the key must be a known Step field — so `promt:` is an error naming
//     the real fields, rather than being silently dropped the way a typed
//     decode drops it (that is how `flow.session` typos used to pass);
//  2. and it must not be one of `id`, `interactive`, `interaction`,
//     `resume_after` — the keys the orchestrator reads out of the same
//     file without ever resolving templates.
//
// Consequence, and the reason for the shape: a field added to Step later
// is inheritable BY DEFAULT. An allow-list would put the maintenance
// friction on the fast-growing side — opencode's Step gains fields
// regularly, the orchestrator's small struct rarely changes — so each new
// engine field would be silently non-inheritable until someone thought to
// curate a list in this file.
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
		if _, known := stepFieldIndexByYAMLKey()[key]; !known {
			return nil, fmt.Errorf("%w: template %q in %q declares %q, which is not a step field (inheritable step fields: %s)",
				ErrInvalidTemplate, name, path, key, strings.Join(inheritableStepKeys(), ", "))
		}
		keys[key] = struct{}{}
	}

	// A template that contributes nothing is a mistake, not a no-op: a
	// null template (`.empty:` with no body) would otherwise merge an
	// empty agent and prompt into the extending step, and validateFlow
	// does not require a prompt — so the step would run empty.
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: template %q in %q declares no keys; a template must contribute at least one step key",
			ErrInvalidTemplate, name, path)
	}

	tmpl := &stepTemplate{Name: name, Keys: keys}
	// Decoding into a Step is safe only because the loop above already
	// rejected the four keys a template may not carry.
	if err := node.Decode(&tmpl.step); err != nil {
		return nil, fmt.Errorf("%w: template %q in %q: %v", ErrInvalidTemplate, name, path, err)
	}

	if tmpl.step.Output != nil && tmpl.step.Output.Schema != nil {
		resolved, err := format.ResolveSchemaRef(tmpl.step.Output.Schema, baseDir)
		if err != nil {
			return nil, fmt.Errorf("%w: resolving output schema $ref for template %q in %q: %v",
				ErrInvalidTemplate, name, path, err)
		}
		// ResolveSchemaRef resolves exactly ONE level: if the file it
		// loaded is itself a bare `{"$ref": ...}`, the remaining hop
		// would be resolved later by the flow's own $ref loop, against
		// the FLOW's directory — quietly breaking the rule that a
		// template's $ref resolves against the template file, and
		// picking the wrong file when a same-named schema sits beside
		// the flow. Reject instead of resolving a chain here: nothing
		// needs chained refs, and a rejection cannot be silently wrong.
		if _, chained := resolved["$ref"]; chained {
			return nil, fmt.Errorf("%w: template %q in %q resolves its output schema to another %q (%v); "+
				"chained refs are not supported, because the second hop would resolve against the flow's directory instead of the template's",
				ErrInvalidTemplate, name, path, "$ref", resolved["$ref"])
		}
		// Fresh wrapper holding the resolved map. This alone does NOT
		// isolate steps from each other — the map inside is still one
		// object — which is why mergeTemplates deep-copies whatever it
		// inherits.
		tmpl.step.Output = &StepOutput{Schema: resolved}
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
//
// A failure to parse is returned, NOT swallowed into a nil slice: nil is
// indistinguishable from "no step declared any key", which would let
// every template overwrite every step's own values at once.
func stepRawKeys(data []byte) ([]map[string]struct{}, error) {
	var raw struct {
		Flow struct {
			Steps []map[string]yaml.Node `yaml:"steps"`
		} `yaml:"flow"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: cannot re-parse flow.steps to recover per-step declared keys: %v",
			ErrInvalidInclude, err)
	}
	result := make([]map[string]struct{}, len(raw.Flow.Steps))
	for i, step := range raw.Flow.Steps {
		keys := make(map[string]struct{}, len(step))
		for key := range step {
			keys[key] = struct{}{}
		}
		result[i] = keys
	}
	return result, nil
}

// stepsRawKeysAligned returns the per-step raw key sets, verifying they
// are positionally aligned with the typed steps.
//
// The check is not defensive padding — the two decodes genuinely disagree
// on element count for one real shape. yaml.v3 DROPS a null / comment-only
// sequence entry when decoding into `[]Step`, but keeps it as an empty map
// when decoding into `[]map[string]yaml.Node`:
//
//	flow:
//	  steps:
//	    - # placeholder
//	    - id: main
//	      agent: mine
//	      extends: [".base"]
//
// gives len(steps) == 1 and len(rawSteps) == 2, so `main` would be paired
// with the empty key set, read as declaring nothing, and have its own
// `agent: mine` overwritten by the template's — a guard step silently
// running an agent its flow does not name. validateFlow cannot catch it
// because the dropped entry is absent from the typed tree.
//
// Probed and found NOT to diverge: anchors and aliases, a merge key
// (`<<:`, whose merged-in keys appear in both decodes), a multi-document
// file (both take the first document), an empty-map entry (`- {}`, kept by
// both), and a non-sequence `steps:` (both fail, and the typed decode in
// parseFlowFile reports it first). This guard covers any future divergence
// regardless.
func stepsRawKeysAligned(steps []Step, data []byte) ([]map[string]struct{}, error) {
	rawSteps, err := stepRawKeys(data)
	if err != nil {
		return nil, err
	}
	if len(rawSteps) != len(steps) {
		return nil, fmt.Errorf("%w: cannot recover per-step declared keys (%d raw vs %d steps); "+
			"remove empty entries from flow.steps", ErrInvalidInclude, len(rawSteps), len(steps))
	}
	return rawSteps, nil
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

func availableTemplates(templates map[string]*stepTemplate) string {
	if len(templates) == 0 {
		return "none — the flow declares no include, or its includes define no `.`-prefixed template"
	}
	return strings.Join(sortedKeys(templates), ", ")
}

func pluralise(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
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
