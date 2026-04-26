package langfuse

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/version"
)

const (
	defaultBaseURL = "https://cloud.langfuse.com"
	tracerName     = "opencode"
)

// Client wraps an OpenTelemetry TracerProvider configured to export
// spans to Langfuse via the OTLP HTTP endpoint.
type Client struct {
	tracer   trace.Tracer
	provider *sdktrace.TracerProvider
	enabled  bool
}

// New creates a new Langfuse client backed by the OTEL OTLP HTTP exporter.
// Keys and baseURL are resolved from the provided values, falling back
// to environment variables.
func New(publicKey, secretKey, baseURL string) *Client {
	pk := resolveKey(publicKey, "LANGFUSE_PUBLIC_KEY")
	sk := resolveKey(secretKey, "LANGFUSE_SECRET_KEY")
	if pk == "" || sk == "" {
		return &Client{enabled: false}
	}

	bu := resolveKey(baseURL, "LANGFUSE_BASE_URL")
	if bu == "" {
		bu = defaultBaseURL
	}
	bu = strings.TrimRight(bu, "/")

	authStr := base64.StdEncoding.EncodeToString([]byte(pk + ":" + sk))

	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(bu+"/api/public/otel/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization":                "Basic " + authStr,
			"x-langfuse-ingestion-version": "4",
		}),
	)
	if err != nil {
		logging.Warn("langfuse: failed to create OTEL exporter", "error", err)
		return &Client{enabled: false}
	}

	res, _ := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(tracerName),
			semconv.ServiceVersion(version.Version),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)),
		sdktrace.WithResource(res),
	)

	return &Client{
		tracer:   tp.Tracer(tracerName),
		provider: tp,
		enabled:  true,
	}
}

// Enabled returns true if the client has valid credentials and a working exporter.
func (c *Client) Enabled() bool {
	return c != nil && c.enabled
}

// Shutdown flushes all pending spans and stops the exporter.
// Blocks until complete or timeout. Safe to call on a nil/disabled client.
func (c *Client) Shutdown() {
	if c == nil || c.provider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.provider.Shutdown(ctx); err != nil {
		logging.Warn("langfuse: shutdown error", "error", err)
	}
}

// TraceStart begins a root span representing a Langfuse trace.
// The returned context carries the root span for child span creation.
// Call TraceEnd (or EndTrace) when the agent turn completes.
func (c *Client) TraceStart(ctx context.Context, params TraceParams) context.Context {
	if !c.Enabled() {
		return ctx
	}

	attrs := []attribute.KeyValue{
		attribute.String("langfuse.trace.name", params.Name),
	}
	if params.SessionID != "" {
		attrs = append(attrs, attribute.String("langfuse.session.id", params.SessionID))
	}
	if params.UserID != "" {
		attrs = append(attrs, attribute.String("langfuse.user.id", params.UserID))
	}
	if len(params.Tags) > 0 {
		attrs = append(attrs, attribute.StringSlice("langfuse.trace.tags", params.Tags))
	}
	if params.Release != "" {
		attrs = append(attrs, attribute.String("langfuse.release", params.Release))
	}
	for k, v := range params.Metadata {
		attrs = append(attrs, attribute.String("langfuse.trace.metadata."+k, fmt.Sprint(v)))
	}

	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	if !params.IsChild {
		opts = append(opts, trace.WithNewRoot())
	}
	ctx, span := c.tracer.Start(ctx, params.Name, opts...)
	return withRootSpan(ctx, span)
}

// TraceEnd ends the root trace span stored in context.
// No-op if context has no root span.
func (c *Client) TraceEnd(ctx context.Context) {
	if span := getRootSpan(ctx); span != nil {
		span.End()
	}
}

// GenerationStart begins a generation observation as a child of the root trace span.
// Returns a Span handle; call Span.End() when the LLM call completes.
func (c *Client) GenerationStart(ctx context.Context, params GenerationParams) *Span {
	if !c.Enabled() {
		return nil
	}

	// Always parent to root span so generations are siblings, not nested
	parentCtx := ctx
	if root := getRootSpan(ctx); root != nil {
		parentCtx = trace.ContextWithSpan(ctx, root)
	}

	attrs := []attribute.KeyValue{
		attribute.String("langfuse.observation.type", "generation"),
	}
	if params.Model != "" {
		attrs = append(attrs, attribute.String("langfuse.observation.model.name", params.Model))
		attrs = append(attrs, attribute.String("gen_ai.request.model", params.Model))
	}
	for k, v := range params.Metadata {
		attrs = append(attrs, attribute.String("langfuse.observation.metadata."+k, fmt.Sprint(v)))
	}

	_, span := c.tracer.Start(parentCtx, params.Name, trace.WithAttributes(attrs...))
	return &Span{span: span}
}

// ToolStart begins a tool call observation as a child of the root trace span.
// Returns a Span handle; call Span.End() when the tool execution completes.
func (c *Client) ToolStart(ctx context.Context, params ToolParams) *Span {
	if !c.Enabled() {
		return nil
	}

	parentCtx := ctx
	if root := getRootSpan(ctx); root != nil {
		parentCtx = trace.ContextWithSpan(ctx, root)
	}

	attrs := []attribute.KeyValue{
		attribute.String("langfuse.observation.type", "tool"),
	}
	if params.Input != nil {
		inputStr := marshalAny(params.Input)
		attrs = append(attrs, attribute.String("langfuse.observation.input", truncate(inputStr, maxIOSize)))
	}

	_, span := c.tracer.Start(parentCtx, params.Name, trace.WithAttributes(attrs...))
	return &Span{span: span}
}

// Nop returns a disabled client that discards all events.
func Nop() *Client {
	return &Client{enabled: false}
}

// --- Global singleton ---

var globalClient *Client

// Init creates the global Langfuse client. Should be called once at startup.
// Returns true if the client is enabled.
func Init(publicKey, secretKey, baseURL string) bool {
	globalClient = New(publicKey, secretKey, baseURL)
	return globalClient.Enabled()
}

// Get returns the global Langfuse client, or nil if not initialized.
func Get() *Client {
	return globalClient
}

// ShutdownGlobal shuts down the global client, flushing remaining spans.
func ShutdownGlobal() {
	if globalClient != nil {
		globalClient.Shutdown()
	}
}

// EndTrace is a convenience function that ends the root trace span in context.
// Safe to call even when Langfuse is not initialized or context has no span.
func EndTrace(ctx context.Context) {
	if span := getRootSpan(ctx); span != nil {
		span.End()
	}
}

// FormatGenerationName builds a generation name like "coder/claude-sonnet-4-6".
func FormatGenerationName(agentID, model string) string {
	return fmt.Sprintf("%s/%s", agentID, model)
}

// resolveKey resolves a config value: supports "env:VAR_NAME" syntax,
// falls back to the given environment variable, or returns the raw value.
func resolveKey(value, envFallback string) string {
	if value == "" {
		return os.Getenv(envFallback)
	}
	if after, ok := strings.CutPrefix(value, "env:"); ok {
		return os.Getenv(after)
	}
	return value
}
