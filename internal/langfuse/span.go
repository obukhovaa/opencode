package langfuse

import (
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const maxIOSize = 10 * 1024 // 10KB limit for input/output attributes

// Span wraps an OpenTelemetry span for deferred completion.
// All methods are nil-safe — calling them on a nil Span is a no-op.
type Span struct {
	span trace.Span
}

// End finishes the span, recording its end time.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.span.End()
}

// SetUsage records token usage and cost on a generation span.
func (s *Span) SetUsage(u *Usage) {
	if s == nil || u == nil {
		return
	}
	usageMap := map[string]int64{
		"input": u.Input, "output": u.Output, "total": u.Total,
	}
	if u.CacheRead > 0 {
		usageMap["cache_read"] = u.CacheRead
	}
	if u.CacheCreation > 0 {
		usageMap["cache_creation"] = u.CacheCreation
	}
	usage, _ := json.Marshal(usageMap)
	cost, _ := json.Marshal(map[string]float64{
		"input": u.InputCost, "output": u.OutputCost, "total": u.TotalCost,
	})
	s.span.SetAttributes(
		attribute.String("langfuse.observation.usage_details", string(usage)),
		attribute.String("langfuse.observation.cost_details", string(cost)),
	)
}

// SetCompletionStartTime records when the first token was generated (time-to-first-token).
func (s *Span) SetCompletionStartTime(t time.Time) {
	if s == nil {
		return
	}
	s.span.SetAttributes(
		attribute.String("langfuse.observation.completion_start_time", t.Format(time.RFC3339Nano)),
	)
}

// SetError marks the span as errored with the given error message.
func (s *Span) SetError(err error) {
	if s == nil || err == nil {
		return
	}
	s.span.SetAttributes(
		attribute.String("langfuse.observation.level", "ERROR"),
		attribute.String("langfuse.observation.status_message", err.Error()),
	)
	s.span.SetStatus(codes.Error, err.Error())
}

// SetOutput records the output on the span (truncated to maxIOSize).
func (s *Span) SetOutput(output any) {
	if s == nil {
		return
	}
	str := marshalAny(output)
	s.span.SetAttributes(
		attribute.String("langfuse.observation.output", truncate(str, maxIOSize)),
	)
}

func marshalAny(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case nil:
		return ""
	default:
		data, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprint(val)
		}
		return string(data)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
