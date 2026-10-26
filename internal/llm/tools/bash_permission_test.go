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
