package clitool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

// writeScript creates an executable shell script and returns its path. The
// script is the binary under test; the executor itself never uses a shell.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExec_ArgvVerbatimAndExitStatus(t *testing.T) {
	wd := t.TempDir()
	script := writeScript(t, wd, "args.sh", `printf '%s\n' "$#"; for a in "$@"; do printf '[%s]\n' "$a"; done; echo oops >&2; exit 5`)
	m := mustParse(t, wd, "args.yaml", "name: args\ndescription: d\ncommand: "+script+"\nprefixArgs: [\"p1\"]\n")
	inv, err := m.Prepare(map[string]any{"args": []any{"x; rm -rf /tmp/y", "select 'a && b'"}})
	if err != nil {
		t.Fatal(err)
	}
	res := m.Exec(context.Background(), inv)
	if res.ExitCode != 5 || !res.Failed() {
		t.Errorf("exit = %d failed=%v", res.ExitCode, res.Failed())
	}
	if !strings.HasPrefix(res.Stdout, "3\n[p1]\n[x; rm -rf /tmp/y]\n[select 'a && b']\n") {
		t.Errorf("arguments were not passed verbatim: %q", res.Stdout)
	}
	text, isErr := FormatResult(res)
	if !isErr || !strings.Contains(text, "--- stderr ---\noops") || !strings.HasSuffix(text, "exit status 5") {
		t.Errorf("formatted = %q isErr=%v", text, isErr)
	}
}

func TestExec_StdinCwdAndEnv(t *testing.T) {
	wd := t.TempDir()
	sub := filepath.Join(wd, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, wd, "env.sh", `cat; echo; pwd; env | sort`)
	t.Setenv("CLITOOL_SECRET", "s3cr3t")
	t.Setenv("CLITOOL_PASS", "kept")
	m := mustParse(t, wd, "env.yaml", `
name: env
description: d
command: `+script+`
stdin: true
cwd: sub
env:
  inherit: false
  pass: [CLITOOL_PASS]
  set:
    EXTRA: "${env.CLITOOL_PASS}-x"
    MISSING: "${env.NOPE_NOT_SET}"
`)
	inv, err := m.Prepare(map[string]any{"args": []any{}, "stdin": "from stdin"})
	if err != nil {
		t.Fatal(err)
	}
	res := m.Exec(context.Background(), inv)
	if res.Failed() {
		t.Fatalf("run failed: %+v", res)
	}
	out := res.Stdout
	if !strings.HasPrefix(out, "from stdin\n") {
		t.Errorf("stdin not delivered: %q", out)
	}
	resolvedSub, _ := filepath.EvalSymlinks(sub)
	if !strings.Contains(out, "\n"+resolvedSub+"\n") {
		t.Errorf("cwd not applied: %q", out)
	}
	if strings.Contains(out, "CLITOOL_SECRET=") {
		t.Error("inherit:false leaked a non-passed variable")
	}
	for _, want := range []string{"CLITOOL_PASS=kept", "EXTRA=kept-x", "MISSING=", "PATH="} {
		if !strings.Contains(out, want) {
			t.Errorf("env missing %q in %q", want, out)
		}
	}
}

func TestExec_InheritPassesParentEnv(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "say.yaml", echoManifest)
	t.Setenv("CLITOOL_INHERITED", "yes")
	env := m.BuildEnv(os.Environ())
	if !containsPrefix(env, "CLITOOL_INHERITED=yes") {
		t.Error("default inherit should pass the parent environment")
	}
}

// A denied name must reach a CLI tool through none of the manifest's env
// routes, while a literal env.set value is the operator's explicit grant.
func TestBuildEnv_DropsDeniedVariables(t *testing.T) {
	wd := t.TempDir()
	t.Setenv("OPENCODE_SERVER_PASSWORD", "api-s3cret")
	t.Setenv("CLITOOL_DENIED", "op-s3cret")
	t.Setenv(toolenv.EnvVar, "CLITOOL_DENIED")
	t.Setenv("CLITOOL_KEPT", "kept")

	t.Run("inherit", func(t *testing.T) {
		m := mustParse(t, wd, "say.yaml", echoManifest)
		env := m.BuildEnv(os.Environ())
		if containsPrefix(env, "OPENCODE_SERVER_PASSWORD=") || containsPrefix(env, "CLITOOL_DENIED=") {
			t.Errorf("inherit passed a denied variable: %q", env)
		}
		if !containsPrefix(env, "CLITOOL_KEPT=kept") {
			t.Error("inherit dropped an undenied variable")
		}
	})

	t.Run("pass and set", func(t *testing.T) {
		m := mustParse(t, wd, "restricted.yaml", "name: restricted\ndescription: d\ncommand: echo\nenv:\n  inherit: false\n  pass: [CLITOOL_DENIED, CLITOOL_KEPT]\n  set:\n    REF: \"${env.CLITOOL_DENIED}\"\n    OPENCODE_SERVER_PASSWORD: granted-literal\n")
		env := m.BuildEnv(os.Environ())
		if containsPrefix(env, "CLITOOL_DENIED=") {
			t.Errorf("env.pass passed a denied variable: %q", env)
		}
		if !containsPrefix(env, "REF=") || containsPrefix(env, "REF=op-s3cret") {
			t.Errorf("${env.NAME} expanded a denied variable: %q", env)
		}
		if !containsPrefix(env, "OPENCODE_SERVER_PASSWORD=granted-literal") {
			t.Errorf("a literal env.set value must still be passed: %q", env)
		}
		if !containsPrefix(env, "CLITOOL_KEPT=kept") {
			t.Error("env.pass dropped an undenied variable")
		}
	})
}

func TestExec_TimeoutKillsProcessGroup(t *testing.T) {
	wd := t.TempDir()
	script := writeScript(t, wd, "slow.sh", `sleep 30 & wait`)
	m := mustParse(t, wd, "slow.yaml", "name: slow\ndescription: d\ncommand: "+script+"\ntimeout: 1s\nmaxTimeout: 2s\n")
	inv, err := m.Prepare(map[string]any{"args": []any{}, "timeout": float64(60)}) // clamped to maxTimeout
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res := m.Exec(context.Background(), inv)
	if !res.TimedOut || res.Timeout != 2*time.Second {
		t.Errorf("expected a 2s timeout, got timedOut=%v timeout=%v exit=%d", res.TimedOut, res.Timeout, res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("process group was not killed promptly: %v", elapsed)
	}
	text, isErr := FormatResult(res)
	if !isErr || !strings.Contains(text, "timed out after 2s") {
		t.Errorf("formatted = %q", text)
	}
}

func TestExec_MissingBinary(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "ghost.yaml", "name: ghost\ndescription: d\ncommand: definitely-not-installed-xyz\n")
	inv, _ := m.Prepare(map[string]any{"args": []any{"--help"}})
	res := m.Exec(context.Background(), inv)
	text, isErr := FormatResult(res)
	if !isErr || res.StartErr == nil || !strings.Contains(text, "not found") {
		t.Errorf("missing binary should be a clear error: %q", text)
	}
}

func TestFormatResult_NoOutput(t *testing.T) {
	text, isErr := FormatResult(ExecResult{ExitCode: 0})
	if isErr || text != "exit status 0 (no output)" {
		t.Errorf("got %q / %v", text, isErr)
	}
}

func containsPrefix(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
