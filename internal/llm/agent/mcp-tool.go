package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent/mcpauthctx"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/version"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type (
	MCPServerEventType string

	MCPServerEvent struct {
		Type       MCPServerEventType
		ServerName string
		ToolCount  int
		Error      error
	}
)

const (
	MCPServerToolsLoaded MCPServerEventType = "tools_loaded"
	MCPServerError       MCPServerEventType = "error"
)

type (
	MCPClient interface {
		Initialize(
			ctx context.Context,
			request mcp.InitializeRequest,
		) (*mcp.InitializeResult, error)
		ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
		CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)
		Close() error
	}

	MCPRegistry interface {
		// LoadTools returns tools matching filter, if registry is not loaded it will
		// begin loading. Load lifetime is owned by the registry (bounded per server by
		// mcpInitTimeout), never by a caller: results land in a cache shared across
		// every agent, and the returned channel always closes within that bound.
		LoadTools(filter *MCPRegistryFiler) <-chan tools.BaseTool
		// StartClient starts a new MCPClient, caller have to properly close when done
		StartClient(ctx context.Context, name string) (c *client.Client, err error)
		// SetDiscoveryAuth records Authorization overrides that registry-owned
		// DISCOVERY fetches apply, keyed by MCP server name (value is the full
		// header, e.g. "Bearer <jwt>"). Passing nil or an empty map clears them.
		//
		// Tool CALLS get their override from the caller's context
		// (mcpauthctx) — per-call, per-run, no shared state. Discovery cannot
		// work that way: it is deliberately context-free (NewToolSet /
		// getTools run under the registry's own lifetime so a short-lived
		// creator can't strand an agent without MCP tools), so the run's
		// token has no context to ride in on. Without this seam a pool pod
		// never authenticates `initialize`/`tools/list`, the server returns
		// 401, and its tools are absent for the whole run — the per-call
		// override then has no tool to apply to.
		SetDiscoveryAuth(overrides map[string]string)
		// LoadedServers returns the set of MCP server names that have successfully loaded tools.
		LoadedServers() map[string]bool
		// ServerTools returns the tool names for a loaded MCP server (without the server prefix).
		ServerTools(name string) []string
		pubsub.Suscriber[MCPServerEvent]
	}
	MCPRegistryFiler struct {
		ToolNames   []string
		ServerNames []string
	}
	mcpRegistry struct {
		// *mcp.ListToolsResult by MCP server name
		mcpTools sync.Map

		// baseCtx owns the lifetime of cache fetches (see getTools). Fetch
		// results are shared singleflight-style across every agent, so a
		// fetch must never run under one caller's request-scoped context —
		// that caller being canceled would poison the entry for every
		// concurrent and future waiter. Normally the app root context.
		baseCtx context.Context

		// discoveryAuth holds Authorization overrides applied to fetches
		// performed under baseCtx (see SetDiscoveryAuth). Guarded by
		// discoveryAuthMu; read on every cache miss, written once per flow
		// run by the flow runner.
		discoveryAuthMu sync.RWMutex
		discoveryAuth   map[string]string

		permissions   permission.Service
		agentRegistry agentregistry.Registry
		*pubsub.Broker[MCPServerEvent]
	}

	mcpTool struct {
		mcpName     string
		tool        mcp.Tool
		mcpConfig   config.MCPServer
		permissions permission.Service
		reg         agentregistry.Registry
		mcpReg      MCPRegistry
	}
)

// NewMCPRegistry creates the shared MCP registry. ctx bounds the lifetime of
// every cache fetch the registry performs (app shutdown stops in-flight
// loads); it must be a long-lived context, not a request-scoped one.
func NewMCPRegistry(ctx context.Context, permissions permission.Service, agentRegistry agentregistry.Registry) MCPRegistry {
	if ctx == nil {
		ctx = context.Background()
	}
	return &mcpRegistry{
		mcpTools:      sync.Map{},
		baseCtx:       ctx,
		permissions:   permissions,
		agentRegistry: agentRegistry,
		Broker:        pubsub.NewBroker[MCPServerEvent](),
	}
}

