package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

type EventType string

const maxRetries = 8

const (
	EventContentStart  EventType = "content_start"
	EventToolUseStart  EventType = "tool_use_start"
	EventToolUseDelta  EventType = "tool_use_delta"
	EventToolUseStop   EventType = "tool_use_stop"
	EventContentDelta  EventType = "content_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventContentStop   EventType = "content_stop"
	EventComplete      EventType = "complete"
	EventError         EventType = "error"
	EventWarning       EventType = "warning"
)

type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

type ProviderResponse struct {
	Content      string
	ToolCalls    []message.ToolCall
	Usage        TokenUsage
	FinishReason message.FinishReason
}

type ProviderEvent struct {
	Type EventType

	Content  string
	Thinking string
	Response *ProviderResponse
	ToolCall *message.ToolCall
	Error    error
}

type Provider interface {
	SendMessages(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error)

	StreamResponse(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent

	Model() models.Model

	// Counts tokens for provided messages using underlying client OR fallback to default estimation strategy,
	// returns tokens count and whether a threshold has been hit based on the model context size,
	// threhold can be used to track an approaching limit to trigger compaction or other activities
	CountTokens(ctx context.Context, threshold float64, messages []message.Message, tools []toolsPkg.BaseTool) (tokens int64, hit bool)

	// Calculates and sets new max_tokens if needed to be used by underlying client
	AdjustMaxTokens(estimatedTokens int64) int64
}

type providerClientOptions struct {
	apiKey        string
	model         models.Model
	maxTokens     int64
	systemMessage string
	baseURL       string
	headers       map[string]string

	anthropicOptions []AnthropicOption
	openaiOptions    []OpenAIOption
	geminiOptions    []GeminiOption
	bedrockOptions   []BedrockOption
}

func (opts *providerClientOptions) asHeader() *http.Header {
	header := http.Header{}
	if opts.headers == nil {
		return &header
	}
	for k, v := range opts.headers {
		header.Add(k, v)
	}
	return &header
}

type ProviderClientOption func(*providerClientOptions)

type ProviderClient interface {
	send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error)
	stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent
	countTokens(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) (int64, error)
	maxTokens() int64
	setMaxTokens(maxTokens int64)
}

type baseProvider[C ProviderClient] struct {
	options providerClientOptions
	client  C
}

func NewProvider(providerName models.ModelProvider, opts ...ProviderClientOption) (Provider, error) {
	clientOptions := providerClientOptions{}
	for _, o := range opts {
		o(&clientOptions)
	}
	switch providerName {
	case models.ProviderVertexAI:
		return &baseProvider[VertexAIClient]{
			options: clientOptions,
			client:  newVertexAIClient(clientOptions),
		}, nil
	case models.ProviderAnthropic:
		return &baseProvider[AnthropicClient]{
			options: clientOptions,
			client:  newAnthropicClient(clientOptions),
		}, nil
	case models.ProviderOpenAI:
		return &baseProvider[OpenAIClient]{
			options: clientOptions,
			client:  newOpenAIClient(clientOptions),
		}, nil
	case models.ProviderGemini:
		return &baseProvider[GeminiClient]{
			options: clientOptions,
			client:  newGeminiClient(clientOptions),
		}, nil
	case models.ProviderBedrock:
		return &baseProvider[BedrockClient]{
			options: clientOptions,
			client:  newBedrockClient(clientOptions),
		}, nil
	case models.ProviderLocal:
		if clientOptions.baseURL == "" {
			clientOptions.baseURL = os.Getenv("LOCAL_ENDPOINT")
		}
		return &baseProvider[OpenAIClient]{
			options: clientOptions,
			client:  newOpenAIClient(clientOptions),
		}, nil
	case models.ProviderMock:
		// TODO: implement mock client for test
		panic("not implemented")
	}
	return nil, fmt.Errorf("provider not supported: %s", providerName)
}

