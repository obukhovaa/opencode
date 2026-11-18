package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/stretchr/testify/require"
)

func monitorTestContext() context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "monitor-test")
	return context.WithValue(ctx, MessageIDContextKey, "msg")
}

func TestMonitor_Validation(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"missing mode", `{}`, "exactly one of cmd"},
		{"blank command", `{"command":"  "}`, "exactly one of cmd"},
		{"blank cmd", `{"cmd":"  "}`, "exactly one of cmd"},
		{"both modes", `{"cmd":"echo","command":"echo hi"}`, "cannot be combined"},
		{"shell with args", `{"command":"echo hi","args":["hi"]}`, "cannot be combined"},
		{"shell with empty args", `{"command":"echo hi","args":[]}`, "cannot be combined"},
		{"args only", `{"args":["hi"]}`, "exactly one of cmd"},
		{"string args", `{"cmd":"echo","args":"hi"}`, `args must be an array of strings, e.g. ["logs", "-f", "pod"]`},
		{"nonstring arg", `{"cmd":"echo","args":[1]}`, "args must be an array of strings"},
		{"bad regex", `{"command":"echo hi","pattern":"["}`, "invalid pattern regex"},
		{"RE2 rejects lookahead", `{"command":"echo hi","pattern":"(?=hi)"}`, "invalid pattern regex"},
		{"bad interval", `{"cmd":"echo","min_interval_ms":1}`, "min_interval_ms must be"},
		{"bad cap", `{"cmd":"echo","max_events":-1}`, "max_events must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No dependencies: validation must reject before permission or spawn.
			resp, err := NewMonitorTool(nil, nil).Run(monitorTestContext(), ToolCall{Input: tc.input})
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, tc.want)
		})
	}
}

func TestMonitor_InputModesAndOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params MonitorParams
		output string
		event  string
		status string
	}{
		{"shell success", MonitorParams{Command: `printf '\nhello\n' | tr a-z A-Z; printf 'warning\n' >&2`}, "\nHELLO\nwarning\n", "2 match(es)", "Monitor stream ended"},
		{"shell failure", MonitorParams{Command: `printf 'failed\n' >&2; exit 7`}, "failed\n", "failed", "Monitor script failed (exit 7)"},
		{"legacy shell", MonitorParams{Cmd: "bash", Args: []string{"-c", `printf 'legacy\n'`}}, "legacy\n", "legacy", "Monitor stream ended"},
		{"direct argv", MonitorParams{Cmd: "printf", Args: []string{"%s\n", "literal | $HOME"}}, "literal | $HOME\n", "literal | $HOME", "Monitor stream ended"},
		{"explicit pattern", MonitorParams{Command: `printf 'ignore\n'; printf 'FAIL\n' >&2`, Pattern: "OK|FAIL"}, "ignore\nFAIL\n", "1 match(es) in window:\nFAIL", "Monitor stream ended"},
		{"one event", MonitorParams{Command: `printf 'OK\n'`, Pattern: "OK|FAIL", MaxEvents: 1}, "OK\n", "OK", "max_events reached (1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, cleanup := setupBashBgFixture(t)
			defer cleanup()
			input, err := json.Marshal(tc.params)
			require.NoError(t, err)
			resp, err := NewMonitorTool(&allowAllPerms{}, allowAllAgentRegistry{}).Run(monitorTestContext(), ToolCall{ID: "monitor-call", Input: string(input)})
			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)
			var meta MonitorResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
			wantPattern := tc.params.Pattern
			if wantPattern == "" {
				wantPattern = "."
			}
			require.Equal(t, wantPattern, meta.Pattern)
			require.Eventually(t, func() bool {
				deps.mu.Lock()
				defer deps.mu.Unlock()
				return deps.resumes >= 2
			}, 5*time.Second, 10*time.Millisecond, "event and terminal notification must arrive")
			pairs := deps.collect()
			require.Len(t, pairs, 2)
			require.Contains(t, pairs[0].ToolContent, tc.event)
			require.Contains(t, pairs[1].ToolContent, tc.status)
			var synthetic MonitorParams
			require.NoError(t, json.Unmarshal([]byte(pairs[0].AssistantInput), &synthetic))
			require.Equal(t, tc.params.Command, synthetic.Command)
			require.Equal(t, wantPattern, synthetic.Pattern)
			out, err := os.ReadFile(meta.OutputPath)
			require.NoError(t, err)
			// Separate stdout/stderr copy goroutines may merge in either order.
			require.ElementsMatch(t, strings.Split(tc.output, "\n"), strings.Split(string(out), "\n"))
		})
	}
}

type monitorPermissionRegistry struct {
	allowAllAgentRegistry
	input string
	tool  string
}

func (r *monitorPermissionRegistry) EvaluatePermission(_, tool, input string) permission.Action {
	r.input, r.tool = input, tool
	return permission.ActionDeny
}

func TestMonitor_PermissionInput(t *testing.T) {
	for _, params := range []MonitorParams{
		{Command: "printf allowed | cat; printf forbidden >&2"},
		{Cmd: "printf", Args: []string{"legacy"}},
	} {
		reg := &monitorPermissionRegistry{}
		input, err := json.Marshal(params)
		require.NoError(t, err)
		_, err = NewMonitorTool(nil, reg).Run(monitorTestContext(), ToolCall{Input: string(input)})
		_, denied := permission.AsRuleDenied(err)
		require.True(t, denied, "expected rule denial, got %v", err)
		want := params.Command
		if want == "" {
			want = params.Cmd
		}
		require.Equal(t, want, reg.input)
		require.Equal(t, MonitorToolName, reg.tool)
	}
}

func TestMonitor_ShellEnvironmentAndCwd(t *testing.T) {
	leaks := setDeniedEnv(t)
	deps, cleanup := setupBashBgFixture(t)
	defer cleanup()
	dir := t.TempDir()
	params := MonitorParams{Command: `[[ -n "$BASH_VERSION" ]] || exit 9; pwd > cwd.txt; env`, Cwd: dir}
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := NewMonitorTool(&allowAllPerms{}, allowAllAgentRegistry{}).Run(monitorTestContext(), ToolCall{ID: "monitor-env", Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Eventually(t, func() bool {
		deps.mu.Lock()
		defer deps.mu.Unlock()
		return deps.resumes >= 2
	}, 5*time.Second, 10*time.Millisecond)
	pairs := deps.collect()
	require.Len(t, pairs, 2)
	require.Contains(t, pairs[1].ToolContent, "Monitor stream ended")
	var meta MonitorResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	out, err := os.ReadFile(meta.OutputPath)
	require.NoError(t, err)
	assertEnvOutput(t, string(out), leaks)
	cwd, err := os.ReadFile(filepath.Join(dir, "cwd.txt"))
	require.NoError(t, err)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, wantDir, strings.TrimSpace(string(cwd)))
}

func TestMonitor_DescriptionAndSchema(t *testing.T) {
	info := NewMonitorTool(nil, nil).Info()
	require.LessOrEqual(t, len(info.Description), 2048)
	require.Empty(t, info.Required, "runtime validates the alternative input modes")
	for _, name := range []string{"cmd", "command", "args", "cwd", "pattern", "min_interval_ms", "max_events", "description"} {
		require.Contains(t, info.Parameters, name)
	}
	for _, guidance := range []string{"yield alone", "independent work", "run_in_background: true", "finite waiter", "success and failure", "max_events: 1", "script may sleep", "Silence is healthy", "RE2", "merged stdout and stderr", "buffer", "bash -c"} {
		require.True(t, strings.Contains(info.Description, guidance), "missing guidance %q", guidance)
	}
}