func (r *mcpRegistry) StartClient(ctx context.Context, name string) (c *client.Client, err error) {
	m, ok := config.ResolveMCPServers()[name]
	if !ok {
		return nil, fmt.Errorf("no mcp found with name %s", name)
	}

	// Layer a context-scoped Authorization override (per-flow MCP auth,
	// openspec change agent-pod-pool-runtime D1) on top of the static
	// config headers. The override shadows any boot-time Authorization
	// value for the duration of the calling context only; the shared
	// config map is never mutated.
	headers := resolveMCPHeaders(ctx, name, m.Headers)

	startCtx, cancelStart := context.WithTimeout(ctx, 20*time.Second)
	defer cancelStart()
	switch m.Type {
	case config.MCPStdio:
		c, err = client.NewStdioMCPClient(
			m.Command,
			m.Env,
			m.Args...,
		)
	case config.MCPSse:
		c, err = client.NewSSEMCPClient(
			m.URL,
			client.WithHeaders(headers),
		)
	case config.MCPHttp:
		c, err = client.NewStreamableHttpClient(
			m.URL,
			transport.WithHTTPHeaders(headers),
		)
	}
	if err != nil {
		logging.Error("Error creating MCP client", "server", name, "cause", err)
		return nil, err
	}
	if err = c.Start(startCtx); err != nil {
		logging.Error("Error starting MCP client", "server", m.Command, "cause", err)
		// The constructors above already spawned the child process (stdio) or
		// built the transport (sse/http). Returning a nil client here would
		// orphan it with no reference left to close: nothing would ever reap a
		// stdio server whose Start merely timed out.
		if cerr := c.Close(); cerr != nil {
			logging.Warn("Error closing MCP client after failed start", "server", name, "cause", cerr)
		}
		return nil, err
	}
	return c, nil
}

// resolveMCPHeaders returns the header map to construct an MCP client
// with: the server's static config headers, with a context-scoped
// Authorization override (mcpauthctx.WithAuthOverride, stamped per flow
// run by the flow runner) layered on top when present. The static map
// is returned untouched when no override applies; when one does, a
// fresh copy is built so the shared config map is never mutated —
// concurrent tool calls under different run contexts each see their own
// Authorization value.
//
// The copy drops every pre-existing key that canonicalises to
// Authorization before setting the override. That is not defensive
// tidying: viper lower-cases map keys when loading .opencode.json, so a
// config declaring "Authorization" arrives as "authorization". Setting
// the override under the canonical spelling alongside it would leave the
// map holding two keys that both target the same HTTP header, and the
// mcp-go transport applies headers with `for k, v := range headers {
// req.Header.Set(k, v) }` — so which token actually went out would be
// decided by Go's randomised map iteration order, producing intermittent
// 401s that succeed on retry.
func resolveMCPHeaders(ctx context.Context, name string, static map[string]string) map[string]string {
	override, ok := mcpauthctx.AuthOverrideFromContext(ctx, name)
	if !ok {
		return static
	}
	return layerAuthorization(static, override)
}

// authorizationHeader is the canonical spelling the override is written
// under. net/http canonicalises on Set, but the map itself must hold
// exactly one Authorization-equivalent key — see resolveMCPHeaders.
const authorizationHeader = "Authorization"

// layerAuthorization copies static and replaces any Authorization header
// (in any letter case) with value.
func layerAuthorization(static map[string]string, value string) map[string]string {
	layered := make(map[string]string, len(static)+1)
	for k, v := range static {
		if strings.EqualFold(k, authorizationHeader) {
			continue
		}
		layered[k] = v
	}
	layered[authorizationHeader] = value
	return layered
}

// SetDiscoveryAuth records the Authorization overrides applied to
// registry-owned discovery fetches. See the interface doc for why
// discovery cannot take these from a context.
func (r *mcpRegistry) SetDiscoveryAuth(overrides map[string]string) {
	next := make(map[string]string, len(overrides))
	for k, v := range overrides {
		if k == "" || v == "" {
			continue
		}
		next[k] = v
	}
	r.discoveryAuthMu.Lock()
	defer r.discoveryAuthMu.Unlock()
	if len(next) == 0 {
		r.discoveryAuth = nil
		return
	}
	r.discoveryAuth = next
}

