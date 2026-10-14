package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/opencode-ai/opencode/internal/config"
)

// newHangingHTTPMCPServer serves "echo" and a "hang" tool that never answers
// (it ignores cancellation, like a server stuck on its own upstream).
func newHangingHTTPMCPServer(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	mcpSrv := server.NewMCPServer("hang-test", "0.0.1")
	mcpSrv.AddTool(mcp.NewTool("echo"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	mcpSrv.AddTool(mcp.NewTool("hang"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-release
		return textResult("late"), nil
	})
	h := server.NewStreamableHTTPServer(mcpSrv)
	ts := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	t.Cleanup(func() {
		close(release)
		ts.Close()
	})
	return ts.URL
}

func callEchoWithin(t *testing.T, reg *mcpRegistry, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	start := time.Now()
	resp := reg.CallTool(ctx, "web", "echo", "{}")
	if resp.IsError || resp.Content != "ok" {
		t.Fatalf("echo after %s: %+v — later calls are blocked behind the hung one", time.Since(start), resp)
	}
}

// The HTTP half of the CD-5305 freeze: two sessions in one process share the
// pooled streamable-HTTP client of a server, and one of them is stuck in a call
// the server never answers. The other session's calls must not queue behind
// it — neither while it hangs, nor after the stuck run was cancelled (/stop),
// nor after the stuck call ran out its own per-call timeout.
func TestMCPHTTP_HungCallDoesNotBlockOtherSessions(t *testing.T) {
	url := newHangingHTTPMCPServer(t)
	reg := newHTTPPoolRegistry(t, "web", url)
	srv := config.Get().MCPServers["web"]
	srv.CallToolTimeoutSeconds = 2
	config.Get().MCPServers["web"] = srv

	callEchoWithin(t, reg, 5*time.Second)

	runA, stopA := context.WithCancel(context.Background())
	hung := make(chan struct{})
	go func() {
		defer close(hung)
		reg.CallTool(runA, "web", "hang", "{}")
	}()
	time.Sleep(200 * time.Millisecond)

	// Session B while A hangs.
	callEchoWithin(t, reg, 5*time.Second)

	// /stop cancels run A mid-call.
	stopA()
	select {
	case <-hung:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling run A did not end its hung MCP call")
	}
	callEchoWithin(t, reg, 5*time.Second)

	// A hung call that runs out its own per-call timeout, caller still alive.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if resp := reg.CallTool(ctx, "web", "hang", "{}"); !resp.IsError {
		t.Fatalf("hang call: %+v, want a timeout error", resp)
	}
	callEchoWithin(t, reg, 5*time.Second)
}
