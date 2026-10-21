package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
	"github.com/opencode-ai/opencode/internal/toolenv"
)

// Ripgrep may run a preprocessor from RIPGREP_CONFIG_PATH. Its environment
// must be filtered just like the bash tool's, including when the preprocessor
// is a script inside the workspace.
func TestGrepPreprocessor_DeniedEnvironment(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	leaks := setDeniedEnv(t)
	dir := t.TempDir()
	preprocessor := filepath.Join(dir, "preprocess")
	if err := os.WriteFile(preprocessor, []byte(`#!/bin/sh
printf 'OPENCODE_SERVER_PASSWORD=%s\nTOOLS_TEST_DENIED=%s\nTOOLS_TEST_KEPT=%s\n' "${OPENCODE_SERVER_PASSWORD-unset}" "${TOOLS_TEST_DENIED-unset}" "$TOOLS_TEST_KEPT"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "ripgreprc")
	if err := os.WriteFile(configPath, []byte("--pre="+preprocessor+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", configPath)
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte("sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := searchWithRipgrepModes(context.Background(), ".", input, &GrepParams{}, "", "content", 100, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvOutput(t, out, leaks)
	if !strings.Contains(out, "OPENCODE_SERVER_PASSWORD=unset") || !strings.Contains(out, "TOOLS_TEST_DENIED=unset") {
		t.Fatalf("preprocessor did not receive the filtered environment: %s", out)
	}
}

// setDeniedEnv puts a built-in secret, an operator-denied name and an
// undenied probe into the environment, and returns the strings that must not
// appear in a child's `env` output.
func setDeniedEnv(t *testing.T) []string {
	t.Helper()
	t.Setenv("OPENCODE_SERVER_PASSWORD", "api-s3cret")
	t.Setenv("TOOLS_TEST_DENIED", "op-s3cret")
	t.Setenv(toolenv.EnvVar, "TOOLS_TEST_DENIED")
	t.Setenv("TOOLS_TEST_KEPT", "kept-value")
	return []string{"api-s3cret", "op-s3cret"}
}

func assertEnvOutput(t *testing.T, out string, leaks []string) {
	t.Helper()
	for _, leaked := range leaks {
		if strings.Contains(out, leaked) {
			t.Errorf("child env output contains %q", leaked)
		}
	}
	if !strings.Contains(out, "TOOLS_TEST_KEPT=kept-value") {
		t.Errorf("an undenied variable was dropped; output:\n%s", out)
	}
}

func TestBashBackground_DeniedEnvironment(t *testing.T) {
	leaks := setDeniedEnv(t)
	deps, cleanup := setupBashBgFixture(t)
	defer cleanup()

	tool := &bashTool{}
	params := BashParams{Command: "env", Description: "env probe", RunInBackground: true}
	if _, err := tool.runBackground(context.Background(), ToolCall{ID: "call-env"}, params, t.TempDir(), "s-env"); err != nil {
		t.Fatalf("runBackground: %v", err)
	}
	waitForPair(t, deps, 5*time.Second)
	pairs := deps.collect()
	if len(pairs) != 1 {
		t.Fatalf("pairs: want 1, got %d", len(pairs))
	}
	assertEnvOutput(t, pairs[0].ToolContent, leaks)
}

func TestMonitor_DeniedEnvironment(t *testing.T) {
	leaks := setDeniedEnv(t)
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()

	tool := NewMonitorTool(&allowAllPerms{}, allowAllAgentRegistry{})
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "SESS")
	ctx = context.WithValue(ctx, MessageIDContextKey, "MSG")
	resp, err := tool.Run(ctx, ToolCall{ID: "mon-env", Input: `{"cmd":"env","pattern":"zzz-never","description":"env probe"}`})
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`task_id: (monitor_[A-Z2-7]+)`).FindStringSubmatch(resp.Content)
	if m == nil {
		t.Fatalf("no task_id in ack: %s", resp.Content)
	}
	tk, ok := reg.Get(m[1])
	if !ok {
		t.Fatal("monitor not registered")
	}
	deadline := time.Now().Add(5 * time.Second)
	for tk.State() == task.StateRunning && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if tk.State() == task.StateRunning {
		t.Fatal("monitor did not finish")
	}
	out, err := os.ReadFile(tk.OutputPath)
	if err != nil {
		t.Fatalf("read monitor output: %v", err)
	}
	assertEnvOutput(t, string(out), leaks)
}