// discoveryCtx returns baseCtx carrying the recorded discovery auth
// overrides, so StartClient's existing mcpauthctx lookup finds them.
// Values only — the lifetime stays baseCtx's, which is the invariant
// TestMCPRegistry_LoadToolsRegistryOwnedLifetime and
// TestMCPRegistry_ShutdownBoundsFetch pin.
func (r *mcpRegistry) discoveryCtx() context.Context {
	r.discoveryAuthMu.RLock()
	defer r.discoveryAuthMu.RUnlock()
	ctx := r.baseCtx
	for server, header := range r.discoveryAuth {
		ctx = mcpauthctx.WithAuthOverride(ctx, server, header)
	}
	return ctx
}

func (r *mcpRegistry) LoadedServers() map[string]bool {
	result := make(map[string]bool)
	r.mcpTools.Range(func(key, value any) bool {
		entry := value.(*toolsCacheEntry)
		select {
		case <-entry.done:
			if entry.err == nil && entry.data != nil && len(entry.data.Tools) > 0 {
				result[key.(string)] = true
			}
		default:
		}
		return true
	})
	return result
}

func (r *mcpRegistry) ServerTools(name string) []string {
	value, ok := r.mcpTools.Load(name)
	if !ok {
		return nil
	}
	entry := value.(*toolsCacheEntry)
	select {
	case <-entry.done:
		if entry.err != nil || entry.data == nil {
			return nil
		}
		names := make([]string, len(entry.data.Tools))
		for i, t := range entry.data.Tools {
			names[i] = t.Name
		}
		return names
	default:
		return nil
	}
}

func (r *mcpRegistry) LoadTools(filter *MCPRegistryFiler) <-chan tools.BaseTool {
	toolsCh := make(chan tools.BaseTool, 100)

	go func(filter *MCPRegistryFiler) {
		wg := sync.WaitGroup{}
		for name, m := range config.ResolveMCPServers() {
			if filter != nil && len(filter.ServerNames) != 0 && !slices.Contains(filter.ServerNames, name) {
				continue
			}

			wg.Add(1)
			go func(filter *MCPRegistryFiler) {
				defer wg.Done()

				serverTools := r.getTools(name, m)
				for _, t := range serverTools {
					if filter != nil && len(filter.ToolNames) != 0 && !slices.Contains(filter.ToolNames, t.Info().Name) {
						continue
					}
					toolsCh <- t
				}
				r.Publish(pubsub.CreatedEvent, MCPServerEvent{
					Type:       MCPServerToolsLoaded,
					ServerName: name,
					ToolCount:  len(serverTools),
				})
			}(filter)
		}
		wg.Wait()
		close(toolsCh)
	}(filter)
	return toolsCh
}

const (
	ttl = 30 * time.Minute
	// mcpInitTimeout bounds every wait on an MCP server becoming usable: a
	// single cache fetch (start + initialize + list tools), the getTools waiter
	// path's backstop on entry.done, and the per-call initialize handshake in
	// runTool. Deliberately NOT per-server overridable the way
	// callToolTimeoutSeconds is: tool latency is genuinely server-specific,
	// handshake latency is not — initialize is one request/response with no
	// work behind it, so a server that misses this budget is broken, not slow.
	mcpCallToolTimeout = 5 * time.Minute
	// mcpCallToolMaxOutputBytes is the default cap on a single MCP tool call's
	// output kept in the model context. Output beyond this is spilled to a temp
	// file and replaced with a head+tail preview. Overridable per server via
	// MCPServer.CallToolMaxOutputBytes.
	mcpCallToolMaxOutputBytes = 50 * 1024 // 50KB
)

// A var rather than a const so tests can shorten it; never mutated at runtime.
var mcpInitTimeout = 30 * time.Second

type toolsCacheEntry struct {
	done chan bool
	data *mcp.ListToolsResult
	ts   int64
	err  error
	del  atomic.Bool
}

