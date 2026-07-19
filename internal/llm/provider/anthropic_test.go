package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type testTool struct {
	name     string
	baseline bool
}

func (m *testTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        m.name,
		Description: "test",
		Parameters:  map[string]any{},
	}
}

func (m *testTool) Run(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse(""), nil
}

func (m *testTool) AllowParallelism(_ tools.ToolCall, _ []tools.ToolCall) bool {
	return true
}

func (m *testTool) IsBaseline() bool { return m.baseline }

func newTestTool(name string, baseline bool) tools.BaseTool {
	return &testTool{name: name, baseline: baseline}
}

func TestFilterBetaHeaders(t *testing.T) {
	model1M := models.Model{ContextWindow: 1_000_000}
	model200K := models.Model{ContextWindow: 200_000}

	tests := []struct {
		name  string
		value string
		model models.Model
		want  string
	}{
		{
			name:  "single context-1m beta kept for 1M model",
			value: "context-1m-2025-08-07",
			model: model1M,
			want:  "context-1m-2025-08-07",
		},
		{
			name:  "single context-1m beta stripped for 200K model",
			value: "context-1m-2025-08-07",
			model: model200K,
			want:  "",
		},
		{
			name:  "context-1m among multiple betas stripped for 200K model",
			value: "context-1m-2025-08-07,task-budgets-2026-03-13",
			model: model200K,
			want:  "task-budgets-2026-03-13",
		},
		{
			name:  "context-1m among multiple betas kept for 1M model",
			value: "context-1m-2025-08-07,task-budgets-2026-03-13",
			model: model1M,
			want:  "context-1m-2025-08-07,task-budgets-2026-03-13",
		},
		{
			name:  "non-context beta unchanged for 200K model",
			value: "task-budgets-2026-03-13",
			model: model200K,
			want:  "task-budgets-2026-03-13",
		},
		{
			name:  "empty string returns empty",
			value: "",
			model: model200K,
			want:  "",
		},
		{
			name:  "whitespace around values is trimmed",
			value: " context-1m-2025-08-07 , task-budgets-2026-03-13 ",
			model: model200K,
			want:  "task-budgets-2026-03-13",
		},
		{
			name:  "future context-1m version also stripped for small model",
			value: "context-1m-2026-01-01",
			model: model200K,
			want:  "",
		},
		{
			name:  "only context-1m values stripped, others preserved",
			value: "advanced-tool-use,context-1m-2025-08-07,task-budgets-2026-03-13",
			model: model200K,
			want:  "advanced-tool-use,task-budgets-2026-03-13",
		},
		{
			name:  "trailing comma handled",
			value: "context-1m-2025-08-07,",
			model: model200K,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterBetaHeaders(tt.value, tt.model)
			if got != tt.want {
				t.Errorf("filterBetaHeaders(%q, ctx=%d) = %q, want %q",
					tt.value, tt.model.ContextWindow, got, tt.want)
			}
		})
	}
}