func (p *baseProvider[C]) cleanMessages(messages []message.Message) (cleaned []message.Message) {
	for _, msg := range messages {
		// The message has no content parts at all
		if len(msg.Parts) == 0 {
			continue
		}
		// Skip assistant messages that have no text content and no tool calls
		// (e.g., canceled messages that only contain a Finish part)
		if msg.Role == message.Assistant && msg.Content().String() == "" && len(msg.ToolCalls()) == 0 {
			logging.Warn("Skipping assistant message with no content or tool calls (likely canceled)",
				"message_id", msg.ID,
			)
			continue
		}
		cleaned = append(cleaned, msg)
	}
	return
}

// sanitizeToolPairs ensures that tool_use/tool_result message pairs are consistent.
// It handles two cases:
// 1. An Assistant message with tool calls not followed by a Tool message → synthesize error tool results
// 2. A Tool message with tool_result IDs that don't match the preceding Assistant's tool_use IDs → fix the IDs
func (p *baseProvider[C]) sanitizeToolPairs(messages []message.Message) []message.Message {
	var result []message.Message
	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == message.Assistant && len(msg.ToolCalls()) > 0 {
			result = append(result, msg)
			toolCalls := msg.ToolCalls()

			// Check if the next message is a Tool message
			if i+1 < len(messages) && messages[i+1].Role == message.Tool {
				i++
				toolMsg := messages[i]
				toolResults := toolMsg.ToolResults()

				// Build a set of valid tool_use IDs from the assistant message
				validIDs := make(map[string]bool, len(toolCalls))
				for _, tc := range toolCalls {
					validIDs[tc.ID] = true
				}

				// Check if all tool_result IDs are valid
				allValid := true
				for _, tr := range toolResults {
					if !validIDs[tr.ToolCallID] {
						allValid = false
						break
					}
				}

				if allValid {
					result = append(result, toolMsg)
				} else {
					// Fix the tool_result IDs by matching positionally
					logging.Warn("Fixing mismatched tool_result IDs",
						"message_id", toolMsg.ID,
						"tool_call_count", len(toolCalls),
						"tool_result_count", len(toolResults),
					)
					fixedParts := make([]message.ContentPart, 0, len(toolMsg.Parts))
					for _, part := range toolMsg.Parts {
						if tr, ok := part.(message.ToolResult); ok {
							if !validIDs[tr.ToolCallID] {
								// Try to match by position
								resultIdx := -1
								for j, origTR := range toolResults {
									if origTR.ToolCallID == tr.ToolCallID {
										resultIdx = j
										break
									}
								}
								if resultIdx >= 0 && resultIdx < len(toolCalls) {
									tr.ToolCallID = toolCalls[resultIdx].ID
								} else {
									logging.Warn("Dropping unmatched tool result (more results than tool calls)",
										"tool_call_id", tr.ToolCallID,
										"message_id", toolMsg.ID,
									)
									continue
								}
							}
							fixedParts = append(fixedParts, tr)
						} else {
							fixedParts = append(fixedParts, part)
						}
					}
					toolMsg.Parts = fixedParts
					result = append(result, toolMsg)
				}
			} else {
				// No following Tool message — synthesize one
				logging.Warn("Synthesizing missing tool results for orphaned tool_use blocks",
					"message_id", msg.ID,
					"tool_call_count", len(toolCalls),
				)
				parts := make([]message.ContentPart, len(toolCalls))
				for j, tc := range toolCalls {
					parts[j] = message.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Content:    "Tool execution was interrupted",
						IsError:    true,
					}
				}
				result = append(result, message.Message{
					Role:      message.Tool,
					SessionID: msg.SessionID,
					Parts:     parts,
				})
			}
			continue
		}

		// Skip orphaned Tool messages (no preceding Assistant with tool calls)
		if msg.Role == message.Tool && len(msg.ToolResults()) > 0 {
			// Check if previous message in result is an Assistant with tool calls
			hasMatchingAssistant := false
			if len(result) > 0 {
				prev := result[len(result)-1]
				if prev.Role == message.Assistant && len(prev.ToolCalls()) > 0 {
					hasMatchingAssistant = true
				}
			}
			if !hasMatchingAssistant {
				logging.Warn("Skipping orphaned tool result message without preceding assistant tool_use",
					"message_id", msg.ID,
				)
				continue
			}
		}

		result = append(result, msg)
	}
	return result
}

