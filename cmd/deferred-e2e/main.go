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
//
// The native (server-side tool search) path is exercised separately by
// scripts/test/deferred_tools_native.sh, which drives the real binary
// against a live Claude model using the ambient ~/.opencode.json config.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	if *mode != "fallback" {
		fmt.Fprintln(os.Stderr, "only -mode fallback is supported here; the native path is exercised by scripts/test/deferred_tools_native.sh against a live model")
		os.Exit(2)
	}
	runFallback()
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
