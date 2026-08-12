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
			client.WithHeaders(m.Headers),
		)
	case config.MCPHttp:
		c, err = client.NewStreamableHttpClient(
			m.URL,
			transport.WithHTTPHeaders(m.Headers),
		)
	}
	if err != nil {
		logging.Error("Error creating MCP client", "server", name, "cause", err)
		return nil, err
	}
	if err = c.Start(startCtx); err != nil {
		logging.Error("Error starting MCP client", "server", m.Command, "cause", err)
		return nil, err
	}
	return c, nil
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
	// mcpInitTimeout bounds a single cache fetch (start + initialize + list
	// tools) for one MCP server. It is also what bounds getTools' waiter
	// path: every fetcher closes entry.done within this budget.
	mcpInitTimeout     = 30 * time.Second
	mcpCallToolTimeout = 5 * time.Minute
	// mcpCallToolMaxOutputBytes is the default cap on a single MCP tool call's
	// output kept in the model context. Output beyond this is spilled to a temp
	// file and replaced with a head+tail preview. Overridable per server via
	// MCPServer.CallToolMaxOutputBytes.
	mcpCallToolMaxOutputBytes = 50 * 1024 // 50KB
)

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

func (r *mcpRegistry) getTools(name string, m config.MCPServer) []tools.BaseTool {
	toolsToAdd := []tools.BaseTool{}
	entry := &toolsCacheEntry{done: make(chan bool)}
	value, loaded := r.mcpTools.LoadOrStore(name, entry)

	if loaded {
		entry = value.(*toolsCacheEntry)
	}

	if loaded {
		// cache/reuse — wait for the (possibly in-flight) fetch. Bounded:
		// the fetcher closes entry.done within mcpInitTimeout.
		<-entry.done
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
		fetchCtx, cancelFetch := context.WithTimeout(r.baseCtx, mcpInitTimeout)
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

	_, err := c.Initialize(ctx, initRequest)
	if err != nil {
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