func (entry *toolsCacheEntry) expired() bool {
	now := time.Now().UnixMilli()
	return now > entry.ts+ttl.Milliseconds()
}

// awaitCacheEntry waits for an in-flight MCP client-cache fetch to finish.
// Returns true when the entry became ready, false when the caller should give
// up and contribute no tools for this server.
//
// On give-up the entry is deliberately LEFT IN PLACE: the fetcher owns its
// lifecycle, matching what getToolsAttempt does when a fetch completes with
// entry.err set. Deleting it here would let a wedged fetcher's eventual close
// race a freshly stored entry.
//
// budget must stay >= the fetcher's own fetchCtx budget, or a healthy but
// slow-starting server gets skipped by waiters while its fetch is still
// legitimately in flight. Equal is the minimum safe value; if this proves
// flaky in practice, lengthen the waiter's budget, not the fetcher's.
func awaitCacheEntry(ctx context.Context, name string, entry *toolsCacheEntry, budget time.Duration) bool {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-entry.done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		logging.Warn("MCP client cache wait exceeded the fetch budget; skipping server for this resolution",
			"server", name, "budget", budget)
		return false
	}
}

func (r *mcpRegistry) getTools(name string, m config.MCPServer) []tools.BaseTool {
	return r.getToolsAttempt(name, m, true)
}

// getToolsAttempt is getTools with an explicit "may I retry" budget.
// retryOnInheritedErr is true for the caller's first attempt and false
// for the one retry it is allowed, so the recursion is bounded at two.
func (r *mcpRegistry) getToolsAttempt(name string, m config.MCPServer, retryOnInheritedErr bool) []tools.BaseTool {
	toolsToAdd := []tools.BaseTool{}
	entry := &toolsCacheEntry{done: make(chan bool)}
	value, loaded := r.mcpTools.LoadOrStore(name, entry)

	if loaded {
		entry = value.(*toolsCacheEntry)
	}

	if loaded {
		// cache/reuse — wait for the (possibly in-flight) fetch. The fetcher
		// closes entry.done from a defer, which does not run while it is blocked
		// in a call that ignores its ctx (a started-but-mute stdio server), so
		// the wait carries its own backstop rather than trusting that invariant.
		if !awaitCacheEntry(r.discoveryCtx(), name, entry, mcpInitTimeout) {
			return toolsToAdd
		}
		// entry is a pointer, and close(done) provides
		// happens-before: entry.data and entry.err are
		// visible directly
		if entry.expired() && entry.del.CompareAndSwap(false, true) {
			logging.Debug("MCP client cache expired", "server", name, "ts", entry.ts)
			r.mcpTools.Delete(name)
		} else {
			logging.Debug("MCP client cache is used", "server", name, "ts", entry.ts)
		}
	} else {
		// fetch — runs under the registry's own context, never a caller's.
		// The entry is shared by every concurrent and future waiter, so a
		// request-scoped context here lets one canceled caller destroy the
		// toolset of unrelated agents: an async `task` spawn builds the
		// subagent's toolset under the parent's parallel-tool-batch ctx,
		// and the batch is canceled as soon as the ack returns — with a
		// cold cache the subagent resolved zero MCP tools.
		// discoveryCtx is baseCtx plus the recorded per-run Authorization
		// overrides: values only, lifetime unchanged. A server whose only
		// credential is the run's token (the orchestrator MCP endpoint on a
		// pool pod) would otherwise 401 on initialize and contribute no
		// tools at all.
		fetchCtx, cancelFetch := context.WithTimeout(r.discoveryCtx(), mcpInitTimeout)
		defer cancelFetch()
		defer close(entry.done)

		var c *client.Client
		c, entry.err = r.StartClient(fetchCtx, name)
		if entry.err != nil {
			logging.Error("Error starting MCP client", "server", name, "cause", entry.err.Error())
			r.mcpTools.Delete(name)
			return toolsToAdd
		}
		defer c.Close()

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "opencode",
			Version: version.Version,
		}

		_, entry.err = c.Initialize(fetchCtx, initRequest)
		if entry.err != nil {
			logging.Error("Error initializing MCP client", "server", name, "cause", entry.err.Error())
			r.mcpTools.Delete(name)
			return toolsToAdd
		}
		toolsRequest := mcp.ListToolsRequest{}
		entry.data, entry.err = c.ListTools(fetchCtx, toolsRequest)
		if entry.err != nil {
			logging.Error("Error listing MCP tools", "server", name, "cause", entry.err.Error())
			r.mcpTools.Delete(name)
			return toolsToAdd
		}
		entry.ts = time.Now().UnixMilli()
		logging.Debug("MCP client cache is updated", "server", name, "ts", entry.ts)
	}

	if entry.err != nil {
		// We may have INHERITED this error from a fetch someone else was
		// already running — typically the boot-time one, which on a pool
		// pod runs before any per-run MCP token exists and therefore 401s.
		// Letting that verdict stand would freeze an empty toolset into
		// this caller's agent (sync.Once) for the agent's whole life.
		// The failing fetcher already removed the entry from the map, so
		// one retry here re-fetches under the credentials in effect NOW.
		// Bounded to a single extra attempt; a genuinely unreachable
		// server still costs at most two tries per caller.
		if loaded && retryOnInheritedErr {
			logging.Debug("MCP discovery inherited a failed fetch — retrying once under current credentials", "server", name)
			return r.getToolsAttempt(name, m, false)
		}
		return toolsToAdd
	}

	if entry.data != nil {
		for _, t := range entry.data.Tools {
			toolsToAdd = append(toolsToAdd, newMCPTool(name, t, r.permissions, m, r.agentRegistry, r))
		}
	}
	return toolsToAdd
}

