package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/version"
)

type EventType string

const maxRetries = 8

const defaultStreamInactivityTimeout = 5 * time.Minute

var ErrStreamStalled = errors.New("stream stalled: no events received within timeout")

// isTransientStreamError returns true for errors that indicate the
// stream was interrupted by a recoverable upstream condition (transport
// disconnect, provider-side temporary failure). These are worth retrying
// because they are not application-level rejections (auth, schema,
// content policy).
//
// Two classes are covered:
//
//  1. Transport-level — the HTTP/TCP connection died mid-response. The
//     standard library error sentinels and the well-known driver
//     substrings catch this.
//
//  2. Provider-protocol-level — the upstream sent an in-band error
//     frame in the eventstream/SSE payload. AWS Bedrock's
//     anthropic-sdk-go bedrock decoder wraps these as
//     `fmt.Errorf("received exception <ExceptionType>: <message>")`
//     (see anthropics/anthropic-sdk-go/bedrock/bedrock.go). Only the
//     transient AWS exception types are matched — `ValidationException`
//     / `AccessDeniedException` / `ResourceNotFoundException` /
//     `ModelErrorException` are deliberately omitted because they
//     reflect bad inputs or auth misconfig, retrying would just
//     hammer the same wall.
func isTransientStreamError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return contains(msg,
		// Transport-level
		"connection reset by peer",
		"use of closed network connection",
		"broken pipe",
		"unexpected EOF",
		// Bedrock mid-stream exceptions wrapped as
		// "received exception <Type>: <msg>".
		"ServiceUnavailableException", // 503 — Bedrock unable to process (the canonical "Bedrock is unable to process your request" failure mode)
		"ThrottlingException",         // 429 — Bedrock-side rate limit
		"ModelTimeoutException",       // 408 — model inference timed out upstream
		"ModelStreamErrorException",   // mid-stream error from the model itself, retryable per AWS docs
		"InternalServerException",     // 500 — generic upstream blip
	)
	// HTTP/2 RST_STREAM frames (`stream error: stream ID <N>; <CODE>;
	// received from peer`) are deliberately NOT classified as transient.
	// Proxies like litellm sometimes wrap permanent upstream errors (400
	// invalid_request_error, etc.) as RST_STREAM(INTERNAL_ERROR), and the
	// HTTP/2 frame carries no status code — we can't tell a real proxy
	// blip from a permanent client error. Retrying on these would hammer
	// the same bad request through the full exponential-backoff window
	// (~8.5 min) before giving up, which the user experiences as an
	// indefinite "generating…" hang. Better to fail fast and surface the
	// underlying error.
}

