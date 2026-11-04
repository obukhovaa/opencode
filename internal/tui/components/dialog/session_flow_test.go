package dialog

import (
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/session"
)

func TestSessionDialogKeepsOnlyRoots(t *testing.T) {
	d := NewSessionDialogCmp().(*sessionDialogCmp)
	d.SetSessions([]session.Session{
		{ID: "plain"},
		{ID: "root", RootSessionID: "root", FlowID: "flow", DynamicFlow: true},
		{ID: "step", RootSessionID: "root"},
		{ID: "task", RootSessionID: "root", ParentSessionID: "step"},
		{ID: "legacy-task", ParentSessionID: "plain"},
		{ID: "standalone-first", RootSessionID: "standalone-first", FlowID: "standalone"},
	})
	if len(d.sessions) != 3 {
		t.Fatalf("roots: %+v", d.sessions)
	}
	if d.sessions[2].ID != "standalone-first" {
		t.Fatal("standalone root disappeared")
	}
}
func TestSessionFlowMetadata(t *testing.T) {
	for _, tt := range []struct {
		sess session.Session
		want string
	}{
		{session.Session{DynamicFlow: true}, "dynamic flow"},
		{session.Session{FlowID: "standalone"}, "flow"},
	} {
		if got := formatSessionMetadata(tt.sess, time.Now()); !strings.Contains(got, tt.want) {
			t.Fatalf("%q lacks %q", got, tt.want)
		}
	}
}
