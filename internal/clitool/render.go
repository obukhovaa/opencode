package clitool

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// InputError reports a call whose parameters do not satisfy the manifest
// (unknown or missing parameters, type / enum violations). Like PolicyError
// it is model-visible: the model can correct the call.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return "invalid tool input: " + e.Reason }

// Invocation is a validated, policy-checked call ready to execute.
type Invocation struct {
	// Args is the model-influenced vector (what the policy and the permission
	// globs saw); the executor prepends the manifest's prefixArgs.
	Args    []string
	Stdin   string
	Timeout int // seconds; 0 = manifest default
}

// Prepare turns a decoded tool input into an Invocation: argv mode reads
// `args` / `stdin` / `timeout`; structured mode validates the parameters and
// renders the template. Both then run the argument policy. Errors are
// *InputError or *PolicyError.
func (m *Manifest) Prepare(input map[string]any) (*Invocation, error) {
	inv := &Invocation{}
	switch m.Mode {
	case ModeStructured:
		args, err := m.Render(input)
		if err != nil {
			return nil, err
		}
		inv.Args = args
	default:
		for k := range input {
			switch k {
			case "args", "stdin", "timeout":
			default:
				return nil, &InputError{Reason: fmt.Sprintf("unknown parameter %q (argv mode accepts args, stdin, timeout)", k)}
			}
		}
		rawArgs, ok := input["args"]
		if !ok {
			return nil, &InputError{Reason: "args is required (an array of strings; use [] for no arguments)"}
		}
		list, ok := rawArgs.([]any)
		if !ok {
			return nil, &InputError{Reason: "args must be an array of strings"}
		}
		inv.Args = make([]string, 0, len(list))
		for i, v := range list {
			s, ok := v.(string)
			if !ok {
				return nil, &InputError{Reason: fmt.Sprintf("args[%d] must be a string", i)}
			}
			inv.Args = append(inv.Args, s)
		}
		if v, ok := input["stdin"]; ok {
			if !m.Stdin {
				return nil, &InputError{Reason: "this tool does not accept stdin"}
			}
			s, ok := v.(string)
			if !ok {
				return nil, &InputError{Reason: "stdin must be a string"}
			}
			inv.Stdin = s
		}
		if v, ok := input["timeout"]; ok {
			f, ok := v.(float64)
			if !ok || f < 0 || f != math.Trunc(f) {
				return nil, &InputError{Reason: "timeout must be a non-negative integer number of seconds"}
			}
			inv.Timeout = int(f)
		}
	}
	if err := m.CheckArgs(inv.Args); err != nil {
		return nil, err
	}
	return inv, nil
}

// Render validates structured-mode parameters against the declared schema
// subset and renders the argv template.
func (m *Manifest) Render(input map[string]any) ([]string, error) {
	if m.Mode != ModeStructured {
		return nil, errors.New("Render is only valid in structured mode")
	}
	for k := range input {
		if _, ok := m.Parameters[k]; !ok {
			return nil, &InputError{Reason: fmt.Sprintf("unknown parameter %q", k)}
		}
	}
	for _, r := range m.Required {
		if _, ok := input[r]; !ok {
			return nil, &InputError{Reason: fmt.Sprintf("missing required parameter %q", r)}
		}
	}
	values := make(map[string]any, len(input))
	for name, raw := range input {
		schema := m.Parameters[name].(map[string]any)
		v, err := validateValue(name, raw, schema, m.paramTypes[name])
		if err != nil {
			return nil, err
		}
		values[name] = v
	}
	// Schema defaults fill absent optional parameters, so a manifest can
	// pin e.g. `format: JSON` while still letting the model override it.
	for name, raw := range m.Parameters {
		if _, present := values[name]; present {
			continue
		}
		schema := raw.(map[string]any)
		def, ok := schema["default"]
		if !ok {
			continue
		}
		v, err := validateValue(name, def, schema, m.paramTypes[name])
		if err != nil {
			return nil, &InputError{Reason: fmt.Sprintf("manifest default for %q is invalid: %v", name, err)}
		}
		values[name] = v
	}
	return renderEntries(m.template, values), nil
}

// validateValue checks one parameter against the supported JSON-Schema
// subset and returns a normalized value (string, float64, bool, []string).
func validateValue(name string, raw any, schema map[string]any, typ string) (any, error) {
	bad := func(format string, a ...any) error {
		return &InputError{Reason: fmt.Sprintf("parameter %q: ", name) + fmt.Sprintf(format, a...)}
	}
	var val any
	switch typ {
	case "string":
		s, ok := raw.(string)
		if !ok {
			return nil, bad("must be a string")
		}
		if n, ok := numberField(schema, "minLength"); ok && len(s) < int(n) {
			return nil, bad("shorter than minLength %d", int(n))
		}
		if n, ok := numberField(schema, "maxLength"); ok && len(s) > int(n) {
			return nil, bad("longer than maxLength %d", int(n))
		}
		if p, ok := schema["pattern"].(string); ok && p != "" {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, bad("manifest pattern %q does not compile: %v", p, err)
			}
			if !re.MatchString(s) {
				return nil, bad("does not match pattern %q", p)
			}
		}
		val = s
	case "integer", "number":
		f, ok := raw.(float64)
		if !ok {
			// yaml / json decoders may hand over ints in tests.
			switch n := raw.(type) {
			case int:
				f, ok = float64(n), true
			case int64:
				f, ok = float64(n), true
			}
		}
		if !ok {
			return nil, bad("must be a %s", typ)
		}
		if typ == "integer" && f != math.Trunc(f) {
			return nil, bad("must be an integer")
		}
		if n, ok := numberField(schema, "minimum"); ok && f < n {
			return nil, bad("below minimum %v", n)
		}
		if n, ok := numberField(schema, "maximum"); ok && f > n {
			return nil, bad("above maximum %v", n)
		}
		val = f
	case "boolean":
		b, ok := raw.(bool)
		if !ok {
			return nil, bad("must be a boolean")
		}
		val = b
	case "array":
		list, ok := raw.([]any)
		if !ok {
			return nil, bad("must be an array of strings")
		}
		out := make([]string, 0, len(list))
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				return nil, bad("element %d must be a string", i)
			}
			out = append(out, s)
		}
		val = out
	default:
		return nil, bad("unsupported type %q", typ)
	}
	if enumRaw, ok := schema["enum"].([]any); ok && len(enumRaw) > 0 {
		allowed := make([]string, 0, len(enumRaw))
		found := false
		for _, e := range enumRaw {
			es := formatScalar(e)
			allowed = append(allowed, es)
			if formatScalar(val) == es {
				found = true
			}
		}
		if !found {
			return nil, bad("must be one of [%s]", strings.Join(allowed, ", "))
		}
	}
	return val, nil
}

