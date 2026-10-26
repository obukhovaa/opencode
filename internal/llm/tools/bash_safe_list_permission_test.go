package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/permission"
)

func safeListCall(t *testing.T, command string, background bool) (context.Context, ToolCall, string) {
	t.Helper()
	dir := t.TempDir()
	input, err := json.Marshal(BashParams{Command: command, Workdir: dir, RunInBackground: background})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "safe-list-permission")
	ctx = context.WithValue(ctx, MessageIDContextKey, "msg")
	return ctx, ToolCall{ID: "safe-list", Input: string(input)}, dir
}

type rejectingBashPrompt struct {
	permission.Service
	calls int
}

func (p *rejectingBashPrompt) Request(context.Context, permission.CreatePermissionRequest) bool {
	p.calls++
	return false
}

func TestBashSafeListRespectsAsk(t *testing.T) {
	ctx, call, _ := safeListCall(t, "pwd", false)
	p := &rejectingBashPrompt{}
	_, err := NewBashTool(p, bashPermissionRegistry{}).Run(ctx, call)
	if !errors.Is(err, permission.ErrorPermissionDenied) || p.calls != 1 {
		t.Fatalf("unconfigured pwd must prompt once: calls=%d error=%v", p.calls, err)
	}
	if _, rule := permission.AsRuleDenied(err); rule {
		t.Fatal("a rejected prompt must retain its distinct denial result")
	}
}

func TestBashSafeListExplicitAllowStillExecutes(t *testing.T) {
	deps, cleanup := setupBashBgFixture(t)
	defer cleanup()
	ctx, call, _ := safeListCall(t, `env /bin/bash -c 'printf permitted'`, true)
	p := &rejectingBashPrompt{}
	reg := bashPermissionRegistry{rules: map[string]any{"bash": "allow"}}
	_, err := NewBashTool(p, reg).Run(ctx, call)
	if err != nil || p.calls != 0 {
		t.Fatalf("explicitly allowed wrapper should run: calls=%d error=%v", p.calls, err)
	}
	waitForPair(t, deps, 5*time.Second)
	pairs := deps.collect()
	if len(pairs) != 1 || !strings.Contains(pairs[0].ToolContent, "permitted") {
		t.Fatal("allowed wrapper did not produce its harmless output")
	}
}

func TestBashSafeListCannotBypassDeny(t *testing.T) {
	for _, background := range []bool{false, true} {
		for name, command := range map[string]string{
			"env":     `env /bin/bash -c 'printf marker > MK'`,
			"nice":    `nice /bin/bash -c 'printf marker > MK'`,
			"time":    `time /bin/bash -c 'printf marker > MK'`,
			"timeout": `timeout 1 /bin/bash -c 'printf marker > MK'`,
			"go-run":  `go run missing.go`, "go-test": `go test -run '^$' ./missing`,
			"go-build": `go build ./missing`, "go-install": `go install ./missing`,
			"pwd": `pwd`, "ls": `ls`, "echo": `echo harmless`, "git-status": `git status`,
		} {
			t.Run(name+map[bool]string{false: "/foreground", true: "/background"}[background], func(t *testing.T) {
				_, cleanup := setupBashBgFixture(t)
				defer cleanup()
				t.Setenv("SHELL", "/bin/bash")
				ctx, call, dir := safeListCall(t, command, background)
				reg := bashPermissionRegistry{rules: map[string]any{"bash": map[string]any{"*": "deny"}}}
				_, err := NewBashTool(nil, reg).Run(ctx, call)
				if _, denied := permission.AsRuleDenied(err); !denied {
					t.Fatalf("safe-list entry escaped deny-all: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "MK")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("denied wrapper executed: %v", err)
				}
			})
		}
	}
}
