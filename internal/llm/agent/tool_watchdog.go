package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

// toolCallTimeoutMargin is added to an MCP server's own per-call timeout so
// the MCP layer reports its timeout (and evicts a stdio client) before the
// loop watchdog fires.
const toolCallTimeoutMargin = time.Minute

// toolCallWatchdogExempt are the tools the loop never bounds: they carry their
// own timeouts (bash), wait on a human (question) or run a whole subagent
// whose own tool calls are bounded (task).
var toolCallWatchdogExempt = map[string]struct{}{
	TaskToolName:           {},
	tools.QuestionToolName: {},
	tools.BashToolName:     {},
}

// toolCallLimit is the watchdog bound for one call of tool by agentID: the
// configured toolCallTimeoutSeconds, raised for an MCP tool to its server's
// per-call timeout plus a margin. Zero means unbounded.
func toolCallLimit(agentID config.AgentName, tool tools.BaseTool) time.Duration {
	if _, ok := toolCallWatchdogExempt[tool.Info().Name]; ok {
		return 0
	}
	limit := config.ToolCallTimeout(agentID)
	if limit <= 0 {
		return 0
	}
	inner := tool
	if w, ok := inner.(*tools.DeferredWrapper); ok {
		inner = w.Unwrap()
	}
	if m, ok := inner.(*mcpTool); ok {
		if srv, ok := config.ResolveMCPServers()[m.mcpName]; ok {
			limit = max(limit, resolveCallToolTimeout(srv)+toolCallTimeoutMargin)
		}
	}
	return limit
}

// permissionWaits tracks how long one tool call has spent waiting on
// permission prompts, so the watchdog can leave that time out of the call's
// budget: the call is blocked on a human, not stuck.
type permissionWaits struct {
	mu      sync.Mutex
	waiting int
	since   time.Time
	total   time.Duration
	ended   chan struct{}
}

func newPermissionWaits() *permissionWaits {
	return &permissionWaits{ended: make(chan struct{}, 1)}
}

func (p *permissionWaits) WaitStarted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting == 0 {
		p.since = time.Now()
	}
	p.waiting++
}

func (p *permissionWaits) WaitEnded() {
	p.mu.Lock()
	p.waiting--
	if p.waiting == 0 {
		p.total += time.Since(p.since)
	}
	p.mu.Unlock()
	select {
	case p.ended <- struct{}{}:
	default:
	}
}

// paused is the total time spent waiting so far, including a wait in progress.
func (p *permissionWaits) paused() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting > 0 {
		return p.total + time.Since(p.since)
	}
	return p.total
}

// runToolBounded runs one tool call. With a positive limit the call gets its
// own cancellable ctx: past the limit it is cancelled and abandoned, and an
// error result is returned so the model can carry on, even when the tool
// ignores its ctx. A cancelled parent ctx also returns at once instead of
// waiting on such a tool. Time spent waiting on a permission prompt does not
// count towards the limit.
func runToolBounded(ctx context.Context, limit time.Duration, tool tools.BaseTool, call tools.ToolCall) (tools.ToolResponse, error) {
	if limit <= 0 {
		return tool.Run(ctx, call)
	}
	waits := newPermissionWaits()
	toolCtx, cancel := context.WithCancel(permission.WithWaitObserver(ctx, waits))
	defer cancel()
	type outcome struct {
		resp tools.ToolResponse
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		resp, err := tool.Run(toolCtx, call)
		ch <- outcome{resp, err}
	}()
	start := time.Now()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	rearm := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(max(limit-(time.Since(start)-waits.paused()), time.Millisecond))
	}
	for {
		select {
		case o := <-ch:
			return o.resp, o.err
		case <-ctx.Done():
			return tools.ToolResponse{}, ctx.Err()
		case <-waits.ended:
			rearm()
		case <-timer.C:
			if time.Since(start)-waits.paused() < limit {
				rearm()
				continue
			}
			logging.Warn("Tool call exceeded the loop watchdog; abandoning it",
				"tool", call.Name, "ID", call.ID, "limit", limit)
			return tools.NewTextErrorResponse(fmt.Sprintf(
				"tool %s exceeded %s and was abandoned. The agent should try a different approach or skip this step.",
				call.Name, limit,
			)), nil
		}
	}
}