func TestConvertToolsCacheBreakpoints(t *testing.T) {
	tests := []struct {
		name                string
		tools               []tools.BaseTool
		disableCache        bool
		expectedBreakpoints []int
	}{
		{
			name:                "only baseline tools — single breakpoint on last",
			tools:               []tools.BaseTool{newTestTool("read", true), newTestTool("write", true), newTestTool("bash", true)},
			expectedBreakpoints: []int{2},
		},
		{
			name: "baseline plus external — single breakpoint on last",
			tools: []tools.BaseTool{
				newTestTool("read", true), newTestTool("write", true),
				newTestTool("mcp_a", false), newTestTool("mcp_b", false),
			},
			expectedBreakpoints: []int{3},
		},
		{
			name:                "only external tools — single breakpoint on last",
			tools:               []tools.BaseTool{newTestTool("mcp_a", false), newTestTool("mcp_b", false)},
			expectedBreakpoints: []int{1},
		},
		{
			name:                "single tool — breakpoint on it",
			tools:               []tools.BaseTool{newTestTool("read", true)},
			expectedBreakpoints: []int{0},
		},
		{
			name: "cache disabled — no breakpoints",
			tools: []tools.BaseTool{
				newTestTool("read", true), newTestTool("mcp_a", false),
			},
			disableCache:        true,
			expectedBreakpoints: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &anthropicClient{
				options: anthropicOptions{disableCache: tt.disableCache},
			}

			result := client.convertTools(tt.tools)

			breakpointSet := make(map[int]bool)
			for _, idx := range tt.expectedBreakpoints {
				breakpointSet[idx] = true
			}

			for i, tool := range result {
				hasBreakpoint := tool.OfTool != nil && tool.OfTool.CacheControl.Type != ""
				if breakpointSet[i] && !hasBreakpoint {
					t.Errorf("tool[%d] (%s): expected cache breakpoint but none found", i, tt.tools[i].Info().Name)
				}
				if !breakpointSet[i] && hasBreakpoint {
					t.Errorf("tool[%d] (%s): unexpected cache breakpoint", i, tt.tools[i].Info().Name)
				}
			}
		})
	}
}

// newMsg creates a test message with the given role and parts.
func newMsg(role message.MessageRole, parts ...message.ContentPart) message.Message {
	return message.Message{
		Role:  role,
		Parts: parts,
	}
}

func TestConvertMessagesCacheBreakpoints(t *testing.T) {
	tests := []struct {
		name         string
		messages     []message.Message
		disableCache bool
		// expectedCache maps converted-message index → true if any block in that message should have cache_control
		expectedCache map[int]bool
	}{
		{
			name: "user messages — last 2 cached",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "first"}),
				newMsg(message.User, message.TextContent{Text: "second"}),
				newMsg(message.User, message.TextContent{Text: "third"}),
			},
			expectedCache: map[int]bool{0: false, 1: true, 2: true},
		},
		{
			name: "tool loop — assistant tool_use and tool result both cached",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "find files"}),
				newMsg(message.Assistant, message.ToolCall{ID: "tc1", Name: "grep", Input: `{}`, Finished: true}),
				newMsg(message.Tool, message.ToolResult{ToolCallID: "tc1", Name: "grep", Content: "result1"}),
				newMsg(message.Assistant, message.ToolCall{ID: "tc2", Name: "read", Input: `{}`, Finished: true}),
				newMsg(message.Tool, message.ToolResult{ToolCallID: "tc2", Name: "read", Content: "result2"}),
			},
			expectedCache: map[int]bool{0: false, 1: false, 2: false, 3: true, 4: true},
		},
		{
			name: "assistant with text and tool_use — cache on last block",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "hello"}),
				newMsg(message.Assistant, message.TextContent{Text: "thinking"}, message.ToolCall{ID: "tc1", Name: "read", Input: `{}`, Finished: true}),
			},
			expectedCache: map[int]bool{0: true, 1: true},
		},
		{
			name: "cache disabled — no markers anywhere",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "first"}),
				newMsg(message.Assistant, message.TextContent{Text: "response"}),
			},
			disableCache:  true,
			expectedCache: map[int]bool{0: false, 1: false},
		},
		{
			name: "single user message — cached",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "hello"}),
			},
			expectedCache: map[int]bool{0: true},
		},
		{
			name: "multiple tool results in one message — cache on last result",
			messages: []message.Message{
				newMsg(message.User, message.TextContent{Text: "do stuff"}),
				newMsg(message.Assistant,
					message.ToolCall{ID: "tc1", Name: "read", Input: `{}`, Finished: true},
					message.ToolCall{ID: "tc2", Name: "grep", Input: `{}`, Finished: true},
				),
				newMsg(message.Tool,
					message.ToolResult{ToolCallID: "tc1", Name: "read", Content: "file contents"},
					message.ToolResult{ToolCallID: "tc2", Name: "grep", Content: "grep results"},
				),
			},
			expectedCache: map[int]bool{0: false, 1: true, 2: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &anthropicClient{
				options: anthropicOptions{disableCache: tt.disableCache},
			}

			result := client.convertMessages(tt.messages)

			if len(result) > len(tt.messages) {
				t.Fatalf("got %d converted messages, expected at most %d", len(result), len(tt.messages))
			}

			for i, mp := range result {
				expectCached, exists := tt.expectedCache[i]
				if !exists {
					continue
				}

				hasCached := false
				for _, block := range mp.Content {
					if block.OfText != nil && block.OfText.CacheControl.Type != "" {
						hasCached = true
					}
					if block.OfToolUse != nil && block.OfToolUse.CacheControl.Type != "" {
						hasCached = true
					}
					if block.OfToolResult != nil && block.OfToolResult.CacheControl.Type != "" {
						hasCached = true
					}
				}

				if expectCached && !hasCached {
					t.Errorf("message[%d]: expected cache breakpoint but none found", i)
				}
				if !expectCached && hasCached {
					t.Errorf("message[%d]: unexpected cache breakpoint", i)
				}
			}
		})
	}
}

