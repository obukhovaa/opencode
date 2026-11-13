package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
)

type promptShellRegistry struct {
	workflowRegistry
	enabled bool
	action  permission.Action
}

func (r promptShellRegistry) IsToolEnabled(string, string) bool { return r.enabled }
func (r promptShellRegistry) EvaluatePermission(string, string, string) permission.Action {
	return r.action
}
func TestManagedPromptShellCannotBypassPolicyOrOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		enabled    bool
		action     permission.Action
		lost       bool
	}{
		{"generated", "generated", true, permission.ActionAllow, false},
		{"disabled", "named", false, permission.ActionAllow, false},
		{"denied", "named", true, permission.ActionDeny, false},
		{"ask-without-reviewer", "named", true, permission.ActionAsk, false},
		{"stale-owner", "named", true, permission.ActionAllow, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := filepath.Join(t.TempDir(), "executed")
			a := &App{Registry: promptShellRegistry{enabled: tc.enabled, action: tc.action}}
			ctx := context.Background()
			if tc.lost {
				ctx = tools.WithFlowOwnershipGuard(ctx, func(context.Context) error { return errors.New("lease lost") })
			}
			_, err := a.runManagedPromptShell(ctx, "root", "coder", tc.kind, fmt.Sprintf("printf touched > %q", sentinel))
			if err == nil {
				t.Fatal("unauthorized macro accepted")
			}
			if _, err = os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatal("macro executed before authorization")
			}
		})
	}
}
func TestManagedNamedPromptShellRetainsOutput(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	a.Registry = promptShellRegistry{enabled: true, action: permission.ActionAllow}
	output, err := a.runManagedPromptShell(ctx, root, "coder", "named", "printf '%s' macro-output")
	if err != nil || output != "macro-output" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}
