package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func jobCtx(args map[string]any) context.Context {
	return tools.WithJobArgs(context.Background(), args)
}

// A mapped header carries the job arg: a string verbatim, a number as digits.
// Anything that is not a usable scalar omits it, and a static header of that
// name never stands in — whatever its letter case.
func TestResolveFlowArgHeaders(t *testing.T) {
	mapping := map[string]string{"X-Tenant": "tenant", "x-run": "run"}
	tests := []struct {
		name    string
		ctx     context.Context
		mapping map[string]string
		static  map[string]string
		want    map[string]string
	}{
		{name: "no mapping returns headers untouched", ctx: jobCtx(map[string]any{"tenant": "t1"}), static: map[string]string{"A": "b"}, want: map[string]string{"A": "b"}},
		{name: "string arg", ctx: jobCtx(map[string]any{"tenant": "acme"}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{"X-Tenant": "acme"}},
		{name: "float64 integer has no exponent", ctx: jobCtx(map[string]any{"run": float64(12345678901)}), mapping: map[string]string{"X-Run": "run"}, want: map[string]string{"X-Run": "12345678901"}},
		{name: "large float64 integer has no exponent", ctx: jobCtx(map[string]any{"run": float64(1e21)}), mapping: map[string]string{"X-Run": "run"}, want: map[string]string{"X-Run": "1000000000000000000000"}},
		{name: "fractional number", ctx: jobCtx(map[string]any{"run": 0.25}), mapping: map[string]string{"X-Run": "run"}, want: map[string]string{"X-Run": "0.25"}},
		{name: "json.Number", ctx: jobCtx(map[string]any{"run": json.Number("42")}), mapping: map[string]string{"X-Run": "run"}, want: map[string]string{"X-Run": "42"}},
		{name: "int", ctx: jobCtx(map[string]any{"run": 7}), mapping: map[string]string{"X-Run": "run"}, want: map[string]string{"X-Run": "7"}},
		{name: "absent arg", ctx: jobCtx(map[string]any{"other": "x"}), mapping: mapping, want: map[string]string{}},
		{name: "empty string", ctx: jobCtx(map[string]any{"tenant": ""}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "object", ctx: jobCtx(map[string]any{"tenant": map[string]any{"id": "x"}}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "array", ctx: jobCtx(map[string]any{"tenant": []any{"x"}}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "bool", ctx: jobCtx(map[string]any{"tenant": true}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "null", ctx: jobCtx(map[string]any{"tenant": nil}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "CRLF injection", ctx: jobCtx(map[string]any{"tenant": "a\r\nX-Evil: 1"}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "NUL", ctx: jobCtx(map[string]any{"tenant": "a\x00b"}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "DEL", ctx: jobCtx(map[string]any{"tenant": "a\x7fb"}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{}},
		{name: "tab is allowed", ctx: jobCtx(map[string]any{"tenant": "a\tb"}), mapping: map[string]string{"X-Tenant": "tenant"}, want: map[string]string{"X-Tenant": "a\tb"}},
		{
			name: "static header of the same name is replaced, any case",
			ctx:  jobCtx(map[string]any{"tenant": "acme"}), mapping: map[string]string{"X-Tenant": "tenant"},
			static: map[string]string{"x-tenant": "static", "X-TENANT": "static2", "X-Other": "o"},
			want:   map[string]string{"X-Tenant": "acme", "X-Other": "o"},
		},
		{
			name: "static header is stripped when the arg is absent",
			ctx:  jobCtx(map[string]any{}), mapping: map[string]string{"X-Tenant": "tenant"},
			static: map[string]string{"x-tenant": "static", "X-Other": "o"},
			want:   map[string]string{"X-Other": "o"},
		},
		{
			name: "static header is stripped when the value is unusable",
			ctx:  jobCtx(map[string]any{"tenant": map[string]any{}}), mapping: map[string]string{"X-Tenant": "tenant"},
			static: map[string]string{"X-Tenant": "static"},
			want:   map[string]string{},
		},
		{
			name: "a daemon turn without job args carries none and drops the static value",
			ctx:  context.Background(), mapping: map[string]string{"X-Tenant": "tenant"},
			static: map[string]string{"X-TENANT": "static", "X-Other": "o"},
			want:   map[string]string{"X-Other": "o"},
		},
		{name: "two headers", ctx: jobCtx(map[string]any{"tenant": "acme", "run": 3}), mapping: mapping, want: map[string]string{"X-Tenant": "acme", "x-run": "3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var static map[string]string
			if tt.static != nil {
				static = make(map[string]string, len(tt.static))
				for k, v := range tt.static {
					static[k] = v
				}
			}
			got := resolveFlowArgHeaders(tt.ctx, tt.mapping, static)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("headers = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(static, tt.static) {
				t.Errorf("the shared static map was modified: %v", static)
			}
		})
	}
}

// A warning names the header only: neither the arg's name nor its value is
// logged.
func TestResolveFlowArgHeadersWarningNamesTheHeaderOnly(t *testing.T) {
	var logs lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const secret = "tok-SECRET\r\nX-Evil: 1"
	resolveFlowArgHeaders(jobCtx(map[string]any{"credential_arg": secret}),
		map[string]string{"X-Cred": "credential_arg", "X-Missing": "missing_arg"}, nil)

	out := logs.String()
	if !strings.Contains(out, "X-Cred") || !strings.Contains(out, "X-Missing") {
		t.Fatalf("warning does not name the headers: %q", out)
	}
	for _, leaked := range []string{"SECRET", "credential_arg", "missing_arg"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaks %q: %q", leaked, out)
		}
	}
}

func seedFlowArgServer(t *testing.T, srv *mcpMethodServer, peerHeader string) *mcpRegistry {
	t.Helper()
	seedMCPServers(t, map[string]config.MCPServer{"web": {
		Type:           config.MCPHttp,
		URL:            srv.url,
		Headers:        map[string]string{"x-tenant": "static-tenant", "X-Static": "kept"},
		PeerHeader:     peerHeader,
		FlowArgHeaders: map[string]string{"X-Tenant": "tenant", "X-Run": "run"},
	}})
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	t.Cleanup(func() { reg.Shutdown(context.Background()) })
	return reg
}

// End to end against a stub server: calls under a job stamp the mapped
// headers, each job gets its own session, tools/list (discovery) and a call
// outside any job carry none, and a static header of that name is never sent.
func TestFlowArgHeadersReachTheServer(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := seedFlowArgServer(t, srv, "")

	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 1 {
		t.Fatalf("discovered %d tools, want 1", got)
	}

	jobA := jobCtx(map[string]any{"tenant": "acme", "run": float64(17)})
	jobB := jobCtx(map[string]any{"tenant": "globex", "run": "r-2"})
	jobBadRun := jobCtx(map[string]any{"tenant": "initech", "run": map[string]any{"x": 1}})
	for _, ctx := range []context.Context{jobA, jobB, jobA, jobBadRun, context.Background()} {
		if resp := reg.CallTool(ctx, "web", "echo", "{}"); resp.IsError {
			t.Fatalf("call: %s", resp.Content)
		}
	}

	if got, want := srv.headerValues("tools/call", "X-Tenant"), []string{"acme", "globex", "acme", "initech", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("tools/call X-Tenant = %q, want %q", got, want)
	}
	if got, want := srv.headerValues("tools/call", "X-Run"), []string{"17", "r-2", "17", "", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("tools/call X-Run = %q, want %q", got, want)
	}
	if got := srv.headerValues("tools/call", "X-Static"); !reflect.DeepEqual(got, []string{"kept", "kept", "kept", "kept", "kept"}) {
		t.Errorf("static headers not preserved: %q", got)
	}
	for _, v := range srv.headerValues("tools/list", "X-Tenant") {
		if v != "" {
			t.Errorf("tools/list carried X-Tenant %q", v)
		}
	}
	// One discovery client, one per distinct stamped identity (acme, globex,
	// initech), and the argless call reuses the discovery client.
	if got := srv.count("initialize"); got != 4 {
		t.Errorf("initialize sent %d times, want 4", got)
	}
	// The static value never reaches the server, even on the initialize.
	for _, m := range []string{"initialize", "tools/list", "tools/call"} {
		for _, v := range srv.headerValues(m, "X-Tenant") {
			if v == "static-tenant" {
				t.Errorf("%s carried the static X-Tenant", m)
			}
		}
	}
}

// A pooled client is keyed by its stamped headers: two jobs never share a
// session, the same job reuses its own, and the value is not in the key.
func TestFlowArgHeadersKeyThePool(t *testing.T) {
	mapping := map[string]string{"X-Tenant": "tenant"}
	a := resolveFlowArgHeaders(jobCtx(map[string]any{"tenant": "tenant-aaa"}), mapping, nil)
	b := resolveFlowArgHeaders(jobCtx(map[string]any{"tenant": "tenant-bbb"}), mapping, nil)
	cfg := config.MCPServer{Type: config.MCPHttp, URL: "http://example.test/mcp"}
	if mcpConnKey("web", cfg, a) == mcpConnKey("web", cfg, b) {
		t.Error("two jobs share a pooled client key")
	}
	if mcpConnKey("web", cfg, a) != mcpConnKey("web", cfg, a) {
		t.Error("the same job does not get a stable key")
	}
	if strings.Contains(mcpConnKey("web", cfg, a), "tenant-aaa") {
		t.Error("the key holds the stamped value in clear text")
	}
}

// The peer header and the flow-arg headers stamp together.
func TestFlowArgHeadersWithPeerHeader(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := seedFlowArgServer(t, srv, "X-Peer-Id")
	ctx := tools.WithPeer(jobCtx(map[string]any{"tenant": "acme"}), tools.Peer{Channel: "external", Identity: "default", PeerID: "t1:c1"})
	if resp := reg.CallTool(ctx, "web", "echo", "{}"); resp.IsError {
		t.Fatalf("call: %s", resp.Content)
	}
	if got := srv.headerValues("tools/call", "X-Peer-Id"); !reflect.DeepEqual(got, []string{"t1:c1"}) {
		t.Errorf("X-Peer-Id = %q", got)
	}
	if got := srv.headerValues("tools/call", "X-Tenant"); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Errorf("X-Tenant = %q", got)
	}
}

type jobArgsSubagent struct {
	Service // nil — only RunWith is called
	got     chan map[string]any
}

func (s *jobArgsSubagent) RunWith(ctx context.Context, _, _ string, _ int, _ RunOptions, _ ...message.Attachment) (<-chan AgentEvent, error) {
	args, _ := tools.JobArgsFromContext(ctx)
	s.got <- args
	done := make(chan AgentEvent, 1)
	done <- AgentEvent{Type: AgentEventTypeResponse}
	close(done)
	return done, nil
}

type jobArgsResumeRecorder struct{ resumed chan map[string]any }

func (r *jobArgsResumeRecorder) WritePair(context.Context, string, task.SyntheticPair) error {
	return nil
}
func (r *jobArgsResumeRecorder) IsSessionBusy(string) bool { return false }
func (r *jobArgsResumeRecorder) ResumeSession(_, _ string, jobArgs map[string]any) {
	r.resumed <- jobArgs
}

// An async subagent runs on a ctx detached from the spawning turn, and its
// completion auto-resumes the parent later; both must keep the job's args, so
// their MCP calls stamp the same flowArgHeaders.
func TestRunAsyncCarriesJobArgs(t *testing.T) {
	withFreshTaskRegistry(t)
	deps := &jobArgsResumeRecorder{resumed: make(chan map[string]any, 1)}
	t.Cleanup(task.SetDeps(deps))
	sub := &jobArgsSubagent{got: make(chan map[string]any, 1)}
	b := &agentTool{sessions: &memSessions{}}

	ctx := jobCtx(map[string]any{"tenant": "acme"})
	resp, err := b.runAsync(ctx, tools.ToolCall{ID: "call-async"}, TaskParams{TaskTitle: "bg"},
		"PARENT", "explorer", agentregistry.AgentInfo{}, session.Session{ID: "CHILD"}, false, sub, "do it")
	if err != nil || resp.IsError {
		t.Fatalf("runAsync: err=%v resp=%q", err, resp.Content)
	}

	if got := <-sub.got; got["tenant"] != "acme" {
		t.Errorf("subagent run job args = %v, want tenant=acme", got)
	}
	select {
	case got := <-deps.resumed:
		if got["tenant"] != "acme" {
			t.Errorf("parent resume job args = %v, want tenant=acme", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("async completion never resumed the parent session")
	}
}

// A job arg that any server maps through flowArgHeaders never reaches the
// exported trace, whatever telemetry.flowArgs asks for.
func TestCreateLangfuseTraceOmitsRedactedFlowArgs(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	seedMCPServers(t, map[string]config.MCPServer{"web": {
		Type: config.MCPHttp, URL: "http://example.test/mcp",
		FlowArgHeaders: map[string]string{"X-Tenant": "tenant_token"},
	}})
	config.Get().Telemetry = &config.TelemetryConfig{FlowArgs: []string{"*"}}

	ctx := context.WithValue(context.Background(), tools.FlowIDContextKey, "flow-1")
	ctx = context.WithValue(ctx, tools.FlowArgsContextKey, map[string]string{"ticket": "T-1", "tenant_token": "s3cret"})
	a := &agent{agentID: "coder", provider: &stubProvider{}}
	md := traceMetadata(t, a.createLangfuseTrace(ctx, session.Session{ID: "s1"}, nil))

	if got := md["ticket"]; got != "T-1" {
		t.Errorf("ticket = %v, want it kept (metadata: %v)", got, md)
	}
	for k, v := range md {
		if k == "tenant_token" || strings.Contains(v, "s3cret") {
			t.Errorf("trace metadata leaks the mapped arg: %s=%v", k, v)
		}
	}
}