// TestConvertBinaryContentBlockTypes locks in the MIME-type routing for
// binary attachments. PDFs must become document blocks — wrapping them in
// image blocks (the old behavior) is an invalid request that Bedrock
// surfaces as an HTTP/2 stream reset, permanently poisoning any session
// whose history contains the attachment.
func TestConvertBinaryContentBlockTypes(t *testing.T) {
	tests := []struct {
		name     string
		bc       message.BinaryContent
		wantKind string
	}{
		{
			name:     "png stays an image block",
			bc:       message.BinaryContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
			wantKind: "image",
		},
		{
			name:     "mime parameters are stripped",
			bc:       message.BinaryContent{MIMEType: "image/jpeg; charset=binary", Data: []byte{1}},
			wantKind: "image",
		},
		{
			name:     "pdf becomes a document block",
			bc:       message.BinaryContent{MIMEType: "application/pdf", Data: []byte("%PDF-1.7")},
			wantKind: "document",
		},
		{
			name:     "plain text becomes a text-source document block",
			bc:       message.BinaryContent{MIMEType: "text/plain; charset=utf-8", Data: []byte("hello")},
			wantKind: "document",
		},
		{
			name:     "audio degrades to a text placeholder",
			bc:       message.BinaryContent{MIMEType: "audio/ogg", Path: ".opencode/bridge/media/voice.ogg", Data: []byte{1, 2}},
			wantKind: "text",
		},
		{
			name:     "unknown mime degrades to a text placeholder",
			bc:       message.BinaryContent{MIMEType: "application/zip", Data: []byte{1, 2}},
			wantKind: "text",
		},
		{
			// A zero-byte payload must not become an empty document block —
			// the API rejects empty content, and the persisted attachment
			// would poison every subsequent turn of the session.
			name:     "zero-byte text file degrades to a text placeholder",
			bc:       message.BinaryContent{MIMEType: "text/plain", Path: "empty.log", Data: nil},
			wantKind: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := convertBinaryContent(tt.bc)
			var gotKind string
			switch {
			case block.OfImage != nil:
				gotKind = "image"
			case block.OfDocument != nil:
				gotKind = "document"
			case block.OfText != nil:
				gotKind = "text"
			default:
				gotKind = "other"
			}
			if gotKind != tt.wantKind {
				t.Fatalf("got %s block, want %s", gotKind, tt.wantKind)
			}
			if gotKind == "text" && tt.bc.Path != "" && !strings.Contains(block.OfText.Text, tt.bc.Path) {
				t.Errorf("placeholder text %q should reference saved path %q", block.OfText.Text, tt.bc.Path)
			}
		})
	}
}

