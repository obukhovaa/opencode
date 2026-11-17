package tools

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	composeMaxPlan      = 32 * 1024
	composeMaxDepth     = 64
	composeMaxChild     = 256 * 1024
	composeMaxTotal     = 1024 * 1024
	composeMaxProjected = 16 * 1024
)

var errComposeInput = errors.New("invalid composition plan")
var composeID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type composePlan struct {
	Steps  []composeStep `json:"steps"`
	Return []string      `json:"return"`
}
type composeStep struct {
	ID        string           `json:"id"`
	Tool      string           `json:"tool"`
	Arguments map[string]any   `json:"arguments"`
	Bindings  []composeBinding `json:"bindings,omitempty"`
	Select    composeSelect    `json:"select"`
}
type composeBinding struct {
	Target  string `json:"target"`
	Step    string `json:"step"`
	Pointer string `json:"pointer"`
}
type composeSelect struct {
	Fields map[string]string `json:"fields"`
	Rows   *string           `json:"rows,omitempty"`
	Limit  *int              `json:"limit,omitempty"`
}

// Token decoding detects duplicate keys before a map can overwrite them.
func composeJSON(input string) (any, error) {
	if !utf8.ValidString(input) {
		return nil, errComposeInput
	}
	d := json.NewDecoder(strings.NewReader(input))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		token, err := d.Token()
		if err != nil {
			return nil, errComposeInput
		}
		delim, container := token.(json.Delim)
		if !container {
			return token, nil
		}
		if depth >= composeMaxDepth {
			return nil, errComposeInput
		}
		switch delim {
		case '{':
			object := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, errComposeInput
				}
				name, ok := key.(string)
				if !ok {
					return nil, errComposeInput
				}
				if _, exists := object[name]; exists {
					return nil, errComposeInput
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				object[name] = value
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errComposeInput
			}
			return object, nil
		case '[':
			array := []any{}
			for d.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errComposeInput
			}
			return array, nil
		}
		return nil, errComposeInput
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errComposeInput
	}
	return value, nil
}

