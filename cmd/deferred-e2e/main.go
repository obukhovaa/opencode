// deferred-e2e drives the deferred-tools activation paths end-to-end and
// emits a JSON result for scripts/test/deferred_tools*.sh to assert on.
//
// Modes:
//   - fallback (default, hermetic): an in-process mock OpenAI-compatible
//     server records each request's tools array. Asserts the wire contract:
//     request 1 omits the deferred tool and carries toolsearch; after a
//     toolsearch activation, request 2 appends the activated tool after the
//     previously sent tools; a no-deferral toolset serializes exactly its
//     own tools (feature bypass A/B).
//   - native (requires ANTHROPIC_API_KEY): a recording reverse proxy in
//     front of api.anthropic.com captures the real request. Asserts
//     defer_loading + the server tool-search tool are on the wire, and that
//     the model discovered (tool_search result) and invoked the deferred
//     tool within the session.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type result struct {
	OK     bool     `json:"ok"`
	Checks []string `json:"checks"`
	Errors []string `json:"errors"`
}

func (r *result) pass(name string)        { r.Checks = append(r.Checks, name) }
func (r *result) fail(f string, a ...any) { r.Errors = append(r.Errors, fmt.Sprintf(f, a...)) }

func emit(r *result) {
	r.OK = len(r.Errors) == 0
	out, _ := json.Marshal(r)
	fmt.Println(string(out))
	if !r.OK {
		os.Exit(1)
	}
}

type fakeTool struct {
	name, desc string
}

func (f fakeTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        f.name,
		Description: f.desc,
		Parameters:  map[string]any{"query": map[string]any{"type": "string", "description": "input"}},
		Required:    []string{"query"},
	}
}

func (f fakeTool) Run(ctx context.Context, params tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse("sunny, 21C"), nil
}
func (f fakeTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (f fakeTool) IsBaseline() bool                                       { return false }

func main() {
	mode := flag.String("mode", "fallback", "fallback | native")
	flag.Parse()

	tmp, err := os.MkdirTemp("", "deferred-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)
	if _, err := config.Load(tmp, false); err != nil {
		fmt.Fprintln(os.Stderr, "config load:", err)
		os.Exit(2)
	}

	switch *mode {
	case "fallback":
		runFallback()
	case "native":
		runNative()
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", *mode)
		os.Exit(2)
	}
}

// toolNamesFromRequest extracts function names from an OpenAI-compatible
// chat.completions request body.
func toolNamesFromRequest(body map[string]any) []string {
	var names []string
	rawTools, _ := body["tools"].([]any)
	for _, rt := range rawTools {
		if m, ok := rt.(map[string]any); ok {
			if fn, ok := m["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					names = append(names, n)
				}
			}
		}
	}
	return names
}

func runFallback() {
	r := &result{}
	var mu sync.Mutex
	var requests [][]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, toolNamesFromRequest(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	prov, err := provider.NewProvider(models.ProviderOpenAI,
		provider.WithAPIKey("test-key"),
		provider.WithBaseURL(srv.URL),
		provider.WithModel(models.Model{ID: "test", APIModel: "gpt-test", Provider: models.ProviderOpenAI, DefaultMaxTokens: 512}),
		provider.WithSystemMessage("e2e"),
		provider.WithMaxTokens(512),
	)
	if err != nil {
		r.fail("provider: %v", err)
		emit(r)
	}

	seq := &atomic.Int64{}
	deferred := tools.WrapDeferred(fakeTool{name: "mcp_weather_lookup", desc: "Look up the weather for a city"}, seq)
	search := tools.NewToolSearchTool()
	toolset := []tools.BaseTool{fakeTool{name: "plain_tool", desc: "always loaded"}, search, deferred}
	search.BindToolset(toolset)

	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "e2e-session")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "e2e-msg")
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}

	// Request 1: deferred tool must be omitted; toolsearch present.
	if _, err := prov.SendMessages(ctx, msgs, toolset); err != nil {
		r.fail("send 1: %v", err)
		emit(r)
	}
	if got := requests[0]; strings.Join(got, ",") == "plain_tool,toolsearch" {
		r.pass("request1_omits_deferred_and_has_toolsearch")
	} else {
		r.fail("request1 tools = %v, want [plain_tool toolsearch]", requests[0])
	}

	// Activate via a real toolsearch call.
	input, _ := json.Marshal(map[string]string{"query": "weather lookup"})
	resp, err := search.Run(ctx, tools.ToolCall{Name: tools.ToolSearchToolName, Input: string(input)})
	if err != nil || !strings.Contains(resp.Content, "mcp_weather_lookup") {
		r.fail("toolsearch activation failed: err=%v content=%.120s", err, resp.Content)
		emit(r)
	}
	r.pass("toolsearch_returned_contract")

	// Request 2: activated tool appended AFTER the previously sent tools.
	if _, err := prov.SendMessages(ctx, msgs, toolset); err != nil {
		r.fail("send 2: %v", err)
		emit(r)
	}
	if got := strings.Join(requests[1], ","); got == "plain_tool,toolsearch,mcp_weather_lookup" {
		r.pass("request2_appends_activated_after_prefix")
	} else {
		r.fail("request2 tools = %v, want prefix-then-activated", requests[1])
	}

	// A/B: a toolset with no wrappers serializes exactly itself (feature
	// bypass — the expected list IS the pre-feature payload).
	plainSet := []tools.BaseTool{fakeTool{name: "alpha", desc: "a"}, fakeTool{name: "beta", desc: "b"}}
	if _, err := prov.SendMessages(ctx, msgs, plainSet); err != nil {
		r.fail("send 3: %v", err)
		emit(r)
	}
	if got := strings.Join(requests[2], ","); got == "alpha,beta" {
		r.pass("no_deferral_payload_identical_to_bypass")
	} else {
		r.fail("request3 tools = %v, want [alpha beta]", requests[2])
	}

	emit(r)
}