func (p *baseProvider[C]) SendMessages(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error) {
	messages = p.cleanMessages(messages)
	messages = p.sanitizeToolPairs(messages)
	return p.client.send(ctx, messages, tools)
}

func (p *baseProvider[C]) Model() models.Model {
	return p.options.model
}

func (p *baseProvider[C]) StreamResponse(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	messages = p.cleanMessages(messages)
	messages = p.sanitizeToolPairs(messages)
	return p.client.stream(ctx, messages, tools)
}

func (p *baseProvider[C]) CountTokens(ctx context.Context, threshold float64, messages []message.Message, tools []toolsPkg.BaseTool) (int64, bool) {
	estimatedTokens, err := p.client.countTokens(ctx, messages, tools)
	// Fallback to local estimation
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logging.Warn("Provider doesn't support countTokens endpoint, using local strategy for max_tokens", "model", p.options.model.Name, "cause", err.Error())
		}
		estimatedTokens = message.EstimateTokens(messages, tools)
	}
	contextWindow := p.Model().ContextWindow
	if contextWindow <= 0 {
		return estimatedTokens, false
	}
	thresholdAbs := int64(float64(contextWindow) * threshold)
	hitThreshold := estimatedTokens >= thresholdAbs
	logging.Debug("Token estimation for auto-compaction",
		"estimated_tokens", estimatedTokens,
		"threshold", thresholdAbs,
		"context_window", contextWindow,
		"auto-compaction required", hitThreshold,
	)
	return estimatedTokens, hitThreshold
}

func (p *baseProvider[C]) AdjustMaxTokens(estimatedTokens int64) int64 {
	maxTokens := p.client.maxTokens()
	model := p.options.model
	// Safeguard
	if estimatedTokens >= model.ContextWindow {
		logging.Warn(
			"Estimated token count higher than context window, use existing max_tokens",
			"model",
			model.Name,
			"context",
			model.ContextWindow,
			"max_tokens",
			maxTokens,
			"estimated",
			estimatedTokens,
		)
		return 0
	}

	newMaxTokens := maxTokens
	for estimatedTokens+newMaxTokens >= model.ContextWindow {
		newMaxTokens = newMaxTokens / 2
		p.client.setMaxTokens(newMaxTokens)
		if float64(newMaxTokens) < float64(model.ContextWindow)*0.05 {
			logging.Warn(
				"New max_tokens is below 5% of total context, can't shrink further, proceeding",
				"model",
				model.Name,
				"context",
				model.ContextWindow,
				"new_max_tokens",
				newMaxTokens,
				"estimated",
				estimatedTokens,
			)
			break
		}
	}
	if maxTokens != newMaxTokens {
		logging.Info("max_tokens value has changed", "model", model.Name, "old", maxTokens, "new", newMaxTokens)
	}

	return newMaxTokens
}

func WithBaseURL(baseURL string) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.baseURL = baseURL
	}
}

func WithHeaders(headers map[string]string) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.headers = headers
	}
}

func WithAPIKey(apiKey string) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.apiKey = apiKey
	}
}

func WithModel(model models.Model) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.model = model
	}
}

func WithMaxTokens(maxTokens int64) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.maxTokens = maxTokens
	}
}

func WithSystemMessage(systemMessage string) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.systemMessage = systemMessage
	}
}

func WithAnthropicOptions(anthropicOptions ...AnthropicOption) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.anthropicOptions = anthropicOptions
	}
}

func WithOpenAIOptions(openaiOptions ...OpenAIOption) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.openaiOptions = openaiOptions
	}
}

func WithGeminiOptions(geminiOptions ...GeminiOption) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.geminiOptions = geminiOptions
	}
}

func WithBedrockOptions(bedrockOptions ...BedrockOption) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.bedrockOptions = bedrockOptions
	}
}