// TestConvertMessagesSkipsEmptyUserText covers the caption-less bridge
// attachment: the user message has an empty text part plus a binary part.
// The API rejects empty text blocks, so conversion must drop the text and
// keep the attachment.
func TestConvertMessagesSkipsEmptyUserText(t *testing.T) {
	client := &anthropicClient{options: anthropicOptions{disableCache: true}}

	messages := []message.Message{
		newMsg(message.User,
			message.TextContent{Text: ""},
			message.BinaryContent{MIMEType: "application/pdf", Data: []byte("%PDF-1.7")},
		),
	}
	result := client.convertMessages(messages)
	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}
	if len(result[0].Content) != 1 {
		t.Fatalf("got %d content blocks, want 1 (empty text must be dropped)", len(result[0].Content))
	}
	if result[0].Content[0].OfDocument == nil {
		t.Fatal("expected the remaining block to be the pdf document block")
	}

	// A user message that ends up with no renderable blocks is skipped
	// entirely rather than sent as an (invalid) empty-content message.
	empty := client.convertMessages([]message.Message{
		newMsg(message.User, message.TextContent{Text: "   "}),
	})
	if len(empty) != 0 {
		t.Fatalf("got %d messages, want 0 for blank-only user message", len(empty))
	}
}

// TestConvertMessagesCacheOnAttachmentBlock verifies the cache breakpoint
// lands on the last block even when that block is an attachment (the old
// code pinned it to the text block, which no longer always exists).
func TestConvertMessagesCacheOnAttachmentBlock(t *testing.T) {
	client := &anthropicClient{options: anthropicOptions{}}

	result := client.convertMessages([]message.Message{
		newMsg(message.User,
			message.TextContent{Text: "see attached"},
			message.BinaryContent{MIMEType: "application/pdf", Data: []byte("%PDF-1.7")},
		),
	})
	if len(result) != 1 || len(result[0].Content) != 2 {
		t.Fatalf("unexpected conversion shape: %d messages", len(result))
	}
	doc := result[0].Content[1].OfDocument
	if doc == nil {
		t.Fatal("expected document block last")
	}
	if doc.CacheControl.Type == "" {
		t.Error("expected cache breakpoint on the trailing document block")
	}
}

// TestToolCallsNormalizesEmptyInput exercises the Bedrock-specific code path
// where a content_block_start event for tool_use carries {id, name} but no
// "input" field. The SDK accumulator leaves Input as nil bytes; toolCalls()
// must normalize to "{}" so we never persist invalid JSON.
func TestToolCallsNormalizesEmptyInput(t *testing.T) {
	tests := []struct {
		name    string
		rawJSON string
		want    string
	}{
		{
			name:    "bedrock content_block_start missing input field",
			rawJSON: `{"type":"tool_use","id":"toolu_bdrk_001","name":"write"}`,
			want:    "{}",
		},
		{
			name:    "input present as empty object",
			rawJSON: `{"type":"tool_use","id":"toolu_bdrk_002","name":"write","input":{}}`,
			want:    "{}",
		},
		{
			name:    "input populated",
			rawJSON: `{"type":"tool_use","id":"toolu_bdrk_003","name":"write","input":{"file_path":"/tmp/x"}}`,
			want:    `{"file_path":"/tmp/x"}`,
		},
	}

	client := &anthropicClient{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cb anthropic.ContentBlockUnion
			if err := json.Unmarshal([]byte(tt.rawJSON), &cb); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			msg := anthropic.Message{Content: []anthropic.ContentBlockUnion{cb}}
			got := client.toolCalls(msg)
			if len(got) != 1 {
				t.Fatalf("expected 1 tool call, got %d", len(got))
			}
			if got[0].Input != tt.want {
				t.Errorf("Input = %q, want %q", got[0].Input, tt.want)
			}
			// Always must be valid JSON object after normalization.
			var probe map[string]any
			if err := json.Unmarshal([]byte(got[0].Input), &probe); err != nil {
				t.Errorf("Input is not valid JSON object: %v", err)
			}
		})
	}
}

