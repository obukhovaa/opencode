package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// serverToolUseBlock builds the replay shape convertMessages emits for a
// server-side tool search (see anthropicClient.convertMessages).
func serverToolUseBlock(id, name, pattern string) anthropic.ContentBlockParamUnion {
	return anthropic.NewServerToolUseBlock(
		id,
		map[string]any{"pattern": pattern},
		anthropic.ServerToolUseBlockParamName(name),
	)
}

func toolSearchResultBlock(id string, refs ...string) anthropic.ContentBlockParamUnion {
	blocks := make([]anthropic.ToolReferenceBlockParam, 0, len(refs))
	for _, r := range refs {
		blocks = append(blocks, anthropic.ToolReferenceBlockParam{ToolName: r})
	}
	return anthropic.NewToolSearchToolResultBlock(
		anthropic.ToolSearchToolSearchResultBlockParam{ToolReferences: blocks},
		id,
	)
}

func toolSearchErrorBlock(id, code string) anthropic.ContentBlockParamUnion {
	return anthropic.NewToolSearchToolResultBlock(
		anthropic.ToolSearchToolResultErrorParam{ErrorCode: anthropic.ToolSearchToolResultErrorCode(code)},
		id,
	)
}

// TestStripServerToolBlocksForCountTokens locks in the LiteLLM workaround
// for native tool search: once a session runs a server-side search,
// convertMessages replays server_tool_use / tool_search_tool_result blocks on
// EVERY request, and LiteLLM's token counter 500s on them ("Invalid content
// item type: server_tool_use"). They must be re-inlined as text before the
// count_tokens call, with the query and discovered tool names preserved so
// the endpoint still counts the payload.
func TestStripServerToolBlocksForCountTokens(t *testing.T) {
	tests := []struct {
		name            string
		messages        []anthropic.MessageParam
		wantExtraTokens int64
		wantSubstrings  []string
	}{
		{
			name: "search + references pair re-inlined as text",
			messages: []anthropic.MessageParam{
				anthropic.NewAssistantMessage(
					serverToolUseBlock("srvtoolu_1", "tool_search_tool_regex", "gitlab_.*"),
					toolSearchResultBlock("srvtoolu_1", "gitlab_get_merge_request", "gitlab_list_commits"),
					anthropic.NewTextBlock("on it"),
				),
			},
			wantExtraTokens: 2 * countTokensServerToolBlockOverhead,
			wantSubstrings: []string{
				"tool_search_tool_regex",
				"gitlab_.*",
				"gitlab_get_merge_request",
				"gitlab_list_commits",
			},
		},
		{
			name: "errored search keeps its error code",
			messages: []anthropic.MessageParam{
				anthropic.NewAssistantMessage(
					serverToolUseBlock("srvtoolu_1", "tool_search_tool_bm25", "jira"),
					toolSearchErrorBlock("srvtoolu_1", "too_many_requests"),
				),
			},
			wantExtraTokens: 2 * countTokensServerToolBlockOverhead,
			wantSubstrings:  []string{"tool_search_tool_bm25", "too_many_requests"},
		},
		{
			name: "search with no references still yields non-empty content",
			messages: []anthropic.MessageParam{
				anthropic.NewAssistantMessage(
					serverToolUseBlock("srvtoolu_1", "tool_search_tool_regex", "nothing"),
					toolSearchResultBlock("srvtoolu_1"),
				),
			},
			wantExtraTokens: 2 * countTokensServerToolBlockOverhead,
			wantSubstrings:  []string{"tool_search_tool_regex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, extra := stripServerToolBlocksForCountTokens(tt.messages)
			if extra != tt.wantExtraTokens {
				t.Errorf("extra tokens = %d, want %d", extra, tt.wantExtraTokens)
			}
			var text strings.Builder
			for i, msg := range out {
				if len(msg.Content) == 0 {
					t.Errorf("message[%d] has empty content — the API rejects that", i)
				}
				for j, block := range msg.Content {
					if block.OfServerToolUse != nil {
						t.Errorf("message[%d].Content[%d] still has OfServerToolUse", i, j)
					}
					if block.OfToolSearchToolResult != nil {
						t.Errorf("message[%d].Content[%d] still has OfToolSearchToolResult", i, j)
					}
					if block.OfText != nil {
						if block.OfText.Text == "" {
							t.Errorf("message[%d].Content[%d] is an empty text block — the API rejects that", i, j)
						}
						text.WriteString(block.OfText.Text)
						text.WriteString("\n")
					}
				}
			}
			for _, want := range tt.wantSubstrings {
				if !strings.Contains(text.String(), want) {
					t.Errorf("stand-in text missing %q; got:\n%s", want, text.String())
				}
			}
			// The caller's slice must be untouched — the real blocks are still
			// needed for the actual /v1/messages request.
			for _, msg := range tt.messages {
				var sawServerTool bool
				for _, block := range msg.Content {
					if block.OfServerToolUse != nil || block.OfToolSearchToolResult != nil {
						sawServerTool = true
					}
				}
				if !sawServerTool {
					t.Error("input mutated: server-side tool blocks gone from the original messages")
				}
			}
		})
	}
}

