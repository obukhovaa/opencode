package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
// field index, so the set of known — and therefore inheritable — step
// keys follows the Step type with no curated list to maintain. See
// docs/flows.md#shared-step-templates-include--extends for the rule and
// its rationale.
var stepFieldIndexByYAMLKey = sync.OnceValue(func() map[string]int {
	return yamlFieldIndexes(reflect.TypeOf(Step{}))
})

// yamlFieldIndexes derives the yaml-key → field-index map for a struct
// type, mirroring yaml.v3's own field rules. Each skip below is a shape
// whose key CANNOT be honoured by the merge, and admitting one fails
// unsafely rather than loudly:
//
//   - unexported: reflect cannot Set it, so a template naming that key
//     would panic (`reflect.Value.Set using value obtained using
//     unexported field`) inside flow discovery, which nothing recovers.
//   - `,inline`: yaml.v3 accepts the INNER fields' keys and ignores the
//     outer field's name, so the derived name would pass the key check,
//     be left zero by the decode, and then be written over the step's own
//     value — silent data loss. This is the one shape that fails unsafely.
//   - embedded without `,inline`: yaml.v3 DOES key this by the lowercased
//     type name (verified, not assumed), so skipping it is deliberately
//     broader than the decoder — such a field is declarable in a flow step
//     but rejected in a template, which fails LOUDLY. Excluded because an
//     embedded struct's keys belong to whatever owns it, and inheriting
//     half of one is not a thing a template author can reason about. If
//     Step ever needs one, decide then; the invariant test will say so.
//
// The untagged-exported fallback to the lowercased field name is likewise
// yaml.v3's rule; without it such a field decodes fine inline yet is
// rejected in a template.
//
// Split out of stepFieldIndexByYAMLKey so these rules can be tested
// against a probe type: Step has none of these shapes today, which would
// otherwise leave every rule here deletable with the suite green.
func yamlFieldIndexes(t reflect.Type) map[string]int {
	fields := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("yaml")
		parts := strings.Split(tag, ",")
		if slices.Contains(parts[1:], "inline") || field.Anonymous {
			continue
		}
		key := parts[0]
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
// inheritable, including fields added after this was written. In
// particular `context` is intentionally absent: a template supplying a
// per-step context override is exactly what template files are for, and
// the orchestrator never reads that key.
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
// declared keys win, shallow per top-level key and left to right. The
// author-facing semantics are in
// docs/flows.md#shared-step-templates-include--extends; what matters here:
//
//   - declared is the step's set of RAW declared keys and is what decides
//     what overrides — NOT the decoded values, which cannot tell an
//     explicit zero from an omitted key.
//   - the copy is by yaml key through reflection, not a per-field switch,
//     so a field added to Step needs no code here.
//   - inherited values are DEEP-copied: flows live in the process-wide
//     flowCache shared by every concurrent job, so sharing a *StepOutput,
//     Schema map, Rules array, *Fallback or *Compact between steps turns
//     the first per-step mutation anyone adds later into cross-step and
//     cross-job corruption, with -race silent while nothing writes.
func mergeTemplates(step *Step, declared map[string]struct{}, templates []*stepTemplate) {
	fields := stepFieldIndexByYAMLKey()
	suppressed := suppressedByOwnPromptSource(declared)
	dst := reflect.ValueOf(step).Elem()
	for _, key := range inheritableStepKeys() {
		if _, own := declared[key]; own {
			continue
		}
		if _, dead := suppressed[key]; dead {
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
		// inheritableStepKeys() derives its keys from this same map, and
		// yamlFieldIndexes admits only exported, settable fields — so the
		// lookup always hits and the field is always settable. Both facts
		// are asserted by TestStepFieldIndexByYAMLKey_MirrorsDecoder
		// rather than re-checked here, where a fallback branch could only
		// hide a broken invariant.
		dst.Field(fields[key]).Set(deepCopyValue(reflect.ValueOf(src.step).Field(fields[key])))
	}
}

// The step keys naming each of the two mutually exclusive prompt sources:
// an inline prompt, or a Langfuse reference. Declaring either source
// suppresses inheritance of the OTHER one — see
// suppressedByOwnPromptSource.
var (
	inlinePromptStepKeys  = []string{"prompt"}
	managedPromptStepKeys = []string{"langfusePromptPath", "langfusePromptLabel"}
)

// suppressedByOwnPromptSource returns the keys mergeTemplates must not
// inherit because the step declares the competing prompt source itself.
// Empty when it declares neither, which is the common case.
//
// This is the one place mergeTemplates knows field names, and it is worth
// the exception. Because prompt and langfusePromptPath are mutually
// exclusive (validateStepPromptSource), inheriting them independently
// produces two failures that read as bugs in the loader rather than as
// mistakes in the flow:
//
//   - a template supplying `prompt` and a step supplying
//     `langfusePromptPath` (or the reverse) collide on "declares both",
//     even though the step's YAML shows exactly one key and the intent —
//     replace the template's prompt with mine — is unambiguous;
//   - overriding an inherited `langfusePromptPath` with an inline `prompt`
//     would leave the template's `langfusePromptLabel` behind, failing with
//     "sets langfusePromptLabel without langfusePromptPath" for a key the
//     step never wrote.
//
// So a declared prompt source replaces the SOURCE, not just its own field —
// the rule the agent registry already applies in applyConfigOverrides and
// mergeMarkdownIntoExisting, and what
// specs/langfuse-prompt-management/spec.md means by "a declared prompt
// source SHALL replace the source".
//
// Deliberately directional rather than one three-key group:
// langfusePromptLabel stays independently inheritable, so a step CAN
// declare just a label and take the path from its template — the natural
// way to run a template's prompt against a different label.
//
// Only a source the STEP declares suppresses anything. Two templates that
// between them supply both sources still collide, which is a real conflict
// with no author intent to honour.
func suppressedByOwnPromptSource(declared map[string]struct{}) map[string]struct{} {
	declaresAny := func(keys []string) bool {
		for _, key := range keys {
			if _, ok := declared[key]; ok {
				return true
			}
		}
		return false
	}

	var out map[string]struct{}
	add := func(keys []string) {
		if out == nil {
			out = make(map[string]struct{}, len(inlinePromptStepKeys)+len(managedPromptStepKeys))
		}
		for _, key := range keys {
			out[key] = struct{}{}
		}
	}
	if declaresAny(inlinePromptStepKeys) {
		add(managedPromptStepKeys)
	}
	if declaresAny(managedPromptStepKeys) {
		add(inlinePromptStepKeys)
	}
	return out
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
// Template keys are checked by a TWO-PART rule — a known Step field, and
// not one of the keys the orchestrator reads (nonInheritableStepKeys) —
// which is why a field added to Step later is inheritable by default. The
// rule and its rationale are in
// docs/flows.md#shared-step-templates-include--extends.
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
		// ResolveSchemaRef resolves exactly ONE level, so a schema file
		// that is itself a bare `{"$ref": ...}` would have its second hop
		// resolved later by the flow's own loop against the FLOW's
		// directory — silently wrong, and it picks the wrong file when a
		// same-named schema sits beside the flow.
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

// resolveIncludePath resolves an include's `local:` path: absolute as-is,
// relative against config.WorkingDir (root-relative — the same base
// discoverCustomPathFlows uses for relative flowPaths entries; see
// docs/flows.md#path-resolution--note-the-asymmetry).
//
// config.Get() rather than config.WorkingDirectory(), which PANICS when no
// config is loaded: parseFlowFile is reachable from tests and from tooling
// that never calls config.Load. With no config a relative include falls
// back to the process working directory.
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
// A second parse of the same bytes is the established pattern here —
// validateFlowSessionKeys already does it. (A custom Step.UnmarshalYAML
// could record the key set but not perform the merge: templates live
// outside the step's YAML node.)
//
// A parse failure is returned, NOT swallowed into a nil slice: nil is
// indistinguishable from "no step declared any key", which would let every
// template overwrite every step's own values at once.
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
