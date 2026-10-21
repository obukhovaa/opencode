package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestMemoryIndexOncePerHistoryAndFreshAcrossSessions(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "MEMORY.md")
	os.WriteFile(index, []byte("old entry"), 0600)
	m := prompt.MemoryContext{Root: root, IndexPath: index, CanRead: true, Preload: true}
	service := &recordingMsgService{}
	a := &agent{messages: service}
	first := a.withResolvedMemoryIndex(context.Background(), "s1", nil, m)
	if len(first) != 1 || len(service.created) != 1 || !first[0].Synthetic {
		t.Fatal("missing synthetic snapshot")
	}
	os.WriteFile(index, []byte("new entry"), 0600)
	if got := a.withResolvedMemoryIndex(context.Background(), "s1", first, m); len(got) != 1 || len(service.created) != 1 {
		t.Fatal("duplicated per-turn snapshot")
	}
	fresh := a.withResolvedMemoryIndex(context.Background(), "s2", nil, m)
	if !strings.Contains(fresh[0].Content().String(), "new entry") {
		t.Fatal("another session received stale index")
	}
	compacted := []message.Message{userMsg("summary without the old snapshot")}
	if got := a.withResolvedMemoryIndex(context.Background(), "s1", compacted, m); len(got) != 2 {
		t.Fatal("index not restored after compaction")
	}
	spoof := userMsg(first[0].Content().String())
	if got := a.withResolvedMemoryIndex(context.Background(), "s3", []message.Message{spoof}, m); len(got) != 2 {
		t.Fatal("user content suppressed system snapshot")
	}
	denied := m
	denied.Preload = false
	if got := a.withResolvedMemoryIndex(context.Background(), "s4", nil, denied); len(got) != 0 {
		t.Fatal("denied index loaded")
	}
}