func newMCPTool(
	name string,
	tool mcp.Tool,
	permissions permission.Service,
	mcpConfig config.MCPServer,
	reg agentregistry.Registry,
	mcpReg MCPRegistry,
) tools.BaseTool {
	return &mcpTool{
		mcpName:     name,
		tool:        tool,
		mcpConfig:   mcpConfig,
		permissions: permissions,
		reg:         reg,
		mcpReg:      mcpReg,
	}
}

func (b *mcpTool) Info() tools.ToolInfo {
	required := b.tool.InputSchema.Required
	if required == nil {
		required = make([]string, 0)
	}
	return tools.ToolInfo{
		Name:        fmt.Sprintf("%s_%s", b.mcpName, b.tool.Name),
		Description: b.tool.Description,
		Parameters:  b.tool.InputSchema.Properties,
		Required:    required,
	}
}

func (b *mcpTool) Run(ctx context.Context, params tools.ToolCall) (tools.ToolResponse, error) {
	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}

	action := b.reg.EvaluatePermission(string(tools.GetAgentID(ctx)), b.Info().Name, params.Input)
	switch action {
	case permission.ActionAllow:
	case permission.ActionDeny:
		return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		permissionDescription := fmt.Sprintf("execute %s with the following parameters: %s", b.Info().Name, params.Input)
		p := b.permissions.Request(ctx,
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        config.WorkingDirectory(),
				ToolName:    b.Info().Name,
				Action:      "execute",
				Description: permissionDescription,
				Params:      params.Input,
			},
		)
		if !p {
			return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	c, err := b.mcpReg.StartClient(ctx, b.mcpName)
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}
	defer c.Close()
	return runTool(ctx, c, b.tool.Name, params.Input, resolveCallToolTimeout(b.mcpConfig), resolveCallToolMaxOutputBytes(b.mcpConfig))
}

// resolveCallToolTimeout returns the per-call timeout for an MCP server. A positive
// CallToolTimeoutSeconds in the server config overrides the default; 0 or omitted falls
// back to mcpCallToolTimeout.
func resolveCallToolTimeout(m config.MCPServer) time.Duration {
	if m.CallToolTimeoutSeconds > 0 {
		return time.Duration(m.CallToolTimeoutSeconds) * time.Second
	}
	return mcpCallToolTimeout
}

