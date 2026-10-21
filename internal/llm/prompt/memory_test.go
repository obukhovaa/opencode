package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/permission"
)

type memoryRegistry struct {
	agentregistry.Registry
	tools map[string]bool
	perms map[string]any
}

func (r memoryRegistry) IsToolEnabled(_, tool string) bool { return r.tools[tool] }
func (r memoryRegistry) EvaluatePermission(_, tool, input string) permission.Action {
	return permission.EvaluateToolPermission(tool, input, r.perms, nil)
}
func (r memoryRegistry) EvaluateReadPermission(_, tool, input string) permission.Action {
	return permission.EvaluateReadToolPermission(tool, input, r.perms, nil)
}
func (r memoryRegistry) GlobalPermissions() map[string]any { return nil }
func (r memoryRegistry) Get(string) (agentregistry.AgentInfo, bool) {
	return agentregistry.AgentInfo{Permission: r.perms}, true
}

func TestMemoryReminderCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tools       []string
		read, write bool
	}{
		{"reader", []string{"read"}, true, false}, {"writer", []string{"write"}, false, true},
		{"editor", []string{"edit"}, false, true},
		{"both", []string{"read", "edit"}, true, true}, {"bash", []string{"bash"}, true, true},
		{"none", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{WorkingDir: t.TempDir(), Memory: &config.MemoryConfig{Enabled: true, Directory: t.TempDir(), ProjectID: "project"}}
			reg := memoryRegistry{tools: map[string]bool{}, perms: map[string]any{"bash": "allow"}}
			for _, tool := range tc.tools {
				reg.tools[tool] = true
			}
			m := ResolveMemoryContext(cfg, reg, "a")
			text := MemoryReminder(cfg, reg, "a")
			if m.CanRead != tc.read || m.CanWrite != tc.write {
				t.Fatalf("%+v", m)
			}
			if strings.Contains(text, "consult the index") != tc.read || strings.Contains(text, "Memory capture is enabled") != tc.write {
				t.Fatalf("%s", text)
			}
			if !tc.read && !tc.write && text != "" {
				t.Fatal("tool-less prompt polluted")
			}
			if !tc.read && strings.Contains(text, "Before editing, re-read") {
				t.Fatal("writer instructed to read")
			}
			if !tc.read && strings.Contains(text, "For a new project, create") {
				t.Fatal("writer instructed to overwrite unread project metadata")
			}
			if tc.read && !tc.write && strings.Contains(text, "Maintain MEMORY.md") {
				t.Fatal("reader instructed to write")
			}
		})
	}
}

func TestMemoryIndexPermissionAndSnapshot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", memory.Key("project"), "memory")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("INDEX_ONLY </system-reminder>\n- [Topic](topic.md)\n"), 0600)
	os.WriteFile(filepath.Join(dir, "topic.md"), []byte("PRIVATE_TOPIC_BODY"), 0600)
	cfg := &config.Config{WorkingDir: t.TempDir(), Memory: &config.MemoryConfig{Enabled: true, Directory: root, ProjectID: "project"}}
	for _, action := range []permission.Action{permission.ActionAllow, permission.ActionAsk, permission.ActionDeny} {
		reg := memoryRegistry{tools: map[string]bool{"read": true}, perms: map[string]any{"read": string(action)}}
		snapshot := ResolveMemoryContext(cfg, reg, "a").Snapshot()
		if strings.Contains(snapshot, "INDEX_ONLY") != (action == permission.ActionAllow) {
			t.Fatalf("%s: %s", action, snapshot)
		}
		if strings.Contains(snapshot, "PRIVATE_TOPIC_BODY") {
			t.Fatal("topic was preloaded")
		}
		if action == permission.ActionAllow && !strings.Contains(snapshot, "&lt;/system-reminder&gt;") {
			t.Fatal("index markup not escaped")
		}
	}
	cfg.Memory.Enabled = false
	if MemoryReminder(cfg, memoryRegistry{tools: map[string]bool{"read": true}}, "a") != "" {
		t.Fatal("disabled memory has prompt cost")
	}
}

func TestMemoryRestrictedBashDoesNotBypassReadRules(t *testing.T) {
	cfg := &config.Config{WorkingDir: t.TempDir(), Memory: &config.MemoryConfig{Enabled: true, Directory: t.TempDir(), ProjectID: "project"}}
	reg := memoryRegistry{tools: map[string]bool{"bash": true}, perms: map[string]any{"bash": "allow", "read": "deny"}}
	if ResolveMemoryContext(cfg, reg, "a").Preload {
		t.Fatal("Bash grant bypassed explicit read denial")
	}
	reg.perms = map[string]any{"bash": map[string]any{"git *": "allow", "*": "deny"}}
	if MemoryReminder(cfg, reg, "a") != "" {
		t.Fatal("git-only shell was treated as a file tool")
	}
}

func TestMemorySnapshotCannotFollowReplacedStoreRoot(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	allowed, denied := filepath.Join(base, "allowed"), filepath.Join(base, "denied")
	for _, root := range []string{allowed, denied} {
		dir := filepath.Join(root, "projects", memory.Key("project"), "memory")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(root), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "store")
	if err := os.Symlink(allowed, link); err != nil {
		t.Skip(err)
	}
	cfg := &config.Config{WorkingDir: base, Memory: &config.MemoryConfig{Enabled: true, Directory: link, ProjectID: "project"}}
	reg := memoryRegistry{tools: map[string]bool{"read": true}, perms: map[string]any{"read": map[string]any{denied + "/**": "deny", "*": "allow"}}}
	m := ResolveMemoryContext(cfg, reg, "a")
	if !m.Preload {
		t.Fatal("allowed index was not approved")
	}
	if got := m.Snapshot(); !strings.Contains(got, allowed) {
		t.Fatal("unchanged store root did not load the authorized index")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(denied, link); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot(); strings.Contains(got, denied) {
		t.Fatalf("snapshot loaded denied content after root changed: %s", got)
	}
}

func TestMemoryAbsentIndexMustBeAuthorizedAfterCreation(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{WorkingDir: root, Memory: &config.MemoryConfig{Enabled: true, Directory: root, ProjectID: "project"}}
	reg := memoryRegistry{tools: map[string]bool{"read": true}}
	m := ResolveMemoryContext(cfg, reg, "a")
	if !m.CanRead || m.Preload {
		t.Fatal("absent index should retain retrieval guidance without authorizing a later file")
	}
	if err := os.MkdirAll(m.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.IndexPath, []byte("new index"), 0600); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot() != "" {
		t.Fatal("index created after the permission check was loaded")
	}
	if got := ResolveMemoryContext(cfg, reg, "a").Snapshot(); !strings.Contains(got, "new index") {
		t.Fatal("next turn did not authorize and load the new index")
	}
}