func contains(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

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

	langfuseClient *langfuse.Client

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
		// (e.g., canceled messages that only contain a Finish part, or a
		// whitespace-only TextContent left behind by an aborted stream).
		// The whitespace-trim check must match the provider-side conversion
		// (see internal/llm/provider/anthropic.go — the anthropic path also
		// drops whitespace-only text), otherwise such messages slip past
		// cleanup and trigger a downstream "no content blocks" warn.
		if msg.Role == message.Assistant && strings.TrimSpace(msg.Content().String()) == "" && len(msg.ToolCalls()) == 0 {
			// Info, not Warn: this is a benign path — the row exists because
			// an assistant stream was canceled or aborted before producing
			// any content or tool calls, and the provider APIs reject empty
			// assistant turns. No operator action is needed.
			logging.Info("Skipping assistant message with no content or tool calls (likely canceled)",
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

	lf := p.options.langfuseClient
	var gen *langfuse.Span
	if lf != nil && lf.Enabled() {
		model := p.options.model
		gen = lf.GenerationStart(ctx, langfuse.GenerationParams{
			Name:     langfuse.FormatGenerationName(getAgentIDFromCtx(ctx), string(model.APIModel)),
			Model:    string(model.APIModel),
			Metadata: p.generationMetadata(ctx),
		})
		defer gen.End()
	}

	resp, err := p.client.send(ctx, messages, tools)

	if gen != nil {
		if err != nil {
			gen.SetError(err)
		}
		if resp != nil {
			gen.SetUsage(p.buildUsage(resp.Usage))
		}
	}
	return resp, err
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

	lf := p.options.langfuseClient
	if lf == nil || !lf.Enabled() {
		return p.client.stream(ctx, messages, tools)
	}

	model := p.options.model
	gen := lf.GenerationStart(ctx, langfuse.GenerationParams{
		Name:     langfuse.FormatGenerationName(getAgentIDFromCtx(ctx), string(model.APIModel)),
		Model:    string(model.APIModel),
		Metadata: p.generationMetadata(ctx),
	})

	upstream := p.client.stream(ctx, messages, tools)
	wrapped := make(chan ProviderEvent)

	go func() {
		defer close(wrapped)
		defer gen.End()
		var completionStartTime *time.Time
		var lastErr error

		for event := range upstream {
			// Capture time-to-first-token from the first content delta
			if completionStartTime == nil && (event.Type == EventContentDelta || event.Type == EventThinkingDelta) {
				t := time.Now().UTC()
				completionStartTime = &t
			}
			if event.Type == EventError {
				lastErr = event.Error
			}
			if event.Type == EventComplete && event.Response != nil {
				gen.SetUsage(p.buildUsage(event.Response.Usage))
			}
			select {
			case wrapped <- event:
			case <-ctx.Done():
				// Consumer stopped reading (cancellation/error). Record the
				// generation as an error so it doesn't stay open in Langfuse,
				// then drain upstream to let the provider goroutine exit.
				lastErr = ctx.Err()
				for range upstream {
				}
				goto done
			}
		}
	done:
		if completionStartTime != nil {
			gen.SetCompletionStartTime(*completionStartTime)
		}
		if lastErr != nil {
			gen.SetError(lastErr)
		}
	}()

	return wrapped
}

// localTokenEstimate computes the heuristic token count for the request.
// Unlike some provider count_tokens endpoints, it ALWAYS accounts for the
// tool schemas (via message.EstimateTokens) and the system prompt — the two
// components that dominate a fresh request's footprint. It is a lower-bound
// estimate (the 4-bytes/token heuristic tends to slightly undercount dense
// JSON), which is exactly why it is safe to use as a floor for the endpoint
// result in CountTokens.
func (p *baseProvider[C]) localTokenEstimate(messages []message.Message, tools []toolsPkg.BaseTool) int64 {
	est := message.EstimateTokens(messages, tools, message.BytesPerTokenEta)
	// Account for system message tokens not included in EstimateTokens.
	if p.options.systemMessage != "" {
		est += int64(len(p.options.systemMessage) / message.BytesPerTokenEta)
	}
	return est
}

// reconcileTokenEstimate picks the trustworthy token count between the
// provider's count_tokens endpoint result and the local heuristic estimate.
//
// Some proxies (observed: LiteLLM in front of Bedrock) answer HTTP 200 from
// /count_tokens but silently omit the system prompt AND tool schemas from the
// returned count — undercounting a fresh, tool-heavy request by tens of
// thousands of tokens. Because the loop trusts this value to decide when to
// auto-compact, the truncation makes compaction fire late or never, risking a
// hard context-overflow error mid-run.
//
// The local estimate always includes system + tools, so we take the larger of
// the two as a floor: a healthy endpoint (native Anthropic / Vertex) already
// counts everything and stays >= local, so it wins unchanged; a truncating
// proxy is corrected up to the local estimate.
func reconcileTokenEstimate(endpoint, local int64) int64 {
	if endpoint > local {
		return endpoint
	}
	return local
}

func (p *baseProvider[C]) CountTokens(ctx context.Context, threshold float64, messages []message.Message, tools []toolsPkg.BaseTool) (int64, bool) {
	local := p.localTokenEstimate(messages, tools)
	estimatedTokens := local
	endpointTokens, err := p.client.countTokens(ctx, messages, tools)
	if err != nil {
		// Endpoint unavailable — fall back to the local estimate.
		if !errors.Is(err, context.Canceled) {
			logging.Warn("Provider doesn't support countTokens endpoint, using local strategy for max_tokens", "model", p.options.model.Name, "cause", err.Error())
		}
	} else {
		estimatedTokens = reconcileTokenEstimate(endpointTokens, local)
		// A proxy that returns fewer tokens than the local estimate is almost
		// certainly omitting system + tools from the count (see
		// reconcileTokenEstimate). Warn once per call so the drift is visible
		// without silently under-compacting.
		if endpointTokens < local {
			logging.Warn("count_tokens endpoint returned fewer tokens than the local estimate; using local estimate (endpoint likely omits system prompt and tool schemas)",
				"model", p.options.model.Name,
				"endpoint_tokens", endpointTokens,
				"local_tokens", local,
			)
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
		"endpoint_tokens", endpointTokens,
		"local_tokens", local,
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

// CalculateCost computes input and output costs for the given token usage and model pricing.
// This is the single source of truth for cost calculation across the codebase.
func CalculateCost(model models.Model, u TokenUsage) (inputCost, outputCost float64) {
	inputCost = model.CostPer1MInCached/1e6*float64(u.CacheCreationTokens) +
		model.CostPer1MOutCached/1e6*float64(u.CacheReadTokens) +
		model.CostPer1MIn/1e6*float64(u.InputTokens)
	outputCost = model.CostPer1MOut / 1e6 * float64(u.OutputTokens)
	return
}

// buildUsage converts a ProviderResponse's TokenUsage into a Langfuse Usage struct.
func (p *baseProvider[C]) buildUsage(u TokenUsage) *langfuse.Usage {
	inputCost, outputCost := CalculateCost(p.options.model, u)
	totalInput := u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
	return &langfuse.Usage{
		Input:         totalInput,
		Output:        u.OutputTokens,
		Total:         totalInput + u.OutputTokens,
		CacheRead:     u.CacheReadTokens,
		CacheCreation: u.CacheCreationTokens,
		InputCost:     inputCost,
		OutputCost:    outputCost,
		TotalCost:     inputCost + outputCost,
	}
}

// generationMetadata builds metadata for a Langfuse generation event.
func (p *baseProvider[C]) generationMetadata(ctx context.Context) map[string]any {
	meta := map[string]any{
		"opencode_version": version.Version,
	}
	if agentID := getAgentIDFromCtx(ctx); agentID != "" {
		meta["agent_id"] = agentID
	}
	// Apply metadata namespace prefix when configured.
	if cfg := config.Get(); cfg.Telemetry != nil && cfg.Telemetry.MetadataNamespace != "" {
		meta = langfuse.NamespaceMetadata(meta, cfg.Telemetry.MetadataNamespace)
	}
	return meta
}

// getAgentIDFromCtx reads the agent ID from context using the tools package key.
func getAgentIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(toolsPkg.AgentIDContextKey).(string); ok {
		return v
	}
	return ""
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

func WithLangfuse(client *langfuse.Client) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.langfuseClient = client
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

// GetUserID returns the resolved user ID (from env, config, or auto-generated UUID).
// The value is cached after first resolution.
func GetUserID() string {
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
		if uid := GetUserID(); uid != "" {
			resolved[meta.UserID] = uid
		}
	}
	if meta.Tags != "" {
		tags := ResolveTags(ctx)
		if len(tags) > 0 {
			resolved[meta.Tags] = tags
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// ResolveTags collects tags from config (telemetry.tags) and dynamic context values.
func ResolveTags(ctx context.Context) []string {
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