// resolveCallToolMaxOutputBytes returns the per-call output-size cap (bytes) for an MCP
// server. A positive CallToolMaxOutputBytes overrides the default; a negative value
// disables the cap entirely (unbounded, signalled as -1); 0 or omitted falls back to
// mcpCallToolMaxOutputBytes.
func resolveCallToolMaxOutputBytes(m config.MCPServer) int {
	switch {
	case m.CallToolMaxOutputBytes < 0:
		return -1
	case m.CallToolMaxOutputBytes > 0:
		return m.CallToolMaxOutputBytes
	default:
		return mcpCallToolMaxOutputBytes
	}
}

func runTool(ctx context.Context, c MCPClient, toolName string, input string, callTimeout time.Duration, maxOutputBytes int) (tools.ToolResponse, error) {
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "OpenCode",
		Version: version.Version,
	}

	// The handshake needs its own deadline: it is one request/response with no
	// work behind it, so a server that has not answered within mcpInitTimeout is
	// broken rather than slow. Unbounded, a server that starts but never replies
	// parks the caller for the life of the process, and callTimeout below never
	// applies because CallTool is never reached.
	//
	// cancelInit is called explicitly, not deferred: a defer would hold the timer
	// across the up-to-callTimeout CallTool that follows.
	initCtx, cancelInit := context.WithTimeout(ctx, mcpInitTimeout)
	_, err := c.Initialize(initCtx, initRequest)
	initErr := initCtx.Err()
	cancelInit()
	if err != nil {
		// Only attribute the timeout to our handshake budget while the parent ctx
		// is still alive — otherwise the deadline came from upstream and naming
		// mcpInitTimeout would be misleading.
		if ctx.Err() == nil && initErr == context.DeadlineExceeded {
			return tools.NewTextErrorResponse(fmt.Sprintf(
				"MCP handshake for tool %q did not complete within %s — the server started but never answered initialize. The agent should try a different approach or skip this step.",
				toolName, mcpInitTimeout,
			)), nil
		}
		return tools.NewTextErrorResponse(err.Error()), nil
	}

	toolRequest := mcp.CallToolRequest{}
	toolRequest.Params.Name = toolName
	var args map[string]any
	if err = json.Unmarshal([]byte(input), &args); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	toolRequest.Params.Arguments = args

	callCtx, cancelCall := context.WithTimeout(ctx, callTimeout)
	defer cancelCall()
	result, err := c.CallTool(callCtx, toolRequest)
	if err != nil {
		// Only attribute the timeout to our per-call budget when the parent ctx is
		// still alive — otherwise the deadline came from upstream and reporting our
		// callTimeout would be misleading.
		if ctx.Err() == nil && callCtx.Err() == context.DeadlineExceeded {
			return tools.NewTextErrorResponse(fmt.Sprintf(
				"MCP tool %q timed out after %s — upstream MCP server did not respond. The agent should try a different approach or skip this step.",
				toolName, callTimeout,
			)), nil
		}
		return tools.NewTextErrorResponse(err.Error()), nil
	}

	// Concatenate every content block. (Previously only the last block survived,
	// which silently dropped data for multi-block results — and would undercount
	// the output when applying the size cap below.)
	var sb strings.Builder
	for _, v := range result.Content {
		if tc, ok := v.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		} else {
			fmt.Fprintf(&sb, "%v", v)
		}
	}

	// Cap the output kept in context; oversized results spill to a temp file and
	// are replaced with a head+tail preview pointing at it (the agent explores it
	// via grep/read/bash). This guards the context window against tools that
	// return very large payloads (e.g. multi-MB CI build logs). NewTextResponse
	// still applies the global backstop cap on top.
	output := sb.String()
	preview, filePath := tools.PersistLargeOutput(output, toolName, "mcp", maxOutputBytes)
	if filePath != "" {
		logging.Info("MCP tool output capped",
			"tool", toolName, "totalBytes", len(output), "maxOutputBytes", maxOutputBytes, "file", filePath)
	}
	return tools.NewTextResponse(preview), nil
}

func (b *mcpTool) AllowParallelism(call tools.ToolCall, allCalls []tools.ToolCall) bool {
	return true
}

func (b *mcpTool) IsBaseline() bool { return false }
