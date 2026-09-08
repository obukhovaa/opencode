package slashcmd

import (
	"reflect"
	"testing"
)

func TestNamedPlaceholders(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"none", "no placeholders here", nil},
		{"single", "review $TARGET now", []string{"TARGET"}},
		{"first appearance order", "$SCOPE then $TARGET", []string{"SCOPE", "TARGET"}},
		{"deduplicated", "$FOO appears twice: $FOO", []string{"FOO"}},
		{"underscores and digits", "$FOO_BAR and $BAZ123", []string{"FOO_BAR", "BAZ123"}},
		{"ARGUMENTS is not a named placeholder", "do $ARGUMENTS", nil},
		{"ARGUMENTS excluded from a mixed set", "$ARGUMENTS for $TARGET", []string{"TARGET"}},
		{"indexed ARGUMENTS excluded", "$ARGUMENTS[0] and $ARGUMENTS[1]", nil},
		{"lowercase is not a placeholder", "cost is $usd", nil},
		{"leading digit is not a placeholder", "$1INVALID", nil},
		{"dollar amount is not a placeholder", "costs $50 today", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NamedPlaceholders(tt.content); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NamedPlaceholders(%q) = %#v, want %#v", tt.content, got, tt.want)
			}
		})
	}
}

func TestBindNamed(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		names      []string
		positional []string
		want       string
	}{
		{
			name:       "binds by index",
			content:    "review $TARGET in $SCOPE",
			names:      []string{"TARGET", "SCOPE"},
			positional: []string{"HEAD~3", "src/"},
			want:       "review HEAD~3 in src/",
		},
		{
			name:       "missing positional binds empty",
			content:    "review $TARGET in $SCOPE",
			names:      []string{"TARGET", "SCOPE"},
			positional: []string{"HEAD~3"},
			want:       "review HEAD~3 in ",
		},
		{
			// A name that is a prefix of another must not corrupt the longer one:
			// the single regex pass matches the longest name, so $FOO_BAR is one
			// placeholder rather than $FOO followed by literal "_BAR".
			name:       "prefix names do not collide",
			content:    "$FOO and $FOO_BAR",
			names:      []string{"FOO", "FOO_BAR"},
			positional: []string{"a", "b"},
			want:       "a and b",
		},
		{
			name:       "ARGUMENTS is left for SubstituteContent",
			content:    "$TARGET then $ARGUMENTS",
			names:      []string{"TARGET"},
			positional: []string{"x"},
			want:       "x then $ARGUMENTS",
		},
		{
			name:       "no names is a no-op",
			content:    "$TARGET stays",
			names:      nil,
			positional: []string{"x"},
			want:       "$TARGET stays",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bindNamed(tt.content, tt.names, tt.positional); got != tt.want {
				t.Errorf("bindNamed() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommandInfoIsAction(t *testing.T) {
	// Every builtin is classified here explicitly: a new builtin that neither
	// carries prompt content nor is listed as an action fails this test, which is
	// the point — the classification must be a conscious choice.
	wantAction := map[string]bool{
		"init":             false,
		"review":           false,
		"commit":           false,
		"new":              true,
		"reset":            true,
		"compact":          true,
		"agents":           true,
		"auto-approve":     true,
		"vim":              true,
		"sessions-cleanup": true,
		"rename":           true,
		"loop":             true,
		"crons":            true,
	}

	builtins := BuiltinCommands()
	if len(builtins) != len(wantAction) {
		t.Fatalf("BuiltinCommands() has %d entries, the expectation table has %d — classify the new command", len(builtins), len(wantAction))
	}

	for _, b := range builtins {
		want, ok := wantAction[b.ID]
		if !ok {
			t.Errorf("builtin %q is not classified in the expectation table", b.ID)
			continue
		}
		if got := b.IsAction(); got != want {
			t.Errorf("%q.IsAction() = %v, want %v", b.ID, got, want)
		}
	}

	// A custom command with an empty body is a prompt command that expands to
	// nothing, not an action.
	empty := CommandInfo{ID: "project:empty", Content: ""}
	if empty.IsAction() {
		t.Error("a non-TUIOnly command with an empty body must not be an action")
	}
}
