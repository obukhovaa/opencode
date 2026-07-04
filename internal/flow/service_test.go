package flow

import (
	"encoding/json"
	"testing"
)

func TestEvaluatePredicate(t *testing.T) {
	tests := []struct {
		name      string
		predicate string
		args      map[string]any
		want      bool
		wantErr   bool
	}{
		{"equals match", `${args.status} == done`, map[string]any{"status": "done"}, true, false},
		{"equals no match", `${args.status} == done`, map[string]any{"status": "pending"}, false, false},
		{"equals missing key", `${args.status} == done`, map[string]any{}, false, false},

		{"not equals match", `${args.status} != done`, map[string]any{"status": "pending"}, true, false},
		{"not equals no match", `${args.status} != done`, map[string]any{"status": "done"}, false, false},
		{"not equals missing key", `${args.status} != done`, map[string]any{}, false, false},

		{"regex match", `${args.workflow} =~ /IMPL|REVIEW/`, map[string]any{"workflow": "IMPLEMENTATION"}, true, false},
		{"regex no match", `${args.workflow} =~ /IMPL|REVIEW/`, map[string]any{"workflow": "SKIP"}, false, false},
		{"regex exact", `${args.workflow} =~ /^DONE$/`, map[string]any{"workflow": "DONE"}, true, false},
		{"regex case sensitive", `${args.workflow} =~ /done/`, map[string]any{"workflow": "DONE"}, false, false},

		{"invalid syntax", `bad predicate`, map[string]any{}, false, true},
		{"invalid regex", `${args.x} =~ /[invalid/`, map[string]any{"x": "test"}, false, true},
		{"regex missing delimiters", `${args.x} =~ pattern`, map[string]any{"x": "test"}, false, true},

		{"numeric value", `${args.count} == 5`, map[string]any{"count": 5}, true, false},
		{"with whitespace", `${args.status} == done`, map[string]any{"status": "done"}, true, false},

		{"sizeof empty array", `sizeof ${args.items} == 0`, map[string]any{"items": []any{}}, true, false},
		{"sizeof array", `sizeof ${args.items} == 3`, map[string]any{"items": []any{"a", "b", "c"}}, true, false},
		{"sizeof array not equal", `sizeof ${args.items} != 0`, map[string]any{"items": []any{"a"}}, true, false},
		{"sizeof empty map", `sizeof ${args.data} == 0`, map[string]any{"data": map[string]any{}}, true, false},
		{"sizeof map", `sizeof ${args.data} == 2`, map[string]any{"data": map[string]any{"k1": "v1", "k2": "v2"}}, true, false},
		{"sizeof string", `sizeof ${args.name} == 5`, map[string]any{"name": "hello"}, true, false},
		{"sizeof missing key", `sizeof ${args.missing} == 0`, map[string]any{}, false, false},
		{"sizeof regex", `sizeof ${args.items} =~ /^[0-9]+$/`, map[string]any{"items": []any{"a", "b"}}, true, false},

		// Dot-path traversal into nested maps.
		{"dot-path equals", `${args.reviewer.email} == u@x.com`, map[string]any{"reviewer": map[string]any{"email": "u@x.com"}}, true, false},
		{"dot-path missing leaf", `${args.reviewer.mention} == whatever`, map[string]any{"reviewer": map[string]any{"email": "u@x.com"}}, false, false},
		{"dot-path through non-map", `${args.reviewer.email} == whatever`, map[string]any{"reviewer": "string-not-map"}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluatePredicate(tt.predicate, tt.args, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("evaluatePredicate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("evaluatePredicate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvaluatePredicate_StepScope(t *testing.T) {
	tests := []struct {
		name      string
		predicate string
		stepVars  map[string]any
		want      bool
		wantErr   bool
	}{
		{"iteration equals 1", `${step.iteration} == 1`, map[string]any{"iteration": 1}, true, false},
		{"iteration not equals", `${step.iteration} != 1`, map[string]any{"iteration": 3}, true, false},
		{"iteration regex", `${step.iteration} =~ /^[0-9]+$/`, map[string]any{"iteration": 5}, true, false},
		{"unknown step var errors", `${step.bogus} == 1`, map[string]any{"iteration": 1}, false, true},
		{"sizeof on int", `sizeof ${step.iteration} == 1`, map[string]any{"iteration": 5}, true, false}, // "5" has length 1
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluatePredicate(tt.predicate, nil, tt.stepVars)
			if (err != nil) != tt.wantErr {
				t.Errorf("evaluatePredicate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("evaluatePredicate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubstituteScoped_StepIteration(t *testing.T) {
	tmpl := "Iteration ${step.iteration} of ${args.label}"
	got := substituteScoped(tmpl, map[string]any{"label": "build"}, map[string]any{"iteration": 3})
	want := "Iteration 3 of build"
	if got != want {
		t.Errorf("substituteScoped() = %q, want %q", got, want)
	}
}

func TestSubstituteScoped_StepIterationNotInArgs(t *testing.T) {
	// Even when args also has an "iteration" key, ${step.iteration} must
	// resolve from stepVars (closed namespace, not args). Conversely
	// ${args.iteration} must resolve from args, not stepVars.
	tmpl := "step=${step.iteration} args=${args.iteration}"
	got := substituteScoped(tmpl, map[string]any{"iteration": 99}, map[string]any{"iteration": 3})
	want := "step=3 args=99"
	if got != want {
		t.Errorf("substituteScoped() = %q, want %q", got, want)
	}
}

func TestSubstituteScoped_DotPath(t *testing.T) {
	args := map[string]any{
		"reviewer": map[string]any{
			"email":       "user@example.com",
			"displayName": "Test User",
			"mention":     "<@U1>",
		},
	}
	tmpl := "email=${args.reviewer.email} name=${args.reviewer.displayName}"
	got := substituteScoped(tmpl, args, nil)
	want := "email=user@example.com name=Test User"
	if got != want {
		t.Errorf("substituteScoped() = %q, want %q", got, want)
	}
}

func TestSubstituteScoped_DotPathMissingLeafPreserved(t *testing.T) {
	// A dot-path where the leaf key is missing must preserve the literal
	// placeholder (same behaviour as a flat missing key). Otherwise the
	// prompt silently loses the marker and downstream detection (e.g.
	// resolveSessionPrefix) can't flag it.
	args := map[string]any{
		"reviewer": map[string]any{"email": "u@x.com"},
	}
	got := substituteScoped("m=${args.reviewer.mention}", args, nil)
	want := "m=${args.reviewer.mention}"
	if got != want {
		t.Errorf("substituteScoped() = %q, want %q", got, want)
	}
}

func TestSubstituteScoped_DotPathThroughNonMapPreserved(t *testing.T) {
	// Walking past a non-map value must preserve the placeholder rather
	// than emitting a partial result.
	args := map[string]any{"reviewer": "just-a-string"}
	got := substituteScoped("m=${args.reviewer.email}", args, nil)
	want := "m=${args.reviewer.email}"
	if got != want {
		t.Errorf("substituteScoped() = %q, want %q", got, want)
	}
}

func TestSubstituteScoped_TopLevelWinsOverDotWalk(t *testing.T) {
	// Backward compat: a flat key that literally contains a dot must
	// resolve before the resolver falls back to walking a["b"].
	args := map[string]any{
		"a.b": "flat-wins",
		"a":   map[string]any{"b": "nested-loses"},
	}
	got := substituteScoped("${args.a.b}", args, nil)
	if got != "flat-wins" {
		t.Errorf("substituteScoped() = %q, want %q", got, "flat-wins")
	}
}

func TestSubstituteScoped_BareArgsStillJSON(t *testing.T) {
	// Bare ${args} (no path) still emits the whole args as JSON — must
	// not be swallowed by the dot-path regex.
	args := map[string]any{"k": "v"}
	got := substituteScoped("${args}", args, nil)
	if got == "${args}" {
		t.Errorf("bare ${args} was not substituted, got %q", got)
	}
	if !containsSubstring(got, `"k": "v"`) {
		t.Errorf("bare ${args} did not emit JSON, got %q", got)
	}
}

func TestSubstituteArgs(t *testing.T) {
	tests := []struct {
		name     string
		template string
		args     map[string]any
		want     string
	}{
		{
			name:     "simple substitution",
			template: "Hello ${args.name}",
			args:     map[string]any{"name": "World"},
			want:     "Hello World",
		},
		{
			name:     "multiple substitutions",
			template: "${args.greeting} ${args.name}!",
			args:     map[string]any{"greeting": "Hello", "name": "World"},
			want:     "Hello World!",
		},
		{
			name:     "missing key preserved",
			template: "Hello ${args.missing}",
			args:     map[string]any{},
			want:     "Hello ${args.missing}",
		},
		{
			name:     "full args dump",
			template: "All args: ${args}",
			args:     map[string]any{"key": "value"},
			want:     "",
		},
		{
			name:     "no placeholders",
			template: "No placeholders here",
			args:     map[string]any{"key": "value"},
			want:     "No placeholders here",
		},
		{
			name:     "numeric value",
			template: "Count: ${args.count}",
			args:     map[string]any{"count": 42},
			want:     "Count: 42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substituteArgs(tt.template, tt.args)
			if tt.name == "full args dump" {
				if !containsJSON(got, tt.args) {
					t.Errorf("substituteArgs() full dump doesn't contain expected JSON")
				}
			} else if got != tt.want {
				t.Errorf("substituteArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func containsJSON(result string, args map[string]any) bool {
	argsJSON, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return false
	}
	return len(result) > 0 && result != "${args}" && containsSubstring(result, string(argsJSON))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestResolveSessionPrefix(t *testing.T) {
	tests := []struct {
		name       string
		specPrefix string
		args       map[string]any
		want       string
		wantErr    bool
	}{
		{"empty prefix uses timestamp", "", map[string]any{}, "", false},
		{"literal constant", "my_static_id", map[string]any{}, "my_static_id", false},
		{"args variable", "${args.jira_issue_id}", map[string]any{"jira_issue_id": "PROJ-123"}, "PROJ-123", false},
		{"args variable numeric", "${args.build_num}", map[string]any{"build_num": 42}, "42", false},
		{"mixed literal and variables", "psb-${args.monitor_id}-${args.event_id}", map[string]any{"monitor_id": "123", "event_id": "456"}, "psb-123-456", false},
		{"mixed with one variable", "prefix-${args.id}", map[string]any{"id": "abc"}, "prefix-abc", false},
		{"args variable missing", "${args.missing_key}", map[string]any{}, "", true},
		{"mixed with partial missing", "psb-${args.found}-${args.missing}", map[string]any{"found": "ok"}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSessionPrefix(tt.specPrefix, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveSessionPrefix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if tt.specPrefix == "" {
				if got == "" {
					t.Error("resolveSessionPrefix() returned empty string for timestamp fallback")
				}
			} else if got != tt.want {
				t.Errorf("resolveSessionPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCopyArgs(t *testing.T) {
	original := map[string]any{"a": 1, "b": "two"}
	copied := copyArgs(original)

	original["c"] = 3

	if _, ok := copied["c"]; ok {
		t.Error("copyArgs should create independent copy")
	}
	if len(copied) != 2 {
		t.Errorf("copied length = %d, want 2", len(copied))
	}
}

func TestFindStep(t *testing.T) {
	steps := []Step{
		{ID: "step-a"},
		{ID: "step-b"},
		{ID: "step-c"},
	}

	t.Run("found", func(t *testing.T) {
		s := findStep(steps, "step-b")
		if s == nil {
			t.Fatal("expected to find step-b")
		}
		if s.ID != "step-b" {
			t.Errorf("found step ID = %q, want %q", s.ID, "step-b")
		}
	})

	t.Run("not found", func(t *testing.T) {
		s := findStep(steps, "nonexistent")
		if s != nil {
			t.Error("expected nil for nonexistent step")
		}
	})
}

func TestResolveNextSteps(t *testing.T) {
	allSteps := []Step{
		{ID: "step-a", Prompt: "a"},
		{ID: "step-b", Prompt: "b"},
		{ID: "step-c", Prompt: "c"},
		{ID: "step-d", Prompt: "d"},
	}

	tests := []struct {
		name         string
		rules        []Rule
		args         map[string]any
		wantStepIDs  []string
		wantPostpone []bool
	}{
		{
			name:        "no rules — terminal step",
			rules:       nil,
			args:        map[string]any{"status": "done"},
			wantStepIDs: nil,
		},
		{
			name:        "empty rules — terminal step",
			rules:       []Rule{},
			args:        map[string]any{"status": "done"},
			wantStepIDs: nil,
		},
		{
			name:        "unconditional rule — always advances",
			rules:       []Rule{{Then: "step-b"}},
			args:        map[string]any{},
			wantStepIDs: []string{"step-b"},
		},
		{
			name:        "conditional rule matches",
			rules:       []Rule{{If: "${args.status} == done", Then: "step-b"}},
			args:        map[string]any{"status": "done"},
			wantStepIDs: []string{"step-b"},
		},
		{
			name:        "conditional rule does not match — terminal",
			rules:       []Rule{{If: "${args.status} == done", Then: "step-b"}},
			args:        map[string]any{"status": "pending"},
			wantStepIDs: nil,
		},
		{
			name: "multiple rules — parallel fork",
			rules: []Rule{
				{If: "${args.status} == done", Then: "step-b"},
				{If: "${args.status} == done", Then: "step-c"},
			},
			args:        map[string]any{"status": "done"},
			wantStepIDs: []string{"step-b", "step-c"},
		},
		{
			name: "mixed conditional and unconditional",
			rules: []Rule{
				{If: "${args.status} == done", Then: "step-b"},
				{Then: "step-c"},
			},
			args:        map[string]any{"status": "pending"},
			wantStepIDs: []string{"step-c"},
		},
		{
			name: "all rules match including unconditional",
			rules: []Rule{
				{If: "${args.status} == done", Then: "step-b"},
				{Then: "step-c"},
			},
			args:        map[string]any{"status": "done"},
			wantStepIDs: []string{"step-b", "step-c"},
		},
		{
			name:        "rule references nonexistent step — skipped",
			rules:       []Rule{{Then: "nonexistent"}},
			args:        map[string]any{},
			wantStepIDs: nil,
		},
		{
			name:         "unconditional with postpone",
			rules:        []Rule{{Then: "step-b", Postpone: true}},
			args:         map[string]any{},
			wantStepIDs:  []string{"step-b"},
			wantPostpone: []bool{true},
		},
		{
			name:        "invalid predicate — skipped gracefully",
			rules:       []Rule{{If: "bad predicate", Then: "step-b"}},
			args:        map[string]any{},
			wantStepIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveNextSteps(tt.rules, allSteps, tt.args, nil)

			if len(got) != len(tt.wantStepIDs) {
				t.Fatalf("resolveNextSteps() returned %d steps, want %d", len(got), len(tt.wantStepIDs))
			}

			for i, rs := range got {
				if rs.step.ID != tt.wantStepIDs[i] {
					t.Errorf("step[%d].ID = %q, want %q", i, rs.step.ID, tt.wantStepIDs[i])
				}
				if tt.wantPostpone != nil && rs.postpone != tt.wantPostpone[i] {
					t.Errorf("step[%d].postpone = %v, want %v", i, rs.postpone, tt.wantPostpone[i])
				}
			}
		})
	}
}

func TestValidateArgs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"count":   map[string]any{"type": "integer"},
			"enabled": map[string]any{"type": "boolean"},
			"score":   map[string]any{"type": "number"},
			"tags":    map[string]any{"type": "array"},
			"meta":    map[string]any{"type": "object"},
		},
		"required": []any{"name"},
	}

	tests := []struct {
		name    string
		args    map[string]any
		schema  map[string]any
		wantErr bool
	}{
		{"nil schema passes", map[string]any{"anything": true}, nil, false},
		{"empty schema passes", map[string]any{"anything": true}, map[string]any{}, false},
		{"valid args", map[string]any{"name": "test", "count": 5}, schema, false},
		{"missing required", map[string]any{"count": 5}, schema, true},
		{"wrong type string", map[string]any{"name": 123}, schema, true},
		{"wrong type integer", map[string]any{"name": "ok", "count": "abc"}, schema, true},
		{"float as integer", map[string]any{"name": "ok", "count": 3.5}, schema, true},
		{"int-like float ok", map[string]any{"name": "ok", "count": float64(3)}, schema, false},
		{"wrong type boolean", map[string]any{"name": "ok", "enabled": "yes"}, schema, true},
		{"wrong type number", map[string]any{"name": "ok", "score": "high"}, schema, true},
		{"wrong type array", map[string]any{"name": "ok", "tags": "a,b"}, schema, true},
		{"wrong type object", map[string]any{"name": "ok", "meta": "flat"}, schema, true},
		{"valid all types", map[string]any{
			"name": "x", "count": 1, "enabled": true,
			"score": 3.14, "tags": []any{"a"}, "meta": map[string]any{"k": "v"},
		}, schema, false},
		{"prompt always allowed", map[string]any{"name": "ok", "prompt": 12345}, schema, false},
		{"unknown key with additionalProperties default", map[string]any{"name": "ok", "extra": "val"}, schema, false},
		{"unknown key with additionalProperties false", map[string]any{"name": "ok", "extra": "val"}, map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"name": map[string]any{"type": "string"}},
			"required":             []any{"name"},
			"additionalProperties": false,
		}, true},
		{"prompt bypasses additionalProperties false", map[string]any{"name": "ok", "prompt": "hello"}, map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"name": map[string]any{"type": "string"}},
			"required":             []any{"name"},
			"additionalProperties": false,
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(tt.args, tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
