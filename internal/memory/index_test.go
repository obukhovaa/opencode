package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMemoryIndexBounds(t *testing.T) {
	for _, text := range []string{strings.Repeat("line\n", 500), strings.Repeat("界", 20000), strings.Repeat("\xff", 40000)} {
		got := BoundIndex([]byte(text))
		if !got.Truncated || len(got.Text) > IndexMaxBytes || !utf8.ValidString(got.Text) || strings.Count(strings.TrimSuffix(got.Text, "\n"), "\n") >= IndexMaxLines {
			t.Fatalf("invalid bound: %d bytes", len(got.Text))
		}
	}
	exact := strings.Repeat("x\n", 200)
	if got := BoundIndex([]byte(exact)); got.Truncated || got.Text != exact {
		t.Fatal("exact line limit changed")
	}
}

func TestMemoryIndexRejectsSymlinkAndDoesNotReadTopics(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "key", "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- [Topic](topic.md)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "topic.md"), []byte("TOPIC_SECRET_BODY"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := ReadIndex(root, "projects/key/memory/MEMORY.md", canonical)
	if err != nil || strings.Contains(index.Text, "TOPIC_SECRET_BODY") {
		t.Fatalf("%+v %v", index, err)
	}
	if err := os.Remove(filepath.Join(dir, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "topic.md"), filepath.Join(dir, "MEMORY.md")); err != nil {
		t.Skip(err)
	}
	if _, err := ReadIndex(root, "projects/key/memory/MEMORY.md", canonical); err == nil {
		t.Fatal("symlink loaded")
	}
}

func TestMemoryAuthoringFeedback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", Key("project"), "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text string
		bad        bool
	}{
		{"valid.md", "---\ndescription: Find the deploy command\nupdatedAt: 2026-01-02T03:04:05Z\n---\n- Run the documented check.\n", false},
		{"legacy.md", "Long prose without metadata", true},
		{"large.md", strings.Repeat("x", NoteMaxBytes+1), true},
		{"MEMORY.md", strings.Repeat("- [Topic](topic.md)\n", 201), true},
	} {
		file := filepath.Join(dir, tc.name)
		if err := os.WriteFile(file, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		if got := CheckFile(root, file); (got != "") != tc.bad {
			t.Errorf("%s: %q", tc.name, got)
		}
		data, _ := os.ReadFile(file)
		if string(data) != tc.text {
			t.Fatal("feedback changed saved content")
		}
	}
}

func TestMemoryCanonicalIdentityAndAliases(t *testing.T) {
	for _, remote := range []string{"git@GitLab.com:piano/project.git", "https://user:secret@gitlab.com/piano/project.git", "ssh://git@gitlab.com:22/piano/project/"} {
		id, err := CanonicalRemote(remote)
		if err != nil || id != "git:gitlab.com/piano/project" {
			t.Fatalf("%q %v", id, err)
		}
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "aliases.json"), []byte(`{"mirror":"canonical"}`), 0600)
	id, err := ProjectID(root, root, "mirror")
	if err != nil || id != "canonical" {
		t.Fatalf("%s %v", id, err)
	}
}

func TestMemoryCanonicalIdentityRetainsNonDefaultSchemePorts(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"https://host:22/repo", "git:host:22/repo"},
		{"ssh://host:443/repo", "git:host:443/repo"},
		{"http://host:9418/repo", "git:host:9418/repo"},
		{"https://host:443/repo", "git:host/repo"},
		{"ssh://host:22/repo", "git:host/repo"},
		{"http://host:80/repo", "git:host/repo"},
		{"git://host:9418/repo", "git:host/repo"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			got, err := CanonicalRemote(tc.remote)
			if err != nil || got != tc.want {
				t.Errorf("CanonicalRemote(%q) = %q, %v; want %q", tc.remote, got, err, tc.want)
			}
		})
	}
}
