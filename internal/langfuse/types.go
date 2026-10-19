package langfuse

// TraceParams holds parameters for starting a root trace span.
type TraceParams struct {
	Name      string
	SessionID string
	UserID    string
	Tags      []string
	Release   string
	Metadata  map[string]any
	// Input is the initial input of the trace (e.g. the user message that
	// started the agent turn). Rendered as the trace-level input in Langfuse.
	Input any
	// IsChild indicates this trace is a subagent invoked via the task tool.
	// When false, the span is forced to be a new root (independent trace ID).
	// When true and ctx carries the calling run's trace, the span inherits
	// its trace ID and nests under it as an observation: its name, input,
	// output and metadata are written as observation attributes, never over
	// the parent trace's. When true but ctx carries no trace (a subagent run
	// on a detached ctx), the span is the root of a trace of its own.
	IsChild bool
}

// GenerationParams holds parameters for starting a generation span.
type GenerationParams struct {
	Name     string
	Model    string
	Metadata map[string]any
	// Input is the full request payload sent to the LLM (system prompt +
	// message history). Rendered as the generation's input in Langfuse.
	Input any
}

// ToolParams holds parameters for starting a tool call span.
type ToolParams struct {
	Name  string
	Input any
}

// Usage tracks token counts and costs for a generation.
type Usage struct {
	Input         int64
	Output        int64
	Total         int64
	CacheRead     int64
	CacheCreation int64
	// Thinking is the part of Output spent on reasoning; already inside
	// Output and Total, so it is reported as a breakdown, never summed.
	Thinking   int64
	InputCost  float64
	OutputCost float64
	TotalCost  float64
}
