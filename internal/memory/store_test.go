package memory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/fileutil"
)

func invoke(t *testing.T, dir, input string) (Result, error) {
	t.Helper()
	var p Params
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		t.Fatal(err)
	}
	return Run(context.Background(), dir, p)
}

func TestCommandsPersistAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	for _, input := range []string{
		`{"command":"view","path":"/memories"}`,
		`{"command":"create","path":"/memories/projects/demo/memory/MEMORY.md","file_text":"one\ntwo\n"}`,
		`{"command":"str_replace","path":"/memories/projects/demo/memory/MEMORY.md","old_str":"one","new_str":"first"}`,
		`{"command":"insert","path":"/memories/projects/demo/memory/MEMORY.md","insert_line":1,"insert_text":"middle\n"}`,
		`{"command":"rename","old_path":"/memories/projects/demo","new_path":"/memories/projects/renamed"}`,
	} {
		if _, err := invoke(t, dir, input); err != nil {
			t.Fatalf("%s: %v", input, err)
		}
	}
	r, err := invoke(t, dir, `{"command":"view","path":"/memories/projects/renamed/memory/MEMORY.md","view_range":[2,-1]}`)
	if err != nil || !strings.Contains(r.Text, "     2\tmiddle") || !strings.Contains(r.Text, "     3\ttwo") || strings.Contains(r.Text, "first") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := invoke(t, dir, `{"command":"delete","path":"/memories/projects/renamed"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "projects", "renamed")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestRejectsUnsafeAndAmbiguousOperations(t *testing.T) {
	dir := t.TempDir()
	_, err := invoke(t, dir, `{"command":"create","path":"/memories/entry.md","file_text":"repeat repeat\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"command":"create","path":"/memories/entry.md","file_text":"overwrite"}`,
		`{"command":"str_replace","path":"/memories/entry.md","old_str":"repeat","new_str":"oops"}`,
		`{"command":"str_replace","path":"/memories/entry.md","old_str":"absent"}`,
		`{"command":"insert","path":"/memories/entry.md","insert_line":4,"insert_text":"oops"}`,
		`{"command":"view","path":"/memories/../outside"}`,
		`{"command":"view","path":"/memories/.sync/lock"}`,
		`{"command":"view","path":"/memories\\outside"}`,
		`{"command":"delete","path":"/memories"}`,
		`{"command":"rename","old_path":"/memories","new_path":"/memories/old"}`,
		`{"command":"delete","path":"/memories/store.json"}`,
	} {
		if _, err := invoke(t, dir, input); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "entry.md"))
	if string(data) != "repeat repeat\n" {
		t.Fatalf("changed: %s", data)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := invoke(t, dir, `{"command":"view","path":"/memories/link/secret"}`); err == nil {
		t.Fatal("read through symlink")
	}
}

func TestViewsAndOptionalReplacement(t *testing.T) {
	dir := t.TempDir()
	text := "start\n" + strings.Repeat("long line\n", 2000)
	if err := os.WriteFile(filepath.Join(dir, "long.md"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := invoke(t, dir, `{"command":"view","path":"/memories/long.md"}`)
	if err != nil || !strings.Contains(r.Text, "truncated") || len(r.Text) > MaxViewChars+200 {
		t.Fatalf("size %d err %v", len(r.Text), err)
	}
	if _, err := invoke(t, dir, `{"command":"str_replace","path":"/memories/long.md","old_str":"start\n"}`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pixel.png"), []byte{137, 80, 78, 71}, 0600); err != nil {
		t.Fatal(err)
	}
	r, err = invoke(t, dir, `{"command":"view","path":"/memories/pixel.png"}`)
	if err != nil || r.MIME != "image/png" || len(r.Image) != 4 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestLockContentionAndCancellation(t *testing.T) {
	dir := t.TempDir()
	if _, err := invoke(t, dir, `{"command":"view","path":"/memories"}`); err != nil {
		t.Fatal(err)
	}
	lock, err := fileutil.Acquire(filepath.Join(dir, ".sync", "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := invoke(t, dir, `{"command":"create","path":"/memories/new.md","file_text":"new"}`); err == nil {
		t.Fatal("ignored lock")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, dir, Params{Command: "view", Path: "/memories"}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestCrossProcessMemory(t *testing.T) {
	if directory := os.Getenv("MEMORY_TEST_CHILD_DIRECTORY"); directory != "" {
		_, err := invoke(t, directory, `{"command":"create","path":"/memories/child.md","file_text":"from another process"}`)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessMemory$")
	cmd.Env = append(os.Environ(), "MEMORY_TEST_CHILD_DIRECTORY="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	r, err := invoke(t, dir, `{"command":"view","path":"/memories/child.md"}`)
	if err != nil || !strings.Contains(r.Text, "from another process") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestCanonicalIdentityAndAliases(t *testing.T) {
	for _, remote := range []string{"git@GitLab.com:piano/project.git", "https://user:secret@gitlab.com/piano/project.git", "ssh://git@gitlab.com:22/piano/project/"} {
		id, err := CanonicalRemote(remote)
		if err != nil || id != "git:gitlab.com/piano/project" {
			t.Fatalf("%s -> %s %v", remote, id, err)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aliases.json"), []byte(`{"mirror":"canonical"}`), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := ProjectID(dir, dir, "mirror")
	if err != nil || id != "canonical" {
		t.Fatalf("%s %v", id, err)
	}
}
