package dialog

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSplitInlineArgs: the last field takes the remainder so a trailing
// free-text argument (/loop's prompt, /rename's title) keeps its spaces.
func TestSplitInlineArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		argNames []string
		want     []string
	}{
		{"loop with a multi-word prompt", "5m check the build", []string{"interval", "prompt"}, []string{"5m", "check the build"}},
		{"rename takes the whole line", "a better title", []string{"title"}, []string{"a better title"}},
		{"no args leaves fields empty", "", []string{"interval", "prompt"}, []string{"", ""}},
		{"fewer args than fields", "5m", []string{"interval", "prompt"}, []string{"5m", ""}},
		{"surrounding whitespace trimmed", "  5m   run it  ", []string{"interval", "prompt"}, []string{"5m", "run it"}},
		{"no fields", "anything", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitInlineArgs(tt.args, tt.argNames)
			if !reflect.DeepEqual(got, tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
				t.Errorf("SplitInlineArgs(%q, %v) = %#v, want %#v", tt.args, tt.argNames, got, tt.want)
			}
		})
	}
}

// TestArgumentDialogPreFillsInitialValues: inline arguments must arrive in the
// fields, so submitting without retyping returns what the user already wrote.
func TestArgumentDialogPreFillsInitialValues(t *testing.T) {
	var m tea.Model = NewMultiArgumentsDialogCmp(ShowMultiArgumentsDialogMsg{
		CommandID:     "loop",
		ArgNames:      []string{"interval", "prompt"},
		InitialValues: []string{"5m", "check the build"},
	})

	// Enter on the first field advances; Enter on the last submits.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting returned no cmd")
	}
	closeMsg, ok := cmd().(CloseMultiArgumentsDialogMsg)
	if !ok {
		t.Fatalf("got %T, want CloseMultiArgumentsDialogMsg", cmd())
	}
	if !reflect.DeepEqual(closeMsg.Values, []string{"5m", "check the build"}) {
		t.Errorf("Values = %#v, want the pre-filled values", closeMsg.Values)
	}
}

// TestArgumentFieldsBoundsPositionalIndex: $ARGUMENTS[N] takes any number of
// digits, and the highest index sizes the staged argument list, so an
// unbounded one would have staging allocate a slice of that size.
func TestArgumentFieldsBoundsPositionalIndex(t *testing.T) {
	if _, _, _, ok := argumentFields("use $ARGUMENTS[500000000] here", "", false); ok {
		t.Error("an out-of-range positional index produced argument fields")
	}

	names, _, mode, ok := argumentFields("use $ARGUMENTS[0] and $ARGUMENTS[1]", "", false)
	if !ok || mode != ArgsModePositional || len(names) != 2 {
		t.Errorf("in-range indices = %v, mode %v, ok %v", names, mode, ok)
	}

	// A mix keeps the usable indices and drops only the absurd one.
	names, _, _, ok = argumentFields("use $ARGUMENTS[0] and $ARGUMENTS[900000000]", "", false)
	if !ok || len(names) != 1 || names[0] != "0" {
		t.Errorf("mixed indices = %v, ok %v", names, ok)
	}
}