func runNative() {
	r := &result{}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set")
		os.Exit(3) // script maps to SKIP
	}

	// Recording reverse proxy in front of the real API.
	target, _ := url.Parse("https://api.anthropic.com")
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}
	var mu sync.Mutex
	var recorded []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		recorded = append(recorded, string(body))
		mu.Unlock()
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		rp.ServeHTTP(w, req)
	}))
	defer proxy.Close()

	model := models.SupportedModels[models.Claude45Haiku]
	prov, err := provider.NewProvider(models.ProviderAnthropic,
		provider.WithAPIKey(apiKey),
		provider.WithBaseURL(proxy.URL),
		provider.WithModel(model),
		provider.WithSystemMessage("You are a test agent. Tools may be deferred; use tool search to load them."),
		provider.WithMaxTokens(1024),
	)
	if err != nil {
		r.fail("provider: %v", err)
		emit(r)
	}

	seq := &atomic.Int64{}
	deferred := tools.WrapDeferred(fakeTool{name: "get_paris_weather", desc: "Get the current weather in Paris. The only way to answer weather questions."}, seq)
	toolset := []tools.BaseTool{tools.NewToolSearchTool(), deferred}

	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "e2e-native")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "e2e-msg")
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{
		Text: "What is the weather in Paris right now? You MUST use the get_paris_weather tool (search for it first if it is not loaded).",
	}}}}

	resp, err := prov.SendMessages(ctx, msgs, toolset)
	if err != nil {
		r.fail("send: %v", err)
		emit(r)
	}

	mu.Lock()
	wire := strings.Join(recorded, "\n")
	mu.Unlock()
	if strings.Contains(wire, `"defer_loading":true`) {
		r.pass("request_carries_defer_loading")
	} else {
		r.fail("no defer_loading:true on the wire")
	}
	if strings.Contains(wire, "tool_search_tool_regex") {
		r.pass("request_carries_server_tool_search")
	} else {
		r.fail("server tool_search tool missing from request")
	}
	if len(resp.ToolSearches) > 0 {
		r.pass("model_ran_server_tool_search")
	} else {
		r.fail("response carried no tool_search invocations")
	}
	invoked := false
	for _, tc := range resp.ToolCalls {
		if tc.Name == "get_paris_weather" {
			invoked = true
		}
	}
	if invoked {
		r.pass("deferred_tool_invoked_after_discovery")
	} else {
		r.fail("deferred tool was not invoked (tool calls: %d)", len(resp.ToolCalls))
	}

	emit(r)
}
