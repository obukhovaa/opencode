package langfuse

import "time"

// IngestionRequest is the batch request body for POST /api/public/ingestion.
type IngestionRequest struct {
	Batch []IngestionEvent `json:"batch"`
}

// IngestionEvent wraps a single event in the ingestion batch.
type IngestionEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Body      any       `json:"body"`
}

// Event type constants used in IngestionEvent.Type.
const (
	EventTraceCreate      = "trace-create"
	EventGenerationCreate = "generation-create"
	EventGenerationUpdate = "generation-update"
)

// TraceBody is the body for trace-create events.
type TraceBody struct {
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	SessionID string         `json:"sessionId,omitempty"`
	UserID    string         `json:"userId,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Release   string         `json:"release,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Input     any            `json:"input,omitempty"`
	Output    any            `json:"output,omitempty"`
}

// GenerationBody is the body for generation-create and generation-update events.
type GenerationBody struct {
	ID                  string         `json:"id"`
	TraceID             string         `json:"traceId,omitempty"`
	Name                string         `json:"name,omitempty"`
	Model               string         `json:"model,omitempty"`
	ModelParameters     map[string]any `json:"modelParameters,omitempty"`
	Input               any            `json:"input,omitempty"`
	Output              any            `json:"output,omitempty"`
	StartTime           *time.Time     `json:"startTime,omitempty"`
	EndTime             *time.Time     `json:"endTime,omitempty"`
	CompletionStartTime *time.Time     `json:"completionStartTime,omitempty"`
	Usage               *Usage         `json:"usage,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	Level               string         `json:"level,omitempty"`
	StatusMessage       string         `json:"statusMessage,omitempty"`
}

// Usage tracks token counts and costs for a generation.
type Usage struct {
	Input      int64   `json:"input,omitempty"`
	Output     int64   `json:"output,omitempty"`
	Total      int64   `json:"total,omitempty"`
	Unit       string  `json:"unit"`
	InputCost  float64 `json:"inputCost,omitempty"`
	OutputCost float64 `json:"outputCost,omitempty"`
	TotalCost  float64 `json:"totalCost,omitempty"`
}

// IngestionResponse is the response from POST /api/public/ingestion.
type IngestionResponse struct {
	Successes []IngestionResponseItem `json:"successes"`
	Errors    []IngestionResponseItem `json:"errors"`
}

// IngestionResponseItem is a single item in the ingestion response.
type IngestionResponseItem struct {
	ID      string `json:"id"`
	Status  int    `json:"status"`
	Message string `json:"message,omitempty"`
}
