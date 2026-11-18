package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
)

type mcpDiagnosticLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *mcpDiagnosticLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *mcpDiagnosticLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
func captureMCPDiagnostics(t *testing.T) *mcpDiagnosticLog {
	t.Helper()
	buffer := &mcpDiagnosticLog{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buffer
}
func diagnosticsConfig(t *testing.T, endpoints []string) {
	t.Helper()
	seedMCPServers(t, map[string]config.MCPServer{"srv": {Type: config.MCPHttp, URL: "http://unused"}})
	old := config.Get().MCPComposition
	if endpoints == nil {
		config.Get().MCPComposition = nil
	} else {
		config.Get().MCPComposition = &config.MCPCompositionConfig{ReadTools: endpoints}
	}
	t.Cleanup(func() { config.Get().MCPComposition = old })
}

func TestMCPPoolConnectionDiagnosticsSuppressCompositionPayloads(t *testing.T) {
	for _, mode := range []string{"configured", "hidden", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			var endpoints []string
			if mode == "configured" {
				endpoints = []string{"srv_read"}
			}
			diagnosticsConfig(t, endpoints)
			logs := captureMCPDiagnostics(t)
			pool := newMCPClientPool(context.Background())
			pool.newClient = func(ctx context.Context, _ string, _ config.MCPServer, _ map[string]string) (MCPClient, *mcpProc, error) {
				if mode != "ordinary" && !mcpDiagnosticsFromContext(ctx, "srv").restricted.Load() {
					t.Error("restriction lost on pool lifetime")
				}
				return nil, nil, errors.New("connection RAW_CONNECT_SENTINEL")
			}
			ctx := context.Background()
			if mode == "hidden" {
				ctx = context.WithValue(ctx, compositionChildKey{}, true)
			}
			_, err := pool.acquire(ctx, "srv", config.Get().MCPServers["srv"])
			if err == nil {
				t.Fatal("expected connect error")
			}
			pool.shutdown(context.Background())
			leaked := strings.Contains(logs.String(), "RAW_CONNECT_SENTINEL")
			if leaked != (mode == "ordinary") {
				t.Fatalf("diagnostic policy wrong: %s", logs.String())
			}
		})
	}
}

func TestMCPPoolDiagnosticRestrictionSticksToReusedClient(t *testing.T) {
	diagnosticsConfig(t, nil)
	pool := newMCPClientPool(context.Background())
	defer pool.shutdown(context.Background())
	pool.newClient = func(context.Context, string, config.MCPServer, map[string]string) (MCPClient, *mcpProc, error) {
		return &fakeMCPClient{}, nil, nil
	}
	server := config.Get().MCPServers["srv"]
	first, err := pool.acquire(context.Background(), "srv", server)
	if err != nil {
		t.Fatal(err)
	}
	if first.diagnostics.restricted.Load() {
		t.Fatal("ordinary client unexpectedly restricted")
	}
	pool.release(first, false)
	child := context.WithValue(context.Background(), compositionChildKey{}, true)
	second, err := pool.acquire(child, "srv", server)
	if err != nil {
		t.Fatal(err)
	}
	if second != first || !first.diagnostics.restricted.Load() {
		t.Fatal("shared client did not inherit restriction")
	}
	pool.release(second, false)
	third, err := pool.acquire(context.Background(), "srv", server)
	if err != nil {
		t.Fatal(err)
	}
	if third != first || !third.diagnostics.restricted.Load() {
		t.Fatal("restriction reset on ordinary reuse")
	}
	pool.release(third, false)
}

func TestMCPStderrDiagnosticPolicyContinuesDraining(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "composition"}[restricted], func(t *testing.T) {
			logs := captureMCPDiagnostics(t)
			policy := &mcpDiagnostics{}
			policy.restricted.Store(restricted)
			text := strings.Repeat("RAW_STDERR_SENTINEL\n", 20000)
			reader := strings.NewReader(text)
			drainMCPStderrReader(reader, "srv", policy)
			if reader.Len() != 0 {
				t.Fatal("stderr was not drained")
			}
			if strings.Contains(logs.String(), "RAW_STDERR_SENTINEL") == restricted {
				t.Fatalf("stderr policy wrong: restricted=%v", restricted)
			}
		})
	}
}

func TestMCPDiagnosticCloseErrorSuppressed(t *testing.T) {
	logs := captureMCPDiagnostics(t)
	policy := &mcpDiagnostics{}
	policy.restricted.Store(true)
	closeMCPClient(diagnosticCloseError{}, nil, "srv", mcpCloseTiming{abandon: time.Second}, policy)
	if strings.Contains(logs.String(), "RAW_CLOSE_SENTINEL") {
		t.Fatalf("close payload logged: %s", logs.String())
	}
}

type diagnosticCloseError struct{}

func (diagnosticCloseError) Close() error { return errors.New("RAW_CLOSE_SENTINEL") }
