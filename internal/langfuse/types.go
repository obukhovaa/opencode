package langfuse

// TraceParams holds parameters for starting a root trace span.
type TraceParams struct {
	Name      string
	SessionID string
	UserID    string
	Tags      []string
	Release   string
	Metadata  map[string]any
}

// GenerationParams holds parameters for starting a generation span.
type GenerationParams struct {
	Name     string
	Model    string
	Metadata map[string]any
}

// ToolParams holds parameters for starting a tool call span.
type ToolParams struct {
	Name  string
	Input any
}

// Usage tracks token counts and costs for a generation.
type Usage struct {
	Input      int64
	Output     int64
	Total      int64
	InputCost  float64
	OutputCost float64
	TotalCost  float64
}
