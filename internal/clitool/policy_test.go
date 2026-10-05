package clitool

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, wd, file, body string) *Manifest {
	t.Helper()
	m, err := Parse([]byte(body), filepath.Join(wd, file), wd)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return m
}

func TestCheckArgs_DenyAnywhereAllowPrefix(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "snow.yaml", `
name: snow
description: d
command: /bin/echo
args:
  allow: ["sql *", "--help"]
  deny: ["-x", "--filename*", "* -c * -c *"]
`)
	cases := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"sql", "-q", "select 1"}, ""},
		{[]string{"--help"}, ""},
		{[]string{"sql", "-q", "select 1", "-x"}, `argument "-x" matches deny pattern "-x"`},
		{[]string{"sql", "--filename=x.sql"}, `"--filename=x.sql" matches deny pattern "--filename*"`},
		{[]string{"sql", "-c", "dcs", "-q", "x", "-c", "logging"}, `match deny pattern "* -c * -c *"`},
		{[]string{"connection", "list"}, "match none of the allow patterns"},
		{[]string{"sql", "a\x00b"}, "NUL"},
		// Shell operators are plain bytes: the policy sees one argument.
		{[]string{"sql", "-q", "select 1; -x"}, ""},
		// Case-insensitive: a CLI's own spelling tolerance must not open a path.
		{[]string{"SQL", "-X"}, `argument "-X" matches deny pattern "-x"`},
		{[]string{"Sql", "-q", "!SOURCE /etc/passwd"}, ""},
	}
	for _, tc := range cases {
		err := m.CheckArgs(tc.args)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%v: unexpected rejection %v", tc.args, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%v: got %v, want error containing %q", tc.args, err, tc.wantErr)
		}
		var pe *PolicyError
		if err != nil && !errors.As(err, &pe) {
			t.Errorf("%v: error should be a *PolicyError", tc.args)
		}
	}
}

func TestCheckArgs_ContentDenyIsCaseInsensitive(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "snow.yaml", "name: snow\ndescription: d\ncommand: /bin/echo\nargs:\n  deny: [\"*!source*\", \"-x*\"]\n")
	for _, args := range [][]string{{"sql", "-q", "!SOURCE /x"}, {"sql", "-q", "select 1; !Source a.sql"}, {"sql", "-Xh"}, {"sql", "-xh"}} {
		if err := m.CheckArgs(args); err == nil {
			t.Errorf("%v: expected rejection", args)
		}
	}
	if err := m.CheckArgs([]string{"sql", "-q", "select 'resource'"}); err != nil {
		t.Errorf("'resource' must not trip *!source*: %v", err)
	}
}

func TestCheckArgs_NoPolicyAllowsEverything(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "say.yaml", echoManifest)
	if err := m.CheckArgs([]string{"anything", "goes", "--here"}); err != nil {
		t.Errorf("empty policy must not reject: %v", err)
	}
}

func TestPrepare_ArgvMode(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "say.yaml", echoManifest+"stdin: true\n")
	inv, err := m.Prepare(map[string]any{"args": []any{"a", "b c"}, "stdin": "in", "timeout": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Args) != 2 || inv.Args[1] != "b c" || inv.Stdin != "in" || inv.Timeout != 5 {
		t.Errorf("invocation = %+v", inv)
	}
	for name, input := range map[string]map[string]any{
		"missing args":  {},
		"args not list": {"args": "x"},
		"non-string":    {"args": []any{1}},
		"unknown param": {"args": []any{}, "cmd": "x"},
		"bad timeout":   {"args": []any{}, "timeout": -1.0},
	} {
		if _, err := m.Prepare(input); err == nil {
			t.Errorf("%s: expected an input error", name)
		} else {
			var ie *InputError
			if !errors.As(err, &ie) {
				t.Errorf("%s: want *InputError, got %T", name, err)
			}
		}
	}
	noStdin := mustParse(t, wd, "say.yaml", echoManifest)
	if _, err := noStdin.Prepare(map[string]any{"args": []any{}, "stdin": "x"}); err == nil {
		t.Error("stdin must be rejected when the manifest does not enable it")
	}
}