func parseComposePlan(input string) (composePlan, error) {
	var plan composePlan
	if len(input) > composeMaxPlan {
		return plan, errComposeInput
	}
	value, err := composeJSON(input)
	if err != nil {
		return plan, err
	}
	root, ok := value.(map[string]any)
	if !ok || !composeKeys(root, "steps", "return") {
		return plan, errComposeInput
	}
	steps, ok := root["steps"].([]any)
	if !ok || len(steps) < 1 || len(steps) > 8 {
		return plan, errComposeInput
	}
	// Presence matters: null must not masquerade as an omitted optional member.
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok || !composeKeys(step, "id", "tool", "arguments", "bindings", "select") {
			return plan, errComposeInput
		}
		sel, ok := step["select"].(map[string]any)
		if !ok || !composeKeys(sel, "fields", "rows", "limit") {
			return plan, errComposeInput
		}
		fieldValues, ok := sel["fields"].(map[string]any)
		if !ok {
			return plan, errComposeInput
		}
		for _, value := range fieldValues {
			if _, ok := value.(string); !ok {
				return plan, errComposeInput
			}
		}
		if rows, exists := sel["rows"]; exists {
			if _, ok := rows.(string); !ok {
				return plan, errComposeInput
			}
		}
		if limit, exists := sel["limit"]; exists && limit == nil {
			return plan, errComposeInput
		}
		if bindings, exists := step["bindings"]; exists {
			array, ok := bindings.([]any)
			if !ok {
				return plan, errComposeInput
			}
			for _, rawBinding := range array {
				binding, ok := rawBinding.(map[string]any)
				if !ok || !composeKeys(binding, "target", "step", "pointer") {
					return plan, errComposeInput
				}
				for _, name := range []string{"target", "step", "pointer"} {
					if _, ok := binding[name].(string); !ok {
						return plan, errComposeInput
					}
				}
			}
		}
	}
	d := json.NewDecoder(strings.NewReader(input))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&plan); err != nil {
		return plan, errComposeInput
	}
	seen := map[string]bool{}
	for _, step := range plan.Steps {
		if !composeID.MatchString(step.ID) || seen[step.ID] || len(step.Tool) < 1 || len(step.Tool) > 256 || step.Arguments == nil {
			return plan, errComposeInput
		}
		if len(step.Select.Fields) < 1 || len(step.Select.Fields) > 32 {
			return plan, errComposeInput
		}
		for name, pointer := range step.Select.Fields {
			if len(name) < 1 || len(name) > 64 {
				return plan, errComposeInput
			}
			if _, err := composePointer(pointer); err != nil {
				return plan, err
			}
		}
		if (step.Select.Rows == nil) != (step.Select.Limit == nil) {
			return plan, errComposeInput
		}
		if step.Select.Rows != nil {
			if _, err := composePointer(*step.Select.Rows); err != nil || *step.Select.Limit < 1 || *step.Select.Limit > 100 {
				return plan, errComposeInput
			}
		}
		targets := [][]string{}
		for _, binding := range step.Bindings {
			if !seen[binding.Step] {
				return plan, errComposeInput
			}
			target, err := composePointer(binding.Target)
			if err != nil || len(target) == 0 {
				return plan, errComposeInput
			}
			if _, err := composePointer(binding.Pointer); err != nil {
				return plan, err
			}
			if _, err := composeLookup(step.Arguments, target); err != nil {
				return plan, err
			}
			for _, other := range targets {
				if composePrefix(target, other) || composePrefix(other, target) {
					return plan, errComposeInput
				}
			}
			targets = append(targets, target)
		}
		seen[step.ID] = true
	}
	if len(plan.Return) < 1 || len(plan.Return) > 8 {
		return plan, errComposeInput
	}
	returned := map[string]bool{}
	for _, id := range plan.Return {
		if !seen[id] || returned[id] {
			return plan, errComposeInput
		}
		returned[id] = true
	}
	return plan, nil
}

func composePointer(pointer string) ([]string, error) {
	if len(pointer) > 1024 {
		return nil, errComposeInput
	}
	if pointer == "" {
		return nil, nil
	}
	if pointer[0] != '/' {
		return nil, errComposeInput
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		var decoded strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				decoded.WriteByte(part[j])
				continue
			}
			j++
			if j == len(part) || (part[j] != '0' && part[j] != '1') {
				return nil, errComposeInput
			}
			if part[j] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[i] = decoded.String()
	}
	return parts, nil
}
func composePrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func composeIndex(part string, size int) (int, error) {
	if part == "" || (len(part) > 1 && part[0] == '0') {
		return 0, errComposeInput
	}
	for _, c := range part {
		if c < '0' || c > '9' {
			return 0, errComposeInput
		}
	}
	i, err := strconv.Atoi(part)
	if err != nil || i >= size {
		return 0, errComposeInput
	}
	return i, nil
}
func composeLookup(value any, path []string) (any, error) {
	for _, part := range path {
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[part]
			if !ok {
				return nil, errComposeInput
			}
		case []any:
			index, err := composeIndex(part, len(current))
			if err != nil {
				return nil, err
			}
			value = current[index]
		default:
			return nil, errComposeInput
		}
	}
	return value, nil
}
func composeBind(arguments map[string]any, path []string, value any) error {
	parent, err := composeLookup(arguments, path[:len(path)-1])
	if err != nil {
		return err
	}
	key := path[len(path)-1]
	switch current := parent.(type) {
	case map[string]any:
		current[key] = value
	case []any:
		index, err := composeIndex(key, len(current))
		if err != nil {
			return err
		}
		current[index] = value
	default:
		return errComposeInput
	}
	return nil
}

func composeKeys(object map[string]any, allowed ...string) bool {
	for key := range object {
		found := false
		for _, name := range allowed {
			if key == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
