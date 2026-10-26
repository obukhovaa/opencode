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

// The four reviewed bash 5.x lines must be refused before either dispatch
// mode starts; policy alone decides, so this holds on macOS's bash 3.2 too.
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

// CD-5487: no safe-listed command may start another command, so a wrapper
// or a program-running option goes through the permission gate.
func TestIsSafeReadOnlyCommandRunsNoOtherCommand(t *testing.T) {
	for _, command := range []string{
		"env bash -c 'printf ok'", "env -i sh -c x", "env X=1 sh", "nice sh", "time sh",
		"timeout 5 sh", "go run ./x", "go test ./...", "go test -exec sh ./...",
		"go build -toolexec 'sh -c x' ./...", "go vet -vettool=/tmp/x ./...", "go list -toolexec=x",
		"go env -w GOFLAGS=-toolexec=x", "git grep -O'sh -c x' foo", "git grep --open-files-in-pager=x foo",
		"git ls-remote --upload-pack='sh -c x' .", "git ls-remote -u x .",
		`echo ${x:=\$\(touch MK\)} ${x@P}`, "echo $((x))", "echo $(id)", "echo <(id)",
	} {
		if IsSafeReadOnlyCommand(command) {
			t.Errorf("%q must go through the permission gate", command)
		}
	}
	for _, command := range []string{
		"env", "printenv HOME", "go build ./...", "go env GOPATH", "go vet ./...", "git grep -n foo",
		"git ls-remote origin", "echo $HOME ${x:-y}", `echo "a b" 'c'`, "go list -m all",
	} {
		if !IsSafeReadOnlyCommand(command) {
			t.Errorf("%q should stay exempt", command)
		}
	}
}

// CD-5487 done-when: `env bash -c '…'` is denied under "*": deny.
func TestBashRunDeniesSafeListWrapper(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	t.Setenv("SHELL", "/bin/bash")
	dir := t.TempDir()
	marker := filepath.Join(dir, "should-not-exist")
	input, err := json.Marshal(BashParams{Command: fmt.Sprintf("env bash -c 'touch %s'", marker), Workdir: dir})
	if err != nil {
		t.Fatal(err)
	}
	b := NewBashTool(nil, bashPermissionRegistry{rules: map[string]any{"bash": map[string]any{"*": "deny"}}})
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "safe-list-wrapper")
	ctx = context.WithValue(ctx, MessageIDContextKey, "msg")
	_, err = b.Run(ctx, ToolCall{ID: "wrapper", Input: string(input)})
	if _, denied := permission.AsRuleDenied(err); !denied {
		t.Fatalf("want configured-rule denial, got %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrapped command ran: stat error %v", err)
	}
}
