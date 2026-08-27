package langfuse

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const maxIOSize = 10 * 1024 // 10KB limit for tool input/output attributes

// maxGenIOSize caps LLM request/response payloads on generation spans and
// trace-level input/output. Deliberately large — the point is to see the
// exact request and response — but bounded so a pathological payload cannot
// blow up the OTLP export or get the whole span rejected by Langfuse's
// per-event ingestion limits.
const maxGenIOSize = 400 * 1024

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
	s.setOutput(output, maxIOSize)
}

// SetGenerationOutput records the LLM response on a generation span.
// Same attribute as SetOutput but with the larger generation payload cap.
func (s *Span) SetGenerationOutput(output any) {
	s.setOutput(output, maxGenIOSize)
}

func (s *Span) setOutput(output any, max int) {
	if s == nil {
		return
	}
	s.span.SetAttributes(
		attribute.String("langfuse.observation.output", truncate(marshalAny(output), max)),
	)
}

func marshalAny(v any) string {
	switch val := v.(type) {
	case string:
		// Sanitize rather than trust the caller: trace input/output are raw
		// user/model strings, and invalid UTF-8 in a proto3 string field makes
		// the OTLP marshal fail — which drops the whole export batch, not just
		// this span. json.Marshal (below) already substitutes U+FFFD itself.
		return strings.ToValidUTF8(val, "\uFFFD")
	case nil:
		return ""
	default:
		data, err := json.Marshal(val)
		if err != nil {
			return strings.ToValidUTF8(fmt.Sprint(val), "\uFFFD")
		}
		return string(data)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Back off to a rune boundary. Slicing mid-rune yields invalid UTF-8, and
	// the OTLP protobuf encoder rejects invalid UTF-8 in a string field —
	// which fails the export of the whole batch, not just this span. Prompt
	// payloads are full of multi-byte runes, so the boundary is easy to hit.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...[truncated]"
}
