//go:build !windows

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/opencode-ai/opencode/internal/config"
)

// mcpHelperEnv switches the test binary into a stdio MCP server; its value
// picks the behavior after stdin closes ("exit" or "ignore-eof").
const mcpHelperEnv = "OPENCODE_MCP_STDIO_HELPER"

// TestMCPStdioHelperProcess is not a test. The stdio pool tests re-execute
// the test binary with mcpHelperEnv set, and it serves:
//   - pid:  the server's process id
//   - exit: replies, then exits
//   - spam: writes 256 KiB to stderr, then replies
func TestMCPStdioHelperProcess(t *testing.T) {
	mode := os.Getenv(mcpHelperEnv)
	if mode == "" {
		t.Skip("helper process for the stdio MCP pool tests")
	}
	srv := server.NewMCPServer("stdio-helper", "0.0.1")
	srv.AddTool(mcp.NewTool("pid"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult(strconv.Itoa(os.Getpid())), nil
	})
	srv.AddTool(mcp.NewTool("exit"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}()
		return textResult("bye"), nil
	})
	srv.AddTool(mcp.NewTool("spam"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		line := strings.Repeat("x", 1023) + "\n"
		for range 256 {
			_, _ = os.Stderr.WriteString(line)
		}
		return textResult("ok"), nil
	})
	_ = server.ServeStdio(srv)
	if mode == "ignore-eof" {
		// A server that outlives its stdin and shrugs off SIGTERM: only
		// SIGKILL ends it — or its parent dying, so a crashed test run
		// cannot orphan it for good.
		signal.Ignore(syscall.SIGTERM)
		parent := os.Getppid()
		for os.Getppid() == parent {
			time.Sleep(100 * time.Millisecond)
		}
	}
	os.Exit(0)
}

func stdioHelperServer(mode string) config.MCPServer {
	return config.MCPServer{
		Type:    config.MCPStdio,
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Env:     []string{mcpHelperEnv + "=" + mode},
	}
}

func newStdioPoolRegistry(t *testing.T, m config.MCPServer) *mcpRegistry {
	t.Helper()
	seedMCPServers(t, map[string]config.MCPServer{"helper": m})
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reg.Shutdown(ctx)
	})
	return reg
}

func callPID(t *testing.T, reg *mcpRegistry) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := reg.CallTool(ctx, "helper", "pid", "{}")
	if resp.IsError {
		t.Fatalf("pid call failed: %s", resp.Content)
	}
	pid, err := strconv.Atoi(resp.Content)
	if err != nil {
		t.Fatalf("pid call returned %q", resp.Content)
	}
	return pid
}

func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func withCloseGrace(t *testing.T, d time.Duration) {
	t.Helper()
	prev := mcpCloseGrace
	mcpCloseGrace = d
	t.Cleanup(func() { mcpCloseGrace = prev })
}

func TestMCPStdio_OneProcessAcrossCalls(t *testing.T) {
	reg := newStdioPoolRegistry(t, stdioHelperServer("exit"))

	first := callPID(t, reg)
	for range 3 {
		if pid := callPID(t, reg); pid != first {
			t.Fatalf("call ran in process %d, want the pooled %d", pid, first)
		}
	}
	if first == os.Getpid() {
		t.Fatal("the helper ran in the test process itself")
	}
}

func TestMCPStdio_ReplacedAfterTheServerExits(t *testing.T) {
	reg := newStdioPoolRegistry(t, stdioHelperServer("exit"))

	first := callPID(t, reg)
	if resp := reg.CallTool(context.Background(), "helper", "exit", "{}"); resp.IsError || resp.Content != "bye" {
		t.Fatalf("exit call: %+v", resp)
	}
	eventually(t, "the helper to exit", func() bool {
		// Reaped or a zombie: either way its stdin has no reader any more.
		return processGone(first) || !processRunning(first)
	})
	if pid := callPID(t, reg); pid == first {
		t.Fatalf("call ran in the exited process %d", pid)
	}
}

func TestMCPStdio_ServerIgnoringEOFIsKilledOnClose(t *testing.T) {
	withCloseGrace(t, 100*time.Millisecond)
	reg := newStdioPoolRegistry(t, stdioHelperServer("ignore-eof"))
	pid := callPID(t, reg)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reg.Shutdown(ctx)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("shutdown took %s; the escalation did not apply", elapsed)
	}
	eventually(t, "the helper to be killed and reaped", func() bool { return processGone(pid) })
}

// Each call gets its own process, and each is stopped afterwards. That the
// result does not wait for the close is pinned by TestMCPPool_ReuseDisabled,
// without a process spawn's timing in the way.
func TestMCPStdio_ReuseDisabledStopsEachProcess(t *testing.T) {
	withCloseGrace(t, 100*time.Millisecond)
	m := stdioHelperServer("ignore-eof")
	m.ClientIdleTimeoutSeconds = -1
	reg := newStdioPoolRegistry(t, m)

	first := callPID(t, reg)
	second := callPID(t, reg)
	if first == second {
		t.Error("reuse is disabled, but both calls ran in one process")
	}
	eventually(t, "the first helper to be killed", func() bool { return processGone(first) })
	eventually(t, "the second helper to be killed", func() bool { return processGone(second) })
}

// ForceShutdown gives the pool 1s, less than the default grace: the
// escalation must still fit into it rather than leave the server behind.
func TestMCPStdio_ShortShutdownBudgetStillKills(t *testing.T) {
	reg := newStdioPoolRegistry(t, stdioHelperServer("ignore-eof"))
	pid := callPID(t, reg)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reg.Shutdown(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s against a 1s budget", elapsed)
	}
	eventually(t, "the helper to be killed within the budget", func() bool { return processGone(pid) })
}

func TestMCPStdio_DrainsStderr(t *testing.T) {
	reg := newStdioPoolRegistry(t, stdioHelperServer("exit"))

	// 3 x 256 KiB is far past a pipe buffer: without the drain the second
	// call blocks in the server's stderr write until the call times out.
	for i := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		resp := reg.CallTool(ctx, "helper", "spam", "{}")
		cancel()
		if resp.IsError || resp.Content != "ok" {
			t.Fatalf("spam call %d: %+v", i, resp)
		}
	}
}

// processRunning reports whether pid is alive and not a zombie.
func processRunning(pid int) bool {
	if processGone(pid) {
		return false
	}
	if out, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		fields := strings.Fields(string(out))
		return len(fields) < 3 || fields[2] != "Z"
	}
	// No procfs (macOS): ask ps for the process state.
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}