// TestStripServerToolBlocksFastPath guards the fast path: an
// ordinary client-side tool_use/tool_result exchange carries none of the
// rejected block types, so the input slice is returned as-is.
func TestStripServerToolBlocksFastPath(t *testing.T) {
	messages := []anthropic.MessageParam{
		anthropic.NewAssistantMessage(
			anthropic.NewTextBlock("calling a tool"),
			anthropic.NewToolUseBlock("toolu_1", map[string]any{"path": "x.go"}, "view"),
		),
		anthropic.NewUserMessage(anthropic.NewToolResultBlock("toolu_1", "file contents", false)),
	}
	out, extra := stripServerToolBlocksForCountTokens(messages)
	if extra != 0 {
		t.Errorf("extra tokens = %d, want 0", extra)
	}
	if len(out) != len(messages) {
		t.Fatalf("len = %d, want %d", len(out), len(messages))
	}
}

// countTokensTestServer spins up an Anthropic-dialect count_tokens endpoint
// that records each request body and answers with the given status/payload.
func countTokensTestServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32, func() string) {
	t.Helper()
	var hits atomic.Int32
	var last atomic.Value
	last.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		last.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, func() string { return last.Load().(string) }
}

// toolSearchTurn is the assistant turn a native tool-search session persists:
// reasoning, the search itself, and the tool call it enabled.
func toolSearchTurn() []message.Message {
	return []message.Message{
		newMsg(message.User, message.TextContent{Text: "look up the MR"}),
		newMsg(message.Assistant,
			message.ReasoningContent{Thinking: "need the gitlab tool", Signature: "sig-abc"},
			message.ToolSearchContent{
				ToolUseID:  "srvtoolu_1",
				Name:       "tool_search_tool_regex",
				Input:      `{"pattern":"gitlab_.*"}`,
				References: []string{"gitlab_get_merge_request"},
			},
			message.ToolCall{ID: "toolu_1", Name: "gitlab_get_merge_request", Input: `{}`, Finished: true},
		),
	}
}

