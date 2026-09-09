package page

import (
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/tui/components/chat"
)

func TestShellResultOutput(t *testing.T) {
	tests := []struct {
		name        string
		msg         chat.ShellResultMsg
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "captured output with exit code",
			msg: chat.ShellResultMsg{
				Command:  "ls nope",
				Stderr:   "ls: nope: No such file or directory\n",
				ExitCode: 2,
			},
			wantContain: []string{"No such file or directory", "[exit code: 2]"},
		},
		{
			name:        "captured with no output",
			msg:         chat.ShellResultMsg{Command: "true"},
			wantContain: []string{"(no output)"},
		},
		{
			name: "interactive run says where the output went",
			msg: chat.ShellResultMsg{
				Command:     "sudo -v",
				Interactive: true,
				ExitCode:    0,
			},
			wantContain: []string{"ran interactively", "exit code: 0", "written to your terminal"},
			// An interactive run captured nothing; "(no output)" would read as
			// "the command printed nothing", which is the opposite of true.
			wantAbsent: []string{"(no output)"},
		},
		{
			name: "interactive run reports a failure status",
			msg: chat.ShellResultMsg{
				Command:     "sudo -v",
				Interactive: true,
				ExitCode:    1,
			},
			wantContain: []string{"exit code: 1"},
		},
		{
			name:        "cancelled run",
			msg:         chat.ShellResultMsg{Command: "sleep 300", Cancelled: true},
			wantContain: []string{"[cancelled]"},
			wantAbsent:  []string{"(no output)", "exit code"},
		},
		{
			name:        "error",
			msg:         chat.ShellResultMsg{Command: "x", Err: errors.New("failed to create shell instance")},
			wantContain: []string{"[error: failed to create shell instance]"},
		},
		{
			name: "needs-a-terminal hint is appended outside the code fence",
			msg: chat.ShellResultMsg{
				Command:  "my-tool --login",
				Stderr:   "my-tool: not a terminal\n",
				ExitCode: 1,
				Hint:     "This command needs a terminal. Re-run it as `!!my-tool --login`",
			},
			wantContain: []string{"not a terminal", "Re-run it as `!!my-tool --login`"},
		},
		{
			name:       "no hint when none was set",
			msg:        chat.ShellResultMsg{Command: "ls", Stdout: "a\nb\n"},
			wantAbsent: []string{"needs a terminal", "!!"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellResultOutput(tt.msg)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("output unexpectedly contains %q:\n%s", absent, got)
				}
			}
		})
	}
}

// TestShellResultOutputHintIsReadable guards the hint's placement: inside the
// fenced block it would render as command output rather than as advice.
func TestShellResultOutputHintIsReadable(t *testing.T) {
	got := shellResultOutput(chat.ShellResultMsg{
		Command: "sudo -v",
		Stderr:  "sudo: no tty present\n",
		Hint:    "HINT-MARKER",
	})
	fenceEnd := strings.LastIndex(got, "```")
	hintAt := strings.Index(got, "HINT-MARKER")
	if hintAt < fenceEnd {
		t.Errorf("hint rendered inside the code fence:\n%s", got)
	}
}
