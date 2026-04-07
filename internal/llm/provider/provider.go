package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

type EventType string

const maxRetries = 8

const defaultStreamInactivityTimeout = 5 * time.Minute

var ErrStreamStalled = errors.New("stream stalled: no events received within timeout")

func resolveStreamInactivityTimeout() time.Duration {
	if envVal := os.Getenv("OPENCODE_PROVIDER_STREAM_INACTIVITY_TIMEOUT"); envVal != "" {
		if secs, err := strconv.Atoi(envVal); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		logging.Warn("Invalid OPENCODE_PROVIDER_STREAM_INACTIVITY_TIMEOUT value, using default",
			"value", envVal, "default", defaultStreamInactivityTimeout)
	}
	return defaultStreamInactivityTimeout
}

var streamInactivityTimeout = resolveStreamInactivityTimeout()

// streamReader wraps a blocking .Next()-style iterator with an inactivity timeout.
// It spawns a single goroutine that feeds events into a channel. The caller reads
// via Recv() with a timeout select. Call Close() to release the underlying stream
// and stop the background goroutine.
type streamReader[T any] struct {
	ch      chan T
	ctx     context.Context
	cancel  context.CancelFunc
	timer   *time.Timer
	release func()
}

// newStreamReader starts a background goroutine that calls nextFn repeatedly
// and sends each value to an internal channel. closeFn is called to release
// the underlying stream resources — it must unblock a currently-blocked nextFn
// call (e.g. by closing the HTTP response body or cancelling the stream context).
// closeFn is guaranteed to be called exactly once regardless of how many times
// Close() is called or whether the goroutine exits naturally.
func newStreamReader[T any](ctx context.Context, nextFn func() (T, bool), closeFn func()) *streamReader[T] {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan T, 1)
	var once sync.Once
	release := func() { once.Do(closeFn) }
	go func() {
		defer release()
		defer close(ch)
		for {
			val, ok := nextFn()
			if !ok {
				return
			}
			select {
			case ch <- val:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &streamReader[T]{
		ch:      ch,
		ctx:     ctx,
		cancel:  cancel,
		timer:   time.NewTimer(streamInactivityTimeout),
		release: release,
	}
}

// Recv waits for the next value from the stream. Returns the value and true,
// or the zero value and false if the stream ended, timed out, or was cancelled.
// On timeout it returns ErrStreamStalled via the error return.
func (r *streamReader[T]) Recv() (T, bool, error) {
	select {
	case val, ok := <-r.ch:
		if !ok {
			var zero T
			return zero, false, nil
		}
		if !r.timer.Stop() {
			select {
			case <-r.timer.C:
			default:
			}
		}
		r.timer.Reset(streamInactivityTimeout)
		return val, true, nil
	case <-r.timer.C:
		var zero T
		return zero, false, ErrStreamStalled
	case <-r.ctx.Done():
		var zero T
		return zero, false, r.ctx.Err()
	}
}

// Close releases the underlying stream, unblocking any in-progress nextFn call,
// cancels the background goroutine, and stops the inactivity timer.
// Safe to call multiple times.
func (r *streamReader[T]) Close() {
	r.timer.Stop()
	r.release()
	r.cancel()
}

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
	metadata      *config.ProviderMetadata

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
	case models.ProviderYandexCloud:
		if clientOptions.baseURL == "" {
			clientOptions.baseURL = "https://llm.api.cloud.yandex.net/v1"
		}
		folderID := os.Getenv("YANDEXCLOUD_FOLDER_ID")
		if folderID == "" {
			return nil, fmt.Errorf("YandexCloud provider requires folder_id — set YANDEXCLOUD_FOLDER_ID env var")
		}
		clientOptions.model.APIModel = "gpt://" + folderID + "/" + clientOptions.model.APIModel
		logging.Info("YandexCloud provider models resolved", "api_model", clientOptions.model.APIModel)
		return &baseProvider[OpenAIClient]{
			options: clientOptions,
			client:  newOpenAIClient(clientOptions),
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
// With seq-based ordering, messages are guaranteed to be in correct order.
// This function handles crash recovery and proxy ID rewrite:
// 1. An Assistant message with tool calls not followed by a Tool message → synthesize error tool results
// 2. Incomplete tool results (some tool_use IDs missing) → synthesize missing ones
// 3. Mismatched tool_result IDs (proxy rewrite) → fix by positional match
// 4. Orphaned tool result messages → skip
func (p *baseProvider[C]) sanitizeToolPairs(messages []message.Message) []message.Message {
	var result []message.Message
	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == message.Assistant && len(msg.ToolCalls()) > 0 {
			result = append(result, msg)
			toolCalls := msg.ToolCalls()

			if i+1 < len(messages) && messages[i+1].Role == message.Tool {
				i++
				toolMsg := messages[i]
				toolResults := toolMsg.ToolResults()

				validIDs := make(map[string]bool, len(toolCalls))
				for _, tc := range toolCalls {
					validIDs[tc.ID] = true
				}

				resultIDs := make(map[string]bool, len(toolResults))
				allValid := true
				for _, tr := range toolResults {
					if !validIDs[tr.ToolCallID] {
						allValid = false
						break
					}
					resultIDs[tr.ToolCallID] = true
				}

				allComplete := allValid
				if allValid {
					for _, tc := range toolCalls {
						if !resultIDs[tc.ID] {
							allComplete = false
							break
						}
					}
				}

				if allComplete {
					result = append(result, toolMsg)
				} else if allValid {
					logging.Warn("Synthesizing missing tool results for incomplete tool_result set",
						"message_id", toolMsg.ID,
						"tool_call_count", len(toolCalls),
						"tool_result_count", len(toolResults),
					)
					fixedParts := make([]message.ContentPart, 0, len(toolMsg.Parts)+len(toolCalls))
					fixedParts = append(fixedParts, toolMsg.Parts...)
					for _, tc := range toolCalls {
						if !resultIDs[tc.ID] {
							fixedParts = append(fixedParts, message.ToolResult{
								ToolCallID: tc.ID,
								Name:       tc.Name,
								Content:    "Tool execution was interrupted",
								IsError:    true,
							})
						}
					}
					toolMsg.Parts = fixedParts
					result = append(result, toolMsg)
				} else {
					logging.Warn("Fixing mismatched tool_result IDs",
						"message_id", toolMsg.ID,
						"tool_call_count", len(toolCalls),
						"tool_result_count", len(toolResults),
					)
					fixedParts := make([]message.ContentPart, 0, len(toolMsg.Parts))
					for _, part := range toolMsg.Parts {
						if tr, ok := part.(message.ToolResult); ok {
							if !validIDs[tr.ToolCallID] {
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
									logging.Warn("Dropping unmatched tool result",
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

		if msg.Role == message.Tool && len(msg.ToolResults()) > 0 {
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

// StreamToResponse consumes a StreamResponse channel and collects the result
// into a single ProviderResponse. Use this instead of SendMessages when the
// request payload is large and providers may reject non-streaming calls
// (e.g. Anthropic rejects non-streaming requests that could take >10 minutes).
func StreamToResponse(events <-chan ProviderEvent) (*ProviderResponse, error) {
	for event := range events {
		switch event.Type {
		case EventError:
			return nil, event.Error
		case EventComplete:
			return event.Response, nil
		}
	}
	return nil, fmt.Errorf("stream ended without a complete event")
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
		estimatedTokens = message.EstimateTokens(messages, tools, message.BytesPerTokenEta)
		// Account for system message tokens not included in EstimateTokens
		if p.options.systemMessage != "" {
			estimatedTokens += int64(len(p.options.systemMessage) / message.BytesPerTokenEta)
		}
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

func WithMetadata(metadata *config.ProviderMetadata) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.metadata = metadata
	}
}

var processUserID = func() string {
	if id := os.Getenv("OPENCODE_USER_ID"); id != "" {
		return id
	}
	cfg := config.Get()
	if cfg != nil && cfg.Telemetry != nil && cfg.Telemetry.UserID != "" {
		return cfg.Telemetry.UserID
	}
	return uuid.New().String()
}

var (
	resolvedUserID string
	userIDOnce     sync.Once
)

func getUserID() string {
	userIDOnce.Do(func() {
		resolvedUserID = processUserID()
	})
	return resolvedUserID
}

func resolveMetadata(ctx context.Context, meta *config.ProviderMetadata) map[string]any {
	if meta == nil {
		return nil
	}
	resolved := make(map[string]any)
	if meta.SessionID != "" {
		if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok && sid != "" {
			resolved[meta.SessionID] = sid
		}
	}
	if meta.UserID != "" {
		if uid := getUserID(); uid != "" {
			resolved[meta.UserID] = uid
		}
	}
	if meta.Tags != "" {
		tags := resolveTags(ctx)
		if len(tags) > 0 {
			resolved[meta.Tags] = tags
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

func resolveTags(ctx context.Context) []string {
	var tags []string
	cfg := config.Get()
	if cfg != nil && cfg.Telemetry != nil {
		tags = append(tags, cfg.Telemetry.Tags...)
	}
	if dynamic, ok := ctx.Value(toolsPkg.MetadataTagsContextKey).([]string); ok {
		tags = append(tags, dynamic...)
	}
	return tags
}
