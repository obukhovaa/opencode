package provider

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"

	"github.com/openai/openai-go"
	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/message"
)

var scalarReasoningFields = [...]string{"reasoning_content", "reasoning"}

type scalarReasoningCapture struct {
	text    [2]string
	invalid [2]bool
}

func (c *scalarReasoningCapture) add(raw string) string {
	var preview strings.Builder
	for i, field := range scalarReasoningFields {
		value := gjson.Get(raw, field)
		if value.Type == gjson.String {
			c.text[i] += value.String()
			preview.WriteString(value.String())
		} else if value.Exists() && value.Type != gjson.Null {
			// An opaque value in any fragment makes the entire field unsuitable
			// for scalar replay, even if earlier fragments contained strings.
			c.invalid[i] = true
		}
	}
	return preview.String()
}

func (o *openaiClient) scalarOrigin(field string) *message.ScalarReasoningOrigin {
	opts := o.providerOptions
	endpoint, err := url.Parse(opts.baseURL)
	if err != nil || endpoint.Host == "" || opts.model.Provider == "" || opts.model.ID == "" || opts.model.APIModel == "" {
		return nil
	}
	// First-party Chat Completions has no scalar reasoning replay contract.
	if strings.EqualFold(endpoint.Hostname(), "api.openai.com") {
		return nil
	}
	route := fmt.Sprintf("openai-chat-completions:%t:%s", o.options.useBedrock, strings.TrimRight(endpoint.String(), "/"))
	return &message.ScalarReasoningOrigin{
		Field: field, Provider: opts.model.Provider, Model: opts.model.ID,
		APIModel: opts.model.APIModel, Route: fmt.Sprintf("%x", sha256.Sum256([]byte(route))),
	}
}

func (o *openaiClient) scalarReasoning(raw string) []message.ReasoningContent {
	var capture scalarReasoningCapture
	capture.add(raw)
	return o.scalarReasoningParts(capture)
}

func (o *openaiClient) scalarReasoningParts(capture scalarReasoningCapture) []message.ReasoningContent {
	var parts []message.ReasoningContent
	for i, field := range scalarReasoningFields {
		if capture.text[i] == "" || capture.invalid[i] {
			continue
		}
		if origin := o.scalarOrigin(field); origin != nil {
			parts = append(parts, message.ReasoningContent{Thinking: capture.text[i], Scalar: origin})
		}
	}
	return parts
}

func (o *openaiClient) completedScalarReasoning(capture scalarReasoningCapture, completion openai.ChatCompletion) []message.ReasoningContent {
	// A closed SSE connection is not proof that the model finished. Keep
	// truncated previews display-only, as with canceled streams.
	if len(completion.Choices) == 0 || completion.Choices[0].FinishReason == "" {
		return nil
	}
	return o.scalarReasoningParts(capture)
}

func (o *openaiClient) reasoningReplay(msg message.Message) map[string]any {
	var fields map[string]any
	for _, part := range msg.ReasoningParts() {
		if part.Scalar == nil || part.Thinking == "" || part.Signature != "" || part.Redacted || part.Data != "" {
			continue
		}
		for _, field := range scalarReasoningFields {
			if part.Scalar.Field != field {
				continue
			}
			origin := o.scalarOrigin(field)
			if origin == nil || *origin != *part.Scalar {
				continue
			}
			if fields == nil {
				fields = make(map[string]any)
			}
			previous, _ := fields[field].(string)
			fields[field] = previous + part.Thinking
		}
	}
	return fields
}
