package api

// APISession represents a session in the external API format.
type APISession struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parentID,omitempty"`
	Title     string          `json:"title"`
	Directory string          `json:"directory"`
	Time      APISessionTime  `json:"time"`
	Token     APISessionToken `json:"token"`
	Cost      float64         `json:"cost"`
}

// APISessionTime holds created/updated timestamps for a session.
type APISessionTime struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

// APISessionToken holds token usage counts for a session.
type APISessionToken struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

// APISessionStatus represents the current status of a session.
// The field is "type" (not "status") to match the dax opencode SDK schema
// which uses a discriminated union on the "type" field.
type APISessionStatus struct {
	Type string `json:"type"` // "idle", "busy", "retry"
}

// APIMessageInfo holds metadata about a message without its content parts.
type APIMessageInfo struct {
	ID         string           `json:"id"`
	SessionID  string           `json:"sessionID"`
	Role       string           `json:"role"` // "user", "assistant"
	ProviderID string           `json:"providerID,omitempty"`
	ModelID    string           `json:"modelID,omitempty"`
	Tokens     APIMessageTokens `json:"tokens"`
	Cost       float64          `json:"cost"`
	Time       APIMessageTime   `json:"time"`
}

// APIMessageTokens holds token usage breakdown for a message.
type APIMessageTokens struct {
	Input     int64          `json:"input"`
	Output    int64          `json:"output"`
	Reasoning int64          `json:"reasoning"`
	Cache     *APITokenCache `json:"cache,omitempty"`
}

// APITokenCache holds cache read/write token counts.
type APITokenCache struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

// APIMessageTime holds created/updated timestamps for a message.
type APIMessageTime struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

// APIMessageResponse combines message metadata with its content parts.
type APIMessageResponse struct {
	Info  APIMessageInfo `json:"info"`
	Parts []APIPart      `json:"parts"`
}

// APIPart represents a single content part in the external API format.
// The Type field determines which other fields are populated.
// MessageID and SessionID are required by the OpenWork session snapshot schema.
type APIPart struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "text", "tool", "reasoning", "file"
	MessageID string `json:"messageID"`
	SessionID string `json:"sessionID"`

	// For text and reasoning parts
	Text string `json:"text,omitempty"`

	// For tool parts
	Tool   string        `json:"tool,omitempty"`
	CallID string        `json:"callID,omitempty"`
	State  *APIToolState `json:"state,omitempty"`
}

// APIToolState represents the execution state of a tool call.
type APIToolState struct {
	Status   string         `json:"status"` // "pending", "running", "completed", "error"
	Input    map[string]any `json:"input,omitempty"`
	Output   string         `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Title    string         `json:"title,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// APIProvider represents a model provider with its available models.
type APIProvider struct {
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	Source  string                  `json:"source"`
	Env     []string                `json:"env"`
	Options map[string]any          `json:"options"`
	Models  map[string]APIModelInfo `json:"models"`
}

// APIModelInfo describes a single model's capabilities and limits.
type APIModelInfo struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	ProviderID string        `json:"providerID"`
	Limit      APIModelLimit `json:"limit"`
	Attachment bool          `json:"attachment"`
	Reasoning  bool          `json:"reasoning"`
}

// APIModelLimit holds context and output token limits for a model.
type APIModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// APIConfig holds configuration values exposed via the API.
type APIConfig struct {
	Model string `json:"model,omitempty"`
}

// APIEvent represents a server-sent event.
type APIEvent struct {
	Type       string `json:"type"`
	Properties any    `json:"properties"`
}

// APIPermissionRequest is the SDK-facing permission request representation.
type APIPermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionID"`
	ToolName    string `json:"toolName"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params,omitempty"`
	Path        string `json:"path"`
}

// APIPermissionReply is the request body for replying to a permission request.
// Supports two shapes:
//   - Legacy OpenWork: {"allow": true|false}
//   - dax SDK v2 ("permission.reply"): {"reply": "once"|"always"|"reject", "message"?: string}
//
// When Reply is set it takes precedence over Allow. Both shapes route to the
// same permission service methods (Grant/Deny).
type APIPermissionReply struct {
	Allow   *bool  `json:"allow,omitempty"`
	Reply   string `json:"reply,omitempty"`
	Message string `json:"message,omitempty"`
}

// APIPermissionRespond is the request body for the session-scoped
// `POST /session/{sessionID}/permissions/{permissionID}` endpoint (dax SDK
// `permission.respond`). The `response` field has the same semantics as
// APIPermissionReply.Reply.
type APIPermissionRespond struct {
	Response string `json:"response"`
}

// APIQuestionOption represents a single option in a question.
type APIQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// APIQuestionPrompt represents a single question with its options.
type APIQuestionPrompt struct {
	Question string              `json:"question"`
	Options  []APIQuestionOption `json:"options"`
	Multiple bool                `json:"multiple,omitempty"`
	Custom   *bool               `json:"custom,omitempty"`
}

// APIQuestionRequest is the SDK-facing question request representation.
type APIQuestionRequest struct {
	ID        string              `json:"id"`
	SessionID string              `json:"sessionID"`
	Questions []APIQuestionPrompt `json:"questions"`
}

// APIQuestionReply is the request body for replying to a question request.
type APIQuestionReply struct {
	Answers [][]string `json:"answers"`
}

// APIAgent represents an agent in the external API format.
// `Active` is always false for subagents; primary agents have it set
// to true for whichever agent App.ActiveAgent currently returns.
type APIAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode"`
	Model       string `json:"model,omitempty"`
	Active      bool   `json:"active"`
}

// APIAgentSelectRequest is the body for POST /agent/select.
type APIAgentSelectRequest struct {
	ID string `json:"id"`
}

// APIAgentModelSelectRequest is the body for POST /agent/model/select.
type APIAgentModelSelectRequest struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// APIProvidersResponse wraps the provider list returned by GET /config/providers.
type APIProvidersResponse struct {
	Providers []APIProvider `json:"providers"`
	Default   *APIModelInfo `json:"default,omitempty"`
}

// APIPromptRequest is the request body for sending a prompt.
type APIPromptRequest struct {
	Parts []APIPromptPart `json:"parts"`
	Model *APIPromptModel `json:"model,omitempty"`
}

// APIPromptPart represents a part in a prompt request.
// Supports text parts ({type:"text", text:"..."}) and file parts
// ({type:"file", url:"data:...", filename:"...", mime:"image/png"}).
type APIPromptPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	FileName string `json:"filename,omitempty"`
	Mime     string `json:"mime,omitempty"`
}

// APIPromptModel specifies which model to use for a prompt.
type APIPromptModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// APISessionCreateRequest is the request body for creating a session.
type APISessionCreateRequest struct {
	Title      string              `json:"title"`
	Permission []APIPermissionRule `json:"permission,omitempty"`
}

// APISessionUpdateRequest is the request body for updating a session.
// Title is a pointer so a permission-only PATCH does not clobber the existing title.
type APISessionUpdateRequest struct {
	Title      *string             `json:"title,omitempty"`
	Permission []APIPermissionRule `json:"permission,omitempty"`
}

// APIPermissionRule mirrors the dax SDK PermissionRule shape so SDK clients
// can pass through their wildcard-allow rules. Only a single shape is honored
// today (see shouldAutoApprove); other rules are silently ignored.
type APIPermissionRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}