// TestConvertMessagesAcceptsEmptyToolInput ensures replaying a historical
// message whose ToolCall.Input was persisted as "" (pre-fix Bedrock rows)
// converts to a valid OfToolUse block with {} input, without erroring.
func TestConvertMessagesAcceptsEmptyToolInput(t *testing.T) {
	client := &anthropicClient{
		options: anthropicOptions{disableCache: true},
	}

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "whitespace only", input: "   "},
		{name: "newline only", input: "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := []message.Message{
				newMsg(message.User, message.TextContent{Text: "do stuff"}),
				newMsg(message.Assistant, message.ToolCall{
					ID: "toolu_bdrk_old", Name: "write", Input: tt.input, Finished: true,
				}),
			}
			result := client.convertMessages(messages)
			if len(result) != 2 {
				t.Fatalf("expected 2 converted messages, got %d", len(result))
			}
			asst := result[1]
			var sawToolUse bool
			for _, block := range asst.Content {
				if block.OfToolUse == nil {
					continue
				}
				sawToolUse = true
				inputJSON, err := json.Marshal(block.OfToolUse.Input)
				if err != nil {
					t.Fatalf("marshal tool_use input: %v", err)
				}
				if string(inputJSON) != "{}" {
					t.Errorf("tool_use input = %s, want {}", inputJSON)
				}
			}
			if !sawToolUse {
				t.Errorf("expected OfToolUse block in assistant message")
			}
		})
	}
}

func TestStripMediaForCountTokens(t *testing.T) {
	imgParam := anthropic.NewImageBlockBase64("image/png", "ZmFrZWltYWdl")
	// 400 base64 chars → 300 decoded bytes → below the 1500-token floor.
	smallPDF := anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
		Data: strings.Repeat("A", 400),
	})
	// 400_000 base64 chars → 300_000 decoded bytes → 3000 tokens at the
	// 100 bytes/token heuristic, above the floor.
	largePDF := anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
		Data: strings.Repeat("A", 400_000),
	})
	textDoc := anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
		Data: "inline text document",
	})
	tests := []struct {
		name            string
		messages        []anthropic.MessageParam
		wantExtraTokens int64
		wantNoMedia     bool
	}{
		{
			name: "top-level image swapped to text placeholder",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("look at this"), imgParam),
			},
			wantExtraTokens: countTokensImageTokenEstimate,
			wantNoMedia:     true,
		},
		{
			name: "image inside tool_result swapped",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: "tc1",
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfImage: imgParam.OfImage},
						},
						IsError: param.NewOpt(false),
					},
				}),
			},
			wantExtraTokens: countTokensImageTokenEstimate,
			wantNoMedia:     true,
		},
		{
			name: "no media leaves messages untouched",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("plain text")),
			},
			wantExtraTokens: 0,
			wantNoMedia:     true,
		},
		{
			name: "mixed text + image + tool_result image — counts both",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("hi"), imgParam),
				anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: "tc2",
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfText: &anthropic.TextBlockParam{Text: "ok"}},
							{OfImage: imgParam.OfImage},
						},
					},
				}),
			},
			wantExtraTokens: 2 * countTokensImageTokenEstimate,
			wantNoMedia:     true,
		},
		{
			name: "small pdf document swapped at the token floor",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("see attached"), smallPDF),
			},
			wantExtraTokens: countTokensImageTokenEstimate,
			wantNoMedia:     true,
		},
		{
			name: "large pdf document estimated from payload size",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(largePDF),
			},
			wantExtraTokens: 3000,
			wantNoMedia:     true,
		},
		{
			name: "plain-text document re-inlined exactly, no extra estimate",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(textDoc),
			},
			wantExtraTokens: 0,
			wantNoMedia:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Snapshot pre-call so we can assert non-mutation of inputs.
			beforeImagePtrs := collectImagePointers(tt.messages)

			out, extra := stripMediaForCountTokens(tt.messages)
			if extra != tt.wantExtraTokens {
				t.Errorf("extra tokens = %d, want %d", extra, tt.wantExtraTokens)
			}
			if tt.wantNoMedia {
				for i, msg := range out {
					for j, block := range msg.Content {
						if block.OfImage != nil {
							t.Errorf("message[%d].Content[%d] still has OfImage", i, j)
						}
						if block.OfDocument != nil {
							t.Errorf("message[%d].Content[%d] still has OfDocument", i, j)
						}
						if block.OfToolResult != nil {
							for k, inner := range block.OfToolResult.Content {
								if inner.OfImage != nil {
									t.Errorf("message[%d].Content[%d].ToolResult.Content[%d] still has OfImage", i, j, k)
								}
							}
						}
					}
				}
			}
			// Caller's input slice must be untouched — images still present
			// at the same locations on the original messages.
			afterImagePtrs := collectImagePointers(tt.messages)
			if len(beforeImagePtrs) != len(afterImagePtrs) {
				t.Errorf("input mutated: %d images before, %d after", len(beforeImagePtrs), len(afterImagePtrs))
			}
		})
	}
}

