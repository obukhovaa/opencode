package agent

import (
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestResolvedToolsDoesNotWaitForDiscovery(t *testing.T) {
	stream := make(chan tools.BaseTool)
	a := &agent{toolsCh: stream}
	resolved := make(chan struct{})
	go func() { a.resolveTools(); close(resolved) }()
	// The send only completes once the resolver holds its writer lock and is
	// draining the stream. Leave discovery open to model a slow MCP server.
	stream <- newMock("pending-server-tool", false)
	type snapshot struct {
		tools []tools.BaseTool
		ready bool
	}
	got := make(chan snapshot, 1)
	go func() { ts, ready := a.ResolvedTools(); got <- snapshot{ts, ready} }()
	select {
	case result := <-got:
		if result.ready || result.tools != nil {
			t.Errorf("pending discovery returned %+v", result)
		}
	case <-time.After(time.Second):
		// Always release the resolver so a regression fails without leaking a
		// goroutine or wedging the rest of the test process.
		close(stream)
		<-resolved
		t.Fatal("ResolvedTools blocked on unfinished discovery")
	}
	close(stream)
	select {
	case <-resolved:
	case <-time.After(time.Second):
		t.Fatal("resolver did not finish")
	}
	ts, ready := a.ResolvedTools()
	if !ready || len(ts) != 1 || ts[0].Info().Name != "pending-server-tool" {
		t.Fatalf("resolved snapshot tools=%v ready=%v", ts, ready)
	}
}

func TestResolvedToolsDoesNotWaitForLiveReplacement(t *testing.T) {
	a := &agent{tools: []tools.BaseTool{newMock("old", false)}}
	a.toolsResolved.Store(true)
	a.toolsMu.Lock()
	result := make(chan bool, 1)
	go func() { _, ready := a.ResolvedTools(); result <- ready }()
	select {
	case ready := <-result:
		a.toolsMu.Unlock()
		if ready {
			t.Fatal("toolset reported ready while replacement owns lock")
		}
	case <-time.After(time.Second):
		a.toolsMu.Unlock()
		t.Fatal("ResolvedTools blocked during replacement")
	}
}
