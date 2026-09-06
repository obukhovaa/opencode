package tui

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/tui/components/chat"
)

// /new and /reset must behave exactly like the ctrl+n keybinding, i.e. emit
// chat.SessionClearedMsg.
func TestBuildCommandsNewAndResetClearSession(t *testing.T) {
	commands := buildCommands()

	for _, id := range []string{"new", "reset"} {
		t.Run(id, func(t *testing.T) {
			var found bool
			for _, cmd := range commands {
				if cmd.ID != id {
					continue
				}
				found = true
				if cmd.Handler == nil {
					t.Fatalf("/%s has no handler", id)
				}
				teaCmd := cmd.Handler(cmd)
				if teaCmd == nil {
					t.Fatalf("/%s handler returned nil cmd", id)
				}
				if _, ok := teaCmd().(chat.SessionClearedMsg); !ok {
					t.Fatalf("/%s emitted %T, want chat.SessionClearedMsg", id, teaCmd())
				}
			}
			if !found {
				t.Fatalf("/%s is not registered", id)
			}
		})
	}
}
