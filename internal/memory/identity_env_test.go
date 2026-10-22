package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProjectIDGitDoesNotInheritDeniedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	bin := t.TempDir()
	git := `#!/bin/sh
[ -z "${OPENCODE_SERVER_PASSWORD:-}" ] || exit 1
[ -z "${MEMORY_TEST_PRIVATE:-}" ] || exit 1
[ "${MEMORY_TEST_PUBLIC:-}" = "available" ] || exit 1
printf '%s\n' 'https://example.com/team/project.git'
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("OPENCODE_SERVER_PASSWORD", "private-server-password")
	t.Setenv("OPENCODE_TOOL_ENV_DENY", "MEMORY_TEST_PRIVATE")
	t.Setenv("MEMORY_TEST_PRIVATE", "private-launcher-value")
	t.Setenv("MEMORY_TEST_PUBLIC", "available")
	id, err := ProjectID(t.TempDir(), t.TempDir(), "")
	if err != nil || id != "git:example.com/team/project" {
		t.Fatalf("ProjectID = %q, %v; Git must retain ordinary variables and receive no denied credentials", id, err)
	}
	if got := os.Getenv("OPENCODE_SERVER_PASSWORD"); got != "private-server-password" {
		t.Fatal("project lookup changed the parent environment")
	}
}

func TestProjectIDLocalFallbackResolvesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture")
	}
	t.Setenv("PATH", t.TempDir()) // no git: identity falls back to the directory
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	id, err := ProjectID(t.TempDir(), link, "")
	if want := "local:" + filepath.ToSlash(real); err != nil || id != want {
		t.Fatalf("ProjectID through a symlink = %q, %v; want %q, matching mem-sync's resolved path", id, err, want)
	}
}