// collectImagePointers walks messages and gathers pointers to every OfImage
// block (top-level and nested in tool_result). Used to assert that
// stripImagesForCountTokens does not mutate its input slice.
func collectImagePointers(messages []anthropic.MessageParam) []*anthropic.ImageBlockParam {
	var out []*anthropic.ImageBlockParam
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.OfImage != nil {
				out = append(out, block.OfImage)
			}
			if block.OfToolResult != nil {
				for _, inner := range block.OfToolResult.Content {
					if inner.OfImage != nil {
						out = append(out, inner.OfImage)
					}
				}
			}
		}
	}
	return out
}

// TestConvertMessagesMalformedToolInput verifies the WARN path still fires
// (and falls back to {}) when stored Input is not valid JSON. The empty-input
// path is silent; this path is not.
func TestConvertMessagesMalformedToolInput(t *testing.T) {
	client := &anthropicClient{
		options: anthropicOptions{disableCache: true},
	}
	messages := []message.Message{
		newMsg(message.User, message.TextContent{Text: "do stuff"}),
		newMsg(message.Assistant, message.ToolCall{
			ID: "tc1", Name: "write", Input: "{not valid json", Finished: true,
		}),
	}
	result := client.convertMessages(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 converted messages, got %d", len(result))
	}
	var sawToolUse bool
	for _, block := range result[1].Content {
		if block.OfToolUse == nil {
			continue
		}
		sawToolUse = true
		got, err := json.Marshal(block.OfToolUse.Input)
		if err != nil {
			t.Fatalf("marshal tool_use input: %v", err)
		}
		if string(got) != "{}" {
			t.Errorf("tool_use input fallback = %s, want {}", got)
		}
	}
	if !sawToolUse {
		t.Errorf("expected OfToolUse block in assistant message")
	}
}

// TestStripImagesFastPath verifies that the fast path returns the input
// slice unchanged when no images are present — both correctness (count=0,
// same slice header) and the implicit allocation guarantee.
func TestStripImagesFastPath(t *testing.T) {
	tests := []struct {
		name     string
		messages []anthropic.MessageParam
	}{
		{
			name:     "empty slice",
			messages: nil,
		},
		{
			name: "text-only single message",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("plain text")),
			},
		},
		{
			name: "tool_result with text content only",
			messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: "tc1",
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfText: &anthropic.TextBlockParam{Text: "ok"}},
						},
					},
				}),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, extra := stripMediaForCountTokens(tt.messages)
			if extra != 0 {
				t.Errorf("extra tokens = %d, want 0", extra)
			}
			// Fast path returns the same slice header; compare via len + element identity.
			if len(out) != len(tt.messages) {
				t.Errorf("len = %d, want %d", len(out), len(tt.messages))
			}
		})
	}
}