func numberField(schema map[string]any, key string) (float64, bool) {
	switch n := schema[key].(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// formatScalar renders a parameter value as one argument.
func formatScalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(v)
	}
}

// parseTemplate validates a structured `argv` template and records the
// placeholders each entry references. Rules:
//   - a string entry may embed `{param}` placeholders;
//   - a placeholder for an array parameter must be the whole entry (it
//     expands to one argument per element);
//   - a placeholder for a boolean parameter must be the whole entry and must
//     sit inside a group: it emits nothing and only guards the group;
//   - a nested list is an optional group, emitted only when every placeholder
//     it references is present and not boolean false.
func parseTemplate(raw []any, types map[string]string, inGroup bool) ([]templateEntry, []string, error) {
	var entries []templateEntry
	var allRefs []string
	for i, item := range raw {
		switch v := item.(type) {
		case string:
			refs := placeholderRefs(v)
			for _, r := range refs {
				switch types[r] {
				case "array":
					if v != "{"+r+"}" {
						return nil, nil, fmt.Errorf("entry %d: array parameter {%s} must be the whole entry", i, r)
					}
				case "boolean":
					if v != "{"+r+"}" {
						return nil, nil, fmt.Errorf("entry %d: boolean parameter {%s} must be the whole entry", i, r)
					}
					if !inGroup {
						return nil, nil, fmt.Errorf("entry %d: boolean parameter {%s} must be used inside an optional group, e.g. [\"--flag\", \"{%s}\"]", i, r, r)
					}
				}
			}
			entries = append(entries, templateEntry{literal: v, refs: refs})
			allRefs = append(allRefs, refs...)
		case []any:
			sub, refs, err := parseTemplate(v, types, true)
			if err != nil {
				return nil, nil, fmt.Errorf("group %d: %w", i, err)
			}
			if len(refs) == 0 {
				return nil, nil, fmt.Errorf("group %d references no parameter; optional groups need a placeholder to guard them", i)
			}
			entries = append(entries, templateEntry{group: sub, isGroup: true, refs: refs})
			allRefs = append(allRefs, refs...)
		default:
			return nil, nil, fmt.Errorf("entry %d must be a string or a list, got %T", i, item)
		}
	}
	return entries, allRefs, nil
}

func placeholderRefs(s string) []string {
	var refs []string
	for _, match := range placeholderRe.FindAllStringSubmatch(s, -1) {
		refs = append(refs, match[1])
	}
	return refs
}

func renderEntries(entries []templateEntry, values map[string]any) []string {
	var out []string
	for _, e := range entries {
		if e.isGroup {
			if !groupActive(e.refs, values) {
				continue
			}
			out = append(out, renderEntries(e.group, values)...)
			continue
		}
		if len(e.refs) == 0 {
			out = append(out, e.literal)
			continue
		}
		// Whole-entry array / boolean placeholders.
		if len(e.refs) == 1 && e.literal == "{"+e.refs[0]+"}" {
			switch v := values[e.refs[0]].(type) {
			case []string:
				out = append(out, v...)
				continue
			case bool:
				// Guard only: emits nothing (the group decided inclusion).
				continue
			case nil:
				// Absent optional parameter at top level: emits nothing.
				continue
			}
		}
		// Top-level entry with an absent optional parameter: drop the entry
		// rather than emit a dangling placeholder.
		missing := false
		for _, r := range e.refs {
			if _, ok := values[r]; !ok {
				missing = true
			}
		}
		if missing {
			continue
		}
		out = append(out, placeholderRe.ReplaceAllStringFunc(e.literal, func(ph string) string {
			name := ph[1 : len(ph)-1]
			return formatScalar(values[name])
		}))
	}
	return out
}

// groupActive reports whether every referenced parameter is present and not
// boolean false.
func groupActive(refs []string, values map[string]any) bool {
	for _, r := range refs {
		v, ok := values[r]
		if !ok {
			return false
		}
		if b, isBool := v.(bool); isBool && !b {
			return false
		}
	}
	return true
}

// TemplateRefs lists the parameters the template references (sorted).
func (m *Manifest) TemplateRefs() []string {
	seen := map[string]bool{}
	var collect func([]templateEntry)
	collect = func(es []templateEntry) {
		for _, e := range es {
			for _, r := range e.refs {
				seen[r] = true
			}
			if e.isGroup {
				collect(e.group)
			}
		}
	}
	collect(m.template)
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
