package acp

// JSON-RPC 2.0 base types

// Request is a JSON-RPC 2.0 request message.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response message.
type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification (no ID).
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
)

// ACP protocol types

// InitializeParams is the params for the "initialize" method.
type InitializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientCapabilities map[string]any `json:"clientCapabilities,omitempty"`
	ClientInfo         *ClientInfo    `json:"clientInfo,omitempty"`
}

// ClientInfo describes the connecting client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeResult is the result of the "initialize" method.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         AgentInfo         `json:"agentInfo"`
}

// AgentCapabilities declares what the agent supports.
type AgentCapabilities struct {
	LoadSession         bool                 `json:"loadSession,omitempty"`
	PromptCapabilities  *PromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities *SessionCapabilities `json:"sessionCapabilities,omitempty"`
}

// PromptCapabilities declares prompt-related capabilities.
type PromptCapabilities struct {
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
	Image           bool `json:"image,omitempty"`
}

// SessionCapabilities declares session-related capabilities.
type SessionCapabilities struct {
	Close  *struct{} `json:"close,omitempty"`
	List   *struct{} `json:"list,omitempty"`
	Resume *struct{} `json:"resume,omitempty"`
}

// AgentInfo describes the agent.
type AgentInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// NewSessionParams is the params for "session/new".
type NewSessionParams struct {
	CWD string `json:"cwd"`
}

// NewSessionResult is the result of "session/new".
type NewSessionResult struct {
	SessionID string      `json:"sessionId"`
	Models    *ModelsInfo `json:"models,omitempty"`
	Modes     *ModesInfo  `json:"modes,omitempty"`
}

// ModelsInfo describes available models.
type ModelsInfo struct {
	CurrentModelID  string        `json:"currentModelId"`
	AvailableModels []ModelOption `json:"availableModels"`
}

// ModelOption is a single available model.
type ModelOption struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}

// ModesInfo describes available agent modes.
type ModesInfo struct {
	CurrentModeID  string       `json:"currentModeId"`
	AvailableModes []ModeOption `json:"availableModes"`
}

// ModeOption is a single available agent mode.
type ModeOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// LoadSessionParams is the params for "session/load".
type LoadSessionParams struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

// LoadSessionResult is the result of "session/load".
type LoadSessionResult = NewSessionResult

// ListSessionsParams is the params for "session/list".
type ListSessionsParams struct {
	CWD    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// ListSessionsResult is the result of "session/list".
type ListSessionsResult struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// SessionInfo describes a session in a list.
type SessionInfo struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
}

// CloseSessionParams is the params for "session/close".
type CloseSessionParams struct {
	SessionID string `json:"sessionId"`
}

// ResumeSessionParams is the params for "session/resume".
type ResumeSessionParams struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

// PromptParams is the params for "session/prompt".
type PromptParams struct {
	SessionID string       `json:"sessionId"`
	Prompt    []PromptPart `json:"prompt"`
}

// PromptPart is a single part of a prompt message.
// Supports text ({type:"text"}), image ({type:"image"}), and
// resource_link ({type:"resource_link"}) content from ACP clients.
type PromptPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Image fields
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"` // base64
	URI      string `json:"uri,omitempty"`
	// Resource link fields
	Name string `json:"name,omitempty"`
}

// PromptResult is the result of "session/prompt".
type PromptResult struct {
	StopReason string `json:"stopReason"`
	Usage      *Usage `json:"usage,omitempty"`
}

// Usage describes token usage.
type Usage struct {
	TotalTokens       int64 `json:"totalTokens"`
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	ThoughtTokens     int64 `json:"thoughtTokens,omitempty"`
	CachedReadTokens  int64 `json:"cachedReadTokens,omitempty"`
	CachedWriteTokens int64 `json:"cachedWriteTokens,omitempty"`
}

// CancelParams is the params for "session/cancel".
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// SessionUpdate is a notification sent from agent to client.
type SessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// AgentMessageChunk is a session update for text chunks.
type AgentMessageChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	MessageID     string       `json:"messageId"`
	Content       ContentBlock `json:"content"`
}

// AgentThoughtChunk is a session update for reasoning chunks.
type AgentThoughtChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	MessageID     string       `json:"messageId"`
	Content       ContentBlock `json:"content"`
}

// ToolCallUpdate is a session update for tool call state changes.
type ToolCallUpdate struct {
	SessionUpdate string        `json:"sessionUpdate"`
	ToolCallID    string        `json:"toolCallId"`
	Status        string        `json:"status"` // "pending", "in_progress", "completed", "failed"
	Kind          string        `json:"kind"`   // "execute", "edit", "read", "search", "fetch", "other"
	Title         string        `json:"title"`
	Locations     []Location    `json:"locations,omitempty"`
	RawInput      any           `json:"rawInput,omitempty"`
	RawOutput     any           `json:"rawOutput,omitempty"`
	Content       []ToolContent `json:"content,omitempty"`
}

// ToolCallNotification is a session update for new tool calls.
type ToolCallNotification struct {
	SessionUpdate string     `json:"sessionUpdate"`
	ToolCallID    string     `json:"toolCallId"`
	Title         string     `json:"title"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Locations     []Location `json:"locations"`
	RawInput      any        `json:"rawInput"`
}

// UsageUpdate is a session update for token usage.
type UsageUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Used          int64  `json:"used"`
	Size          int64  `json:"size"`
	Cost          *Cost  `json:"cost,omitempty"`
}

// Cost represents monetary cost.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// ContentBlock is a content block in a message chunk.
type ContentBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// ToolContent is content within a tool call update.
type ToolContent struct {
	Type    string        `json:"type"` // "content", "diff"
	Content *ContentBlock `json:"content,omitempty"`
	// Diff fields
	Path    string `json:"path,omitempty"`
	OldText string `json:"oldText,omitempty"`
	NewText string `json:"newText,omitempty"`
}

// PlanUpdate is a session update for todo/plan changes.
type PlanUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"` // "plan"
	Entries       []PlanEntry `json:"entries"`
}

// PlanEntry is a single item in a plan update.
type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status"`   // pending, in_progress, completed, cancelled
	Priority string `json:"priority"` // high, medium, low
}

// Location represents a file location.
type Location struct {
	Path string `json:"path"`
}
