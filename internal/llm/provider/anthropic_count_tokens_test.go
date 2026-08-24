package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
			// server_tool_use + tool_search_tool_result + 2 tool_reference wrappers.
			wantExtraTokens: 2*countTokensServerToolBlockOverhead + 2*countTokensToolReferenceOverhead,
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
			// An error result carries no references to scale on.
			wantExtraTokens: 2 * countTokensServerToolBlockOverhead,
			wantSubstrings:  []string{"tool_search_tool_bm25", "too_many_requests"},
		},
		{
			name: "empty reference list still yields non-empty content",
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
//
// ReasoningOffset is set so convertMessages takes the ORDER-PRESERVING replay
// branch (toolSearchesReplayableInOrder) — the shape rows persisted by current
// code produce, and the only one that replays the signed thinking block
// alongside the search. Leaving it nil silently falls back to the
// thinking-dropping branch, which would make the end-to-end assertions below
// exercise a shape production never sends.
func toolSearchTurn() []message.Message {
	reasoningOffset := 1
	return []message.Message{
		newMsg(message.User, message.TextContent{Text: "look up the MR"}),
		newMsg(message.Assistant,
			message.ReasoningContent{Thinking: "need the gitlab tool", Signature: "sig-abc"},
			message.ToolSearchContent{
				ToolUseID:       "srvtoolu_1",
				Name:            "tool_search_tool_regex",
				Input:           `{"pattern":"gitlab_.*"}`,
				References:      []string{"gitlab_get_merge_request"},
				ReasoningOffset: &reasoningOffset,
			},
			message.ToolCall{ID: "toolu_1", Name: "gitlab_get_merge_request", Input: `{}`, Finished: true},
		),
	}
}

// toolSearchTurnOverhead is what stripServerToolBlocksForCountTokens adds back
// for toolSearchTurn: one server_tool_use, one tool_search_tool_result, one
// tool_reference.
const toolSearchTurnOverhead = int64(2*countTokensServerToolBlockOverhead + countTokensToolReferenceOverhead)

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
	if want := 100 + toolSearchTurnOverhead; got != want {
		t.Errorf("tokens = %d, want %d", got, want)
	}
	// The signed thinking block rides along untouched — stripping the search
	// blocks must not disturb it.
	if !strings.Contains(body, `"thinking"`) {
		t.Errorf("outgoing body dropped the replayed thinking block:\n%s", body)
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
	if want := 100 + toolSearchTurnOverhead; got != want {
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

// countTokensStatusServer answers each call with the next status in the given
// cycle, so a test can script an exact failure sequence.
func countTokensStatusServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1)) - 1
		status := statuses[n%len(statuses)]
		w.Header().Set("Content-Type", "application/json")
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"input_tokens": 7}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"scripted"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newTestAnthropicClient(t *testing.T, baseURL string) *anthropicClient {
	t.Helper()
	client, ok := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: baseURL,
		model:   models.AnthropicModels[models.Claude5Sonnet],
	}).(*anthropicClient)
	if !ok {
		t.Fatal("newAnthropicClient did not return *anthropicClient")
	}
	return client
}

// TestCountTokensCoolsDownAfterRepeatedShapeRejections covers the backstop: a
// proxy that 500s on a content shape it cannot model will do so on every
// agent-loop iteration, so after a few consecutive failures we stop probing
// and let the provider layer run on the local estimate.
func TestCountTokensCoolsDownAfterRepeatedShapeRejections(t *testing.T) {
	srv, hits := countTokensStatusServer(t, http.StatusInternalServerError)
	client := newTestAnthropicClient(t, srv.URL)

	// Failures below the threshold are plain errors — the endpoint keeps
	// getting probed in case the 500 was a one-off.
	for i := 1; i < countTokensShapeErrorThreshold; i++ {
		_, err := client.countTokens(context.Background(), nil, nil)
		if err == nil {
			t.Fatalf("call %d: expected an error", i)
		}
		if errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("call %d: cooled down too early", i)
		}
		if int(hits.Load()) != i {
			t.Fatalf("call %d: probes = %d, want %d", i, hits.Load(), i)
		}
	}

	if _, err := client.countTokens(context.Background(), nil, nil); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("threshold call: want ErrUnsupported, got %v", err)
	}
	probes := hits.Load()

	for range 3 {
		if _, err := client.countTokens(context.Background(), nil, nil); !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("in-cooldown call: want ErrUnsupported, got %v", err)
		}
	}
	if hits.Load() != probes {
		t.Errorf("in-cooldown probes = %d, want %d (endpoint must not be hit again)", hits.Load(), probes)
	}

	// The cooldown must NOT be permanent: one anthropicClient serves every
	// session of an agent for the life of the process, and localTokenEstimate
	// is coarse enough that never re-probing can cost auto-compaction.
	if client.countTokensUnsupported.Load() {
		t.Error("a shape rejection must cool down, not latch permanently like a 404")
	}
	client.countTokensCooldownUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if _, err := client.countTokens(context.Background(), nil, nil); errors.Is(err, errors.ErrUnsupported) {
		t.Error("expired cooldown must re-probe the endpoint")
	}
	if hits.Load() != probes+1 {
		t.Errorf("post-cooldown probes = %d, want %d", hits.Load(), probes+1)
	}
}

