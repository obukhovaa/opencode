package slashcmd

import (
	"errors"
	"testing"
)

// /new and /reset are aliases for the ctrl+n "new session" action and only
// make sense in the TUI.
func TestBuiltinNewSessionAliases(t *testing.T) {
	builtins := BuiltinCommands()

	for _, id := range []string{"new", "reset"} {
		t.Run(id, func(t *testing.T) {
			action, err := Resolve(Parse("/"+id), builtins, nil, true)
			if err != nil {
				t.Fatalf("interactive resolve: %v", err)
			}
			if action.Type != ActionCommand || action.Command.ID != id {
				t.Fatalf("got %v/%v, want command %q", action.Type, action.Command, id)
			}
			if _, err := Resolve(Parse("/"+id), builtins, nil, false); !errors.Is(err, ErrTUIOnly) {
				t.Fatalf("non-interactive resolve err = %v, want ErrTUIOnly", err)
			}
		})
	}
}
