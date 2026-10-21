package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// CompactSessionToolName is the agent tool that requests compaction of the
// agent's own session (openspec capability agent-self-compaction). It is
// registered only for an agent whose selfCompact.enabled is set.
const CompactSessionToolName = "compact_session"

// SessionTokenCounter returns the context size of a session as the
// compaction decisions count it.
type SessionTokenCounter func(ctx context.Context, sessionID string) (int64, error)

// CompactSessionRequest is the metadata of a compact_session result that
// stored a request. The persisted tool result IS the request: it sits in the
// session's message log after the current summary, so it survives a restart,
// and the summary of the compaction that serves it lands after it, which
// consumes it. A skipped call carries no such metadata.
type CompactSessionRequest struct {
	Requested bool   `json:"compaction_requested"`
	Tokens    int64  `json:"tokens"`
	Keep      string `json:"keep,omitempty"`
}

// ParseCompactSessionRequest reads a stored request from a compact_session
// result's metadata.
func ParseCompactSessionRequest(metadata string) (CompactSessionRequest, bool) {
	if metadata == "" {
		return CompactSessionRequest{}, false
	}
	var req CompactSessionRequest
	if err := json.Unmarshal([]byte(metadata), &req); err != nil || !req.Requested {
		return CompactSessionRequest{}, false
	}
	return req, true
}

type compactSessionParams struct {
	Keep string `json:"keep,omitempty"`
}

// CompactSessionTool only records a request: the turn that calls it holds the
// session, so the compaction runs after the turn ends (see the agent's
// compactAfterTurn).
type CompactSessionTool struct {
	minTokens int64

	mu      sync.RWMutex
	counter SessionTokenCounter
}

// NewCompactSessionTool builds the tool with the agent's token floor. The
// counter is bound once the agent's tool set is resolved (BindCounter).
func NewCompactSessionTool(minTokens int64) *CompactSessionTool {
	return &CompactSessionTool{minTokens: max(minTokens, 0)}
}

// BindCounter installs the function that counts the session's context.
func (t *CompactSessionTool) BindCounter(c SessionTokenCounter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counter = c
}

func (t *CompactSessionTool) Info() ToolInfo {
	return ToolInfo{
		Name: CompactSessionToolName,
		Description: `Compact your own session once a task is finished, so the next task does not carry its tool calls and output.

Call it at the end of a turn, when the task you were given is done and nothing belonging to it is still open. The session is summarized right after this turn ends, once your reply has been delivered; the next turn starts from the summary. Nothing happens during this turn, so finish your reply as usual.

Pass in keep anything the next task needs and a summary could lose: open items, pending decisions, ids of background work still running, names and identifiers. It is carried into the summary verbatim.

Returns the session's current context size. Below the configured floor the call returns "skipped" and nothing is compacted.`,
		Parameters: map[string]any{
			"keep": map[string]any{
				"type":        "string",
				"description": "Notes to carry into the summary verbatim: open items, pending decisions, running background task ids, identifiers",
			},
		},
	}
}

func (t *CompactSessionTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params compactSessionParams
	if strings.TrimSpace(call.Input) != "" {
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
		}
	}
	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewTextErrorResponse("session context required"), nil
	}
	t.mu.RLock()
	counter := t.counter
	t.mu.RUnlock()
	if counter == nil {
		return NewTextErrorResponse("compact_session is not available yet; nothing was stored"), nil
	}
	tokens, err := counter(ctx, sessionID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("counting the session's context failed: %s; nothing was stored", err)), nil
	}
	if tokens < t.minTokens {
		return NewTextResponse(fmt.Sprintf("skipped: %d tokens < floor %d", tokens, t.minTokens)), nil
	}
	return WithResponseMetadata(
		NewTextResponse(fmt.Sprintf("requested: %d tokens in context; the session is compacted after this turn ends", tokens)),
		CompactSessionRequest{Requested: true, Tokens: tokens, Keep: strings.TrimSpace(params.Keep)},
	), nil
}

// AllowParallelism is false so a request is recorded in call order.
func (t *CompactSessionTool) AllowParallelism(ToolCall, []ToolCall) bool { return false }

func (t *CompactSessionTool) IsBaseline() bool { return true }
