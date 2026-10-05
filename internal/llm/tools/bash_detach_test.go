package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestDetectSelfDetach(t *testing.T) {
	cases := []struct {
		cmd       string
		construct string // "" = not detaching
	}{
		// Positives.
		{"nohup ./gradlew test > /tmp/log 2>&1 &\necho $!", "nohup"},
		{"nohup x &", "nohup"},
		{"/usr/bin/nohup x", "nohup"},
		{"FOO=bar nohup x", "nohup"},
		{"x &", "&"},
		{"x & echo $!", "&"},
		{"setsid x", "setsid"},
		{"cd /tmp; disown", "disown"},
		{"echo start; nohup ./build.sh", "nohup"},
		// Negatives.
		{"a && b", ""},
		{"go build ./... && go test ./...", ""},
		{"echo 'a & b'", ""},
		{`echo "a & b"`, ""},
		{"x 2>&1", ""},
		{"x >&2", ""},
		{"x &>log", ""},
		{"x &>>log", ""},
		{"(x &)", ""},
		{"echo $(x &)", ""},
		{"echo `x &`", ""},
		{"echo nohup", ""},
		{"cat nohup.out", ""},
		// Ambiguous (unbalanced) → allowed through.
		{"echo 'unterminated &", ""},
		{"(x &", ""},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, detaches := detectSelfDetach(tc.cmd)
			if detaches != (tc.construct != "") || got != tc.construct {
				t.Errorf("detectSelfDetach(%q) = (%q, %v), want %q", tc.cmd, got, detaches, tc.construct)
			}
		})
	}
}

func TestIsSafeReadOnlyCommand_SimpleCommandsOnly(t *testing.T) {
	safe := []string{
		"ls -la", "git status", "git log --oneline -5", "echo 'a; b'", `echo "a | b"`,
		"echo $HOME", "go test ./...", "git diff HEAD~1", "pwd", "date",
	}
	for _, c := range safe {
		if !IsSafeReadOnlyCommand(c) {
			t.Errorf("%q should still be exempt", c)
		}
	}
	unsafe := []string{
		"nohup x", "nohup ./gradlew build > /tmp/log 2>&1 &",
		"echo a; rm b", "echo start; ./gradlew build &", "ls && rm b", "echo x > f", "echo x >> f",
		"ls | xargs rm", "echo $(rm b)", "echo `rm b`", "git status 2>&1", "ls || rm b",
		"echo a\nrm b", "(ls)", "echo 'unterminated",
	}
	for _, c := range unsafe {
		if IsSafeReadOnlyCommand(c) {
			t.Errorf("%q must not be exempt", c)
		}
	}
}

// Full bash.Run path: a self-detaching run_in_background command is refused
// before anything is spawned — no task, no output file, an error result.
func TestBashRun_RejectsSelfDetachingBackgroundCommand(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(wd, false); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()

	bash := NewBashTool(&allowAllPerms{}, agentregistry.GetRegistry())
	ctx := waitFixtureCtx(false)
	for _, cmd := range []string{"nohup ./x > /tmp/log 2>&1 &", "./x &", "setsid ./x"} {
		resp, err := bash.Run(ctx, ToolCall{ID: "detach-call", Input: `{"command":` + jsonString(cmd) + `,"description":"d","run_in_background":true}`})
		if err != nil {
			t.Fatalf("%q: a policy rejection must be a tool error, not a Go error: %v", cmd, err)
		}
		if !resp.IsError || !strings.Contains(resp.Content, "run_in_background") || !strings.Contains(resp.Content, "Do NOT fall back") {
			t.Errorf("%q: expected a rejection naming the fix and foreclosing the foreground fallback; got isError=%v:\n%s", cmd, resp.IsError, resp.Content)
		}
	}
	if got := reg.ListBySession("SESS"); len(got) != 0 {
		t.Errorf("rejected commands must register no task; got %d", len(got))
	}
	// A synchronous call with the same shape is the caller's business.
	resp, err := bash.Run(ctx, ToolCall{ID: "sync-call", Input: `{"command":"echo sync-ok &","description":"d"}`})
	if err != nil || resp.IsError {
		t.Errorf("synchronous call must not be gated: %v %+v", err, resp)
	}
}

func jsonString(s string) string {
	b := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + b + `"`
}

var _ = context.Background