// TestCountTokensTransientOverloadNeverCoolsDown pins the cooldown to statuses
// this client does not already classify as transient. 429/503/529 are explicit
// "come back later" signals (503 is Bedrock's ordinary overload answer), so a
// load spike must not cost the accurate count.
func TestCountTokensTransientOverloadNeverCoolsDown(t *testing.T) {
	for status := range retryableHTTPStatuses {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv, hits := countTokensStatusServer(t, status)
			client := newTestAnthropicClient(t, srv.URL)

			calls := countTokensShapeErrorThreshold * 3
			for i := range calls {
				_, err := client.countTokens(context.Background(), nil, nil)
				if errors.Is(err, errors.ErrUnsupported) {
					t.Fatalf("call %d: HTTP %d must never cool down the endpoint", i+1, status)
				}
			}
			if int(hits.Load()) != calls {
				t.Errorf("probes = %d, want %d — every call must still reach the endpoint", hits.Load(), calls)
			}
		})
	}
}

// TestCountTokensShapeErrorStreakIsConsecutive is the regression guard for a
// streak that only reset on success: a 4xx, a transient 5xx or a cancellation
// between shape rejections has to break it, or three SCATTERED 500s over a long
// session would degrade it to local estimation.
func TestCountTokensShapeErrorStreakIsConsecutive(t *testing.T) {
	interrupters := map[string]int{
		"success":            http.StatusOK,
		"bad request":        http.StatusBadRequest,
		"transient overload": http.StatusServiceUnavailable,
	}
	for name, interrupter := range interrupters {
		t.Run(name, func(t *testing.T) {
			srv, _ := countTokensStatusServer(t, http.StatusInternalServerError, interrupter)
			client := newTestAnthropicClient(t, srv.URL)

			// Alternating 500 / interrupter: never the threshold in a row.
			for i := range countTokensShapeErrorThreshold * 4 {
				_, err := client.countTokens(context.Background(), nil, nil)
				if errors.Is(err, errors.ErrUnsupported) {
					t.Fatalf("call %d: cooled down on a non-consecutive streak", i+1)
				}
			}
		})
	}
}

// TestCountTokensCancellationDoesNotAffectStreak covers the third streak
// outcome: our own cancellation says nothing about the endpoint, so it must
// neither extend nor break the streak.
func TestCountTokensCancellationDoesNotAffectStreak(t *testing.T) {
	srv, _ := countTokensStatusServer(t, http.StatusInternalServerError)
	client := newTestAnthropicClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range countTokensShapeErrorThreshold * 2 {
		if _, err := client.countTokens(ctx, nil, nil); errors.Is(err, errors.ErrUnsupported) {
			t.Fatal("a cancelled context must not cool down the endpoint")
		}
	}
	if got := client.countTokensShapeErrors.Load(); got != 0 {
		t.Errorf("streak = %d, want 0 — cancellations carry no signal", got)
	}
}

// TestCountTokensBedrockSumsBothCompensations covers the one request that hits
// both strippers: the server-tool and media estimates must add, and running
// the media pass over the server-tool pass's output must not lose either.
func TestCountTokensBedrockSumsBothCompensations(t *testing.T) {
	srv, _, lastBody := countTokensTestServer(t, http.StatusOK, `{"input_tokens": 100}`)

	client := newAnthropicClient(providerClientOptions{
		apiKey:           "test-key",
		baseURL:          srv.URL,
		model:            models.BedrockAnthropicModels[models.BedrockEUSonnet5],
		anthropicOptions: []AnthropicOption{WithAnthropicBedrock(true)},
	}).(*anthropicClient)

	messages := append(toolSearchTurn(), newMsg(message.User, message.BinaryContent{
		MIMEType: "image/png",
		Data:     []byte("fakeimage"),
	}))
	got, err := client.countTokens(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("countTokens: %v", err)
	}
	if want := 100 + toolSearchTurnOverhead + countTokensImageTokenEstimate; got != want {
		t.Errorf("tokens = %d, want %d", got, want)
	}
	body := lastBody()
	for _, forbidden := range []string{`"server_tool_use"`, `"tool_search_tool_result"`, `"image"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("outgoing body still contains %s:\n%s", forbidden, body)
		}
	}
}
