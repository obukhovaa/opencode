package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/permission"
)

type bashPermissionRegistry struct {
	agentregistry.Registry
	rules map[string]any
}

func (r bashPermissionRegistry) EvaluatePermission(_, tool, input string) permission.Action {
	return permission.EvaluateToolPermission(tool, input, r.rules, nil)
}

// CD-5483: reject the entire call before even its allowed first command
// executes. Neither synchronous nor detached execution may bypass this gate.
func TestBashRunRejectsDisallowedCompoundBeforeExecution(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			_, cleanup := setupBashBgFixture(t)
			defer cleanup()
			t.Setenv("SHELL", "/bin/bash")
			dir := t.TempDir()
			marker := filepath.Join(dir, "should-not-exist")
			params := BashParams{
				Command: fmt.Sprintf("printf allowed > %q; printf forbidden", marker),
				Workdir: dir, RunInBackground: background,
			}
			input, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			reg := bashPermissionRegistry{rules: map[string]any{"bash": map[string]any{
				"*": "deny", "printf allowed*": "allow",
			}}}
			b := NewBashTool(nil, reg)
			ctx := context.WithValue(context.Background(), SessionIDContextKey, "compound-permission")
			ctx = context.WithValue(ctx, MessageIDContextKey, "msg")
			_, err = b.Run(ctx, ToolCall{ID: "compound", Input: string(input)})
			if _, denied := permission.AsRuleDenied(err); !denied {
				t.Fatalf("want configured-rule denial, got %v", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("allowed prefix ran before the denial: stat error %v", err)
			}
		})
	}
}

func TestBashRunRejectsReevaluatedExpansionsBeforeExecution(t *testing.T) {
	commands := []string{
		`printf allowed ${x:=a[\$\(touch MK\)]} $((x))`,
		`printf allowed ${x:=a[\$\(touch MK\)]} ${!x}`,
		`printf allowed ${x:=a[\$\(touch MK\)]} ${y[x]}`,
		`printf allowed ${x:=\$\(touch MK\)} ${x@P}`,
	}
	for _, background := range []bool{false, true} {
		for _, command := range commands {
			t.Run(fmt.Sprintf("background=%t/%s", background, command), func(t *testing.T) {
				_, cleanup := setupBashBgFixture(t)
				defer cleanup()
				t.Setenv("SHELL", "/bin/bash")
				dir := t.TempDir()
				input, err := json.Marshal(BashParams{Command: command, Workdir: dir, RunInBackground: background})
				if err != nil {
					t.Fatal(err)
				}
				reg := bashPermissionRegistry{rules: map[string]any{"bash": map[string]any{"*": "deny", "printf allowed*": "allow"}}}
				ctx := context.WithValue(context.Background(), SessionIDContextKey, "expansion-permission")
				ctx = context.WithValue(ctx, MessageIDContextKey, "msg")
				_, err = NewBashTool(nil, reg).Run(ctx, ToolCall{ID: "expansion", Input: string(input)})
				if _, denied := permission.AsRuleDenied(err); !denied {
					t.Fatalf("want rule denial before execution, got %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "MK")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("hidden command ran: stat error %v", err)
				}
			})
		}
	}
}

func TestPermissionForBashExecutionRespectsTheActualDialect(t *testing.T) {
	for _, tt := range []struct {
		shell      string
		background bool
		want       permission.Action
	}{
		{"/bin/bash", false, permission.ActionAllow},
		{"/opt/homebrew/bin/bash", false, permission.ActionAllow},
		{"/bin/sh", false, permission.ActionAllow},
		{"/bin/zsh", false, permission.ActionAsk},
		{"/usr/bin/fish", false, permission.ActionAsk},
		{"/bin/zsh", true, permission.ActionAllow},
	} {
		if got := permissionForBashExecution(permission.ActionAllow, tt.background, tt.shell); got != tt.want {
			t.Errorf("%s background=%t: got %s, want %s", tt.shell, tt.background, got, tt.want)
		}
		for _, action := range []permission.Action{permission.ActionDeny, permission.ActionAsk} {
			if got := permissionForBashExecution(action, tt.background, tt.shell); got != action {
				t.Errorf("%s changed %s to %s", tt.shell, action, got)
			}
		}
	}
}
