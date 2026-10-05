package clitool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest writes body to <dir>/<file> and returns its path.
func writeManifest(t *testing.T, dir, file, body string) string {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const echoManifest = `
name: say
description: Echo wrapper for tests.
command: /bin/echo
`

func TestParse_DefaultsAndResolution(t *testing.T) {
	wd := t.TempDir()
	m, err := Parse([]byte(echoManifest), filepath.Join(wd, "say.yaml"), wd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Mode != ModeArgv || m.Grant != GrantExplicit {
		t.Errorf("defaults: mode=%q grant=%q", m.Mode, m.Grant)
	}
	if m.TimeoutDefault() != DefaultTimeout || m.TimeoutMax() != DefaultMaxTimeout || m.MaxOutput() != DefaultMaxOutputBytes {
		t.Errorf("duration/cap defaults wrong: %v %v %d", m.TimeoutDefault(), m.TimeoutMax(), m.MaxOutput())
	}
	if !m.EnvInherit() {
		t.Error("env.inherit should default to true")
	}
	if !m.CommandFound() || m.ResolvedCommand != "/bin/echo" {
		t.Errorf("command not resolved: %q", m.ResolvedCommand)
	}
	resolvedWD, _ := filepath.EvalSymlinks(wd)
	if m.ResolvedCwd != resolvedWD {
		t.Errorf("cwd = %q, want working dir %q", m.ResolvedCwd, resolvedWD)
	}
	props, req := m.InputSchema()
	if _, ok := props["args"]; !ok || len(req) != 1 || req[0] != "args" {
		t.Errorf("argv schema wrong: %v %v", props, req)
	}
	if _, ok := props["stdin"]; ok {
		t.Error("stdin exposed although manifest did not enable it")
	}
	if !strings.Contains(m.FullDescription(), "no shell") {
		t.Error("description should state the no-shell contract")
	}
}

func TestParse_Rejections(t *testing.T) {
	wd := t.TempDir()
	cases := []struct {
		name, file, body, want string
	}{
		{"unknown field", "say.yaml", echoManifest + "args:\n  alow: [\"x\"]\n", "field alow not found"},
		{"reserved name", "bash.yaml", "name: bash\ndescription: d\ncommand: /bin/echo\n", "reserved"},
		{"basename mismatch", "snowflake.yaml", "name: snow\ndescription: d\ncommand: /bin/echo\n", `basename "snowflake" must equal name "snow"`},
		{"bad name", "Say.yaml", "name: Say\ndescription: d\ncommand: /bin/echo\n", "must match"},
		{"missing command", "say.yaml", "name: say\ndescription: d\n", "command is required"},
		{"bad mode", "say.yaml", echoManifest + "mode: shell\n", `mode "shell"`},
		{"bad grant", "say.yaml", echoManifest + "grant: always\n", `grant "always"`},
		{"empty deny pattern", "say.yaml", echoManifest + "args:\n  deny: [\"\"]\n", "empty pattern"},
		{"max below timeout", "say.yaml", echoManifest + "timeout: 5m\nmaxTimeout: 1m\n", "maxTimeout"},
		{"bad duration", "say.yaml", echoManifest + "timeout: soon\n", "not a duration"},
		{"cwd escapes", "say.yaml", echoManifest + "cwd: ../..\n", "outside the working directory"},
		{"cwd missing", "say.yaml", echoManifest + "cwd: nope\n", "not an existing directory"},
		{"permission shape", "say.yaml", echoManifest + "permission: 42\n", "action string or a pattern map"},
		{"permission action", "say.yaml", echoManifest + "permission:\n  \"*\": maybe\n", "allow, ask or deny"},
		{"structured without argv", "say.yaml", echoManifest + "mode: structured\nparameters:\n  q: {type: string}\n", "needs an argv template"},
		{"structured undeclared ref", "say.yaml", echoManifest + "mode: structured\nparameters:\n  q: {type: string}\nargv: [\"{q}\", \"{x}\"]\n", "undeclared parameter {x}"},
		{"structured bad type", "say.yaml", echoManifest + "mode: structured\nparameters:\n  q: {type: object}\nargv: [\"{q}\"]\n", "unsupported type"},
		{"argv-mode with parameters", "say.yaml", echoManifest + "parameters:\n  q: {type: string}\n", "only valid with mode: structured"},
		{"boolean outside group", "say.yaml", echoManifest + "mode: structured\nparameters:\n  v: {type: boolean}\nargv: [\"{v}\"]\n", "inside an optional group"},
		{"group without refs", "say.yaml", echoManifest + "mode: structured\nparameters:\n  q: {type: string}\nargv: [\"{q}\", [\"--x\"]]\n", "references no parameter"},
		{"help without args", "say.yaml", echoManifest + "help: {}\n", "help.args"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body), filepath.Join(wd, tc.file), wd)
			if err == nil {
				t.Fatalf("expected rejection containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParse_PermissionNormalization(t *testing.T) {
	wd := t.TempDir()
	m, err := Parse([]byte(echoManifest+"permission: Allow\n"), filepath.Join(wd, "say.yaml"), wd)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.PermissionDefault()["*"]; got != "allow" {
		t.Errorf("string permission should normalize to {\"*\": allow}, got %v", m.PermissionDefault())
	}
	m, err = Parse([]byte(echoManifest+"permission:\n  \"*\": ask\n  \"sql *\": ALLOW\n"), filepath.Join(wd, "say.yaml"), wd)
	if err != nil {
		t.Fatal(err)
	}
	if m.PermissionDefault()["sql *"] != "allow" || m.PermissionDefault()["*"] != "ask" {
		t.Errorf("map permission not normalized: %v", m.PermissionDefault())
	}
}

func TestParse_JSONManifestAndRelativeCommand(t *testing.T) {
	wd := t.TempDir()
	script := filepath.Join(wd, "bin", "hello")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"hello","description":"json manifest","command":"./bin/hello","maxOutputBytes":-1,"timeout":"90","stdin":true}`
	m, err := Parse([]byte(body), filepath.Join(wd, "hello.json"), wd)
	if err != nil {
		t.Fatalf("json manifest: %v", err)
	}
	if m.ResolvedCommand != script {
		t.Errorf("relative command resolved to %q, want %q", m.ResolvedCommand, script)
	}
	if m.MaxOutput() != -1 {
		t.Errorf("negative maxOutputBytes should disable the cap, got %d", m.MaxOutput())
	}
	if m.TimeoutDefault().Seconds() != 90 {
		t.Errorf("bare number timeout should be seconds, got %v", m.TimeoutDefault())
	}
	props, _ := m.InputSchema()
	if _, ok := props["stdin"]; !ok {
		t.Error("stdin: true should expose the stdin parameter")
	}
}

func TestParse_MissingBinaryStillLoads(t *testing.T) {
	wd := t.TempDir()
	m, err := Parse([]byte("name: ghost\ndescription: d\ncommand: definitely-not-installed-xyz\n"), filepath.Join(wd, "ghost.yaml"), wd)
	if err != nil {
		t.Fatalf("a missing binary must not invalidate the manifest: %v", err)
	}
	if m.CommandFound() {
		t.Error("binary should be reported as not found")
	}
}

func TestReservedNamesCoverEngineTools(t *testing.T) {
	for _, n := range []string{"bash", "toolsearch", "struct_output", "task", "skill"} {
		if !ReservedNames[n] {
			t.Errorf("%q must be reserved", n)
		}
	}
}
