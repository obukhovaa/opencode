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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluatePredicate(tt.predicate, tt.args)
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
