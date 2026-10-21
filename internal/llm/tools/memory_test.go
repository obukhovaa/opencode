package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
)

type memoryPermissions struct {
	agentregistry.Registry
	denied string
}

func (r memoryPermissions) EvaluatePermission(_, _, input string) permission.Action {
	if input == r.denied {
		return permission.ActionDeny
	}
	return permission.ActionAllow
}

func TestMemoryPermissionBeforeMutation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "absent")
	cfg := &config.Config{Memory: &config.MemoryConfig{Directory: directory}}
	tool := NewMemoryTool(cfg, memoryPermissions{denied: "create /memories/note.md"}, nil)
	_, err := tool.Run(context.Background(), ToolCall{Input: `{"command":"create","path":"/memories/note.md","file_text":"note"}`})
	if err == nil {
		t.Fatal("write was allowed")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("denied request created store")
	}
}

func TestMemoryRenameChecksDestinationPermission(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "from.md"), []byte("note"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewMemoryTool(&config.Config{Memory: &config.MemoryConfig{Directory: directory}}, memoryPermissions{denied: "rename /memories/to.md"}, nil)
	_, err := tool.Run(context.Background(), ToolCall{Input: `{"command":"rename","old_path":"/memories/from.md","new_path":"/memories/to.md"}`})
	if err == nil {
		t.Fatal("destination denial ignored")
	}
	if _, err := os.Stat(filepath.Join(directory, "from.md")); err != nil {
		t.Fatal(err)
	}
}