// TestCountTokensSanitizesServerToolBlocksOnBedrock is the end-to-end guard:
// the request that actually leaves for LiteLLM must carry no server-side tool
// block, and the returned count must include the local compensation.
func TestCountTokensSanitizesServerToolBlocksOnBedrock(t *testing.T) {
	srv, hits, lastBody := countTokensTestServer(t, http.StatusOK, `{"input_tokens": 100}`)

	client := newAnthropicClient(providerClientOptions{
		apiKey:           "test-key",
		baseURL:          srv.URL,
		model:            models.BedrockAnthropicModels[models.BedrockEUSonnet5],
		anthropicOptions: []AnthropicOption{WithAnthropicBedrock(true)},
	}).(*anthropicClient)

	got, err := client.countTokens(context.Background(), toolSearchTurn(), nil)
	if err != nil {
		t.Fatalf("countTokens: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 endpoint hit, got %d", hits.Load())
	}
	body := lastBody()
	for _, forbidden := range []string{`"server_tool_use"`, `"tool_search_tool_result"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("outgoing count_tokens body still contains %s — LiteLLM 500s on it:\n%s", forbidden, body)
		}
	}
	// The search payload survives as text so the endpoint still counts it.
	if !strings.Contains(body, "gitlab_get_merge_request") {
		t.Errorf("outgoing body dropped the discovered tool name:\n%s", body)
	}
	// One server_tool_use + one tool_search_tool_result were swapped.
	if want := int64(100 + 2*countTokensServerToolBlockOverhead); got != want {
		t.Errorf("tokens = %d, want %d", got, want)
	}
}

// TestCountTokensStripsServerToolBlocksOffBedrock covers the vertexai path,
// which in a LiteLLM deployment reaches the same token counter as bedrock and
// so 500s on the same blocks. Server-tool stripping is unconditional; only
// media stripping is Bedrock-gated.
func TestCountTokensStripsServerToolBlocksOffBedrock(t *testing.T) {
	srv, _, lastBody := countTokensTestServer(t, http.StatusOK, `{"input_tokens": 100}`)

	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.AnthropicModels[models.Claude5Sonnet],
	}).(*anthropicClient)

	got, err := client.countTokens(context.Background(), toolSearchTurn(), nil)
	if err != nil {
		t.Fatalf("countTokens: %v", err)
	}
	if want := int64(100 + 2*countTokensServerToolBlockOverhead); got != want {
		t.Errorf("tokens = %d, want %d", got, want)
	}
	body := lastBody()
	if strings.Contains(body, `"server_tool_use"`) {
		t.Errorf("server_tool_use must be stripped on every path, not just Bedrock:\n%s", body)
	}
	if !strings.Contains(body, "gitlab_get_merge_request") {
		t.Errorf("outgoing body dropped the discovered tool name:\n%s", body)
	}
}

// TestCountTokensLeavesMediaAloneOffBedrock pins the other half of the split:
// images keep their exact endpoint count everywhere except Bedrock, because
// the swap there trades accuracy for proxy compatibility.
func TestCountTokensLeavesMediaAloneOffBedrock(t *testing.T) {
	srv, _, lastBody := countTokensTestServer(t, http.StatusOK, `{"input_tokens": 100}`)

	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.AnthropicModels[models.Claude5Sonnet],
	}).(*anthropicClient)

	messages := []message.Message{
		newMsg(message.User, message.BinaryContent{
			MIMEType: "image/png",
			Data:     []byte("fakeimage"),
		}),
	}
	got, err := client.countTokens(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("countTokens: %v", err)
	}
	if got != 100 {
		t.Errorf("tokens = %d, want 100 (no local media compensation off the Bedrock path)", got)
	}
	if body := lastBody(); !strings.Contains(body, `"image"`) {
		t.Errorf("native path must send the image block verbatim:\n%s", body)
	}
}

// TestCountTokensLatchesAfterRepeatedServerErrors covers the backstop: a
// proxy that 500s on a content shape it cannot model will do so on every
// agent-loop iteration, so after a few consecutive failures we stop probing
// and let the provider layer run on the local estimate.
func TestCountTokensLatchesAfterRepeatedServerErrors(t *testing.T) {
	srv, hits, _ := countTokensTestServer(t, http.StatusInternalServerError,
		`{"detail":{"error":"Internal server error: Invalid content item type: server_tool_use."}}`)

	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.AnthropicModels[models.Claude5Sonnet],
	}).(*anthropicClient)

	// Failures below the threshold are plain errors — the endpoint keeps
	// getting probed in case the 500 was transient.
	for i := 1; i < countTokensServerErrorLatchThreshold; i++ {
		_, err := client.countTokens(context.Background(), nil, nil)
		if err == nil {
			t.Fatalf("call %d: expected an error", i)
		}
		if errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("call %d: latched too early", i)
		}
		if int(hits.Load()) != i {
			t.Fatalf("call %d: probes = %d, want %d", i, hits.Load(), i)
		}
	}

	_, err := client.countTokens(context.Background(), nil, nil)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("threshold call: want ErrUnsupported, got %v", err)
	}
	probes := hits.Load()

	// Latched: no further HTTP round-trips for the rest of the session.
	for range 3 {
		if _, err := client.countTokens(context.Background(), nil, nil); !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("post-latch call: want ErrUnsupported, got %v", err)
		}
	}
	if hits.Load() != probes {
		t.Errorf("post-latch probes = %d, want %d (endpoint must not be hit again)", hits.Load(), probes)
	}
}

// TestCountTokensServerErrorStreakResetsOnSuccess makes sure the latch needs
// CONSECUTIVE failures: an intermittently flaky proxy must keep its accurate
// count rather than degrading the whole session to the local estimate.
func TestCountTokensServerErrorStreakResetsOnSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Fail every other call: never countTokensServerErrorLatchThreshold
		// in a row.
		if n%2 == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":{"error":"transient"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"input_tokens": 7}`))
	}))
	defer srv.Close()

	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.AnthropicModels[models.Claude5Sonnet],
	}).(*anthropicClient)

	for i := range 8 {
		n, err := client.countTokens(context.Background(), nil, nil)
		if err != nil {
			if errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("call %d: latched on a non-consecutive failure streak", i+1)
			}
			continue
		}
		if n != 7 {
			t.Fatalf("call %d: tokens = %d, want 7", i+1, n)
		}
	}
	if client.countTokensUnsupported.Load() {
		t.Error("intermittent 5xx must not latch the endpoint as unsupported")
	}
}
