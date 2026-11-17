package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

func restartReasoningHistory(t *testing.T, history []message.Message) []message.Message {
	t.Helper()
	ctx := context.Background()
	sqlite := db.NewSQLiteProvider(t.TempDir())
	database, err := sqlite.Connect()
	require.NoError(t, err)
	goose.SetBaseFS(db.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.Up(database, "migrations/sqlite"))
	q := db.New(database)
	_, err = q.CreateSession(ctx, db.CreateSessionParams{ID: "reasoning", Title: "reasoning replay"})
	require.NoError(t, err)
	svc := message.NewService(db.NewQuerier(database), database)
	for i, msg := range history {
		saved, err := svc.Create(ctx, "reasoning", message.CreateMessageParams{Role: msg.Role, Model: msg.Model, Parts: msg.Parts, Seq: int64(i + 1)})
		require.NoError(t, err)
		require.NoError(t, svc.Update(ctx, saved))
	}
	svc.Shutdown()
	require.NoError(t, database.Close())
	database, err = sqlite.Connect()
	require.NoError(t, err)
	defer database.Close()
	svc = message.NewService(db.NewQuerier(database), database)
	defer svc.Shutdown()
	loaded, err := svc.List(ctx, "reasoning")
	require.NoError(t, err)
	return loaded
}

func scalarTestOptions(endpoint string) providerClientOptions {
	return providerClientOptions{apiKey: "test", baseURL: endpoint, maxTokens: 128,
		model: models.Model{ID: "local.reasoner", Provider: models.ProviderLocal, APIModel: "reasoner"}}
}

func scalarToolResponse(w http.ResponseWriter, stream bool, field, raw string) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fragments := []string{raw}
		var text string
		if json.Unmarshal([]byte(raw), &text) == nil && text != "" {
			runes := []rune(text)
			first, _ := json.Marshal(string(runes[:len(runes)/2]))
			second, _ := json.Marshal(string(runes[len(runes)/2:]))
			fragments = []string{string(first), string(second)}
		}
		for _, fragment := range fragments {
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",%q:%s}}]}\n\n", field, fragment)
		}
		fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Checking.","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{}"}},{"index":1,"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant",%q:%s,"content":"Checking.","tool_calls":[{"id":"call_a","type":"function","function":{"name":"read","arguments":"{}"}},{"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]},"finish_reason":"tool_calls"}]}`, field, raw)
}

func scalarReceive(t *testing.T, client ProviderClient, stream bool, history []message.Message, model models.ModelID) message.Message {
	t.Helper()
	prior := message.Message{Role: message.Assistant, Model: model}
	var response *ProviderResponse
	if stream {
		for event := range client.stream(context.Background(), history, nil) {
			switch event.Type {
			case EventThinkingDelta:
				prior.AppendReasoningContent(event.Thinking)
			case EventContentDelta:
				prior.AppendContent(event.Content)
			case EventComplete:
				response = event.Response
			case EventError:
				t.Fatal(event.Error)
			}
		}
	} else {
		var err error
		response, err = client.send(context.Background(), history, nil)
		require.NoError(t, err)
		prior.AppendContent(response.Content)
	}
	require.NotNil(t, response)
	if len(response.Reasoning) > 0 {
		prior.SetReasoningParts(response.Reasoning)
	}
	for _, call := range response.ToolCalls {
		prior.Parts = append(prior.Parts, call)
	}
	prior.AddFinish(response.FinishReason)
	return prior
}

func TestScalarReasoningPersistedToolLoopWire(t *testing.T) {
	for _, route := range []string{"compatible", "bedrock"} {
		for _, stream := range []bool{false, true} {
			for _, field := range scalarReasoningFields {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", route, stream, field), func(t *testing.T) {
					loadConfigIn(t, t.TempDir())
					bodies := make(chan []byte, 4)
					paths := make(chan string, 4)
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						bodies <- body
						paths <- r.URL.Path
						scalarToolResponse(w, gjson.GetBytes(body, "stream").Bool(), field, `"Inspect α\nthen both files."`)
					}))
					defer srv.Close()
					opts := scalarTestOptions(srv.URL + "/v1")
					makeClient := func() ProviderClient { return newOpenAIClient(opts) }
					if route == "bedrock" {
						opts.model = models.SupportedModels[models.BedrockKimiK3]
						makeClient = func() ProviderClient { return newBedrockClient(opts) }
					}
					user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Check files."}}}
					prior := scalarReceive(t, makeClient(), stream, []message.Message{user}, opts.model.ID)
					require.Len(t, prior.ReasoningParts(), 1)
					require.Equal(t, "Inspect α\nthen both files.", prior.ReasoningContent().Thinking)
					require.Len(t, prior.ToolCalls(), 2)
					firstBody := <-bodies
					require.False(t, gjson.GetBytes(firstBody, "messages.1."+field).Exists())
					<-paths
					result := message.Message{Role: message.Tool, Parts: []message.ContentPart{
						message.ToolResult{ToolCallID: "call_a", Name: "read", Content: "a=ok"},
						message.ToolResult{ToolCallID: "call_b", Name: "read", Content: "b=ok"},
					}}
					history := restartReasoningHistory(t, []message.Message{user, prior, result})
					require.Equal(t, prior.ReasoningParts(), history[1].ReasoningParts())
					_, err := makeClient().send(context.Background(), history, nil)
					require.NoError(t, err)
					body := <-bodies
					path := <-paths
					if route == "bedrock" {
						require.Contains(t, path, "/invoke")
					} else {
						require.Equal(t, "/v1/chat/completions", path)
					}
					require.Equal(t, "Inspect α\nthen both files.", gjson.GetBytes(body, "messages.2."+field).String())
					require.Equal(t, "Checking.", gjson.GetBytes(body, "messages.2.content").String())
					require.Equal(t, "call_a", gjson.GetBytes(body, "messages.2.tool_calls.0.id").String())
					require.Equal(t, "call_b", gjson.GetBytes(body, "messages.2.tool_calls.1.id").String())
					require.Equal(t, `{"path":"b"}`, gjson.GetBytes(body, "messages.2.tool_calls.1.function.arguments").String())
					require.Equal(t, "call_a", gjson.GetBytes(body, "messages.3.tool_call_id").String())
					require.Equal(t, "a=ok", gjson.GetBytes(body, "messages.3.content").String())
					require.Equal(t, "call_b", gjson.GetBytes(body, "messages.4.tool_call_id").String())
					require.Equal(t, "b=ok", gjson.GetBytes(body, "messages.4.content").String())
				})
			}
		}
	}
}

func TestScalarReasoningRejectsIncompatibleHistoryWire(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	bodies := make(chan []byte, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
		scalarToolResponse(w, false, "reasoning_content", `"new reasoning"`)
	}))
	defer srv.Close()
	opts := scalarTestOptions(srv.URL + "/v1")
	original := newOpenAIClient(opts).(*openaiClient)
	parts := original.scalarReasoning(`{"reasoning_content":"private reasoning"}`)
	require.Len(t, parts, 1)
	prior := message.Message{Role: message.Assistant, Model: opts.model.ID, Parts: []message.ContentPart{parts[0], message.TextContent{Text: "old answer"}}}
	history := restartReasoningHistory(t, []message.Message{prior})
	for _, change := range []string{"model", "api-model", "provider", "endpoint", "legacy", "signature", "unknown-field", "missing-route"} {
		t.Run(change, func(t *testing.T) {
			next := opts
			row := history[0]
			part := row.ReasoningParts()[0]
			origin := *part.Scalar
			part.Scalar = &origin
			switch change {
			case "model":
				next.model.ID = "local.other"
			case "api-model":
				next.model.APIModel = "other"
			case "provider":
				next.model.Provider = models.ProviderOpenAI
			case "endpoint":
				next.baseURL = srv.URL + "/other"
			case "legacy":
				part.Scalar = nil
			case "signature":
				part.Signature = "foreign-signature"
			case "unknown-field":
				part.Scalar.Field = "reasoning_details"
			case "missing-route":
				part.Scalar.Route = ""
			}
			row.SetReasoningParts([]message.ReasoningContent{part})
			_, err := newOpenAIClient(next).send(context.Background(), []message.Message{row}, nil)
			require.NoError(t, err)
			body := <-bodies
			require.Equal(t, "old answer", gjson.GetBytes(body, "messages.1.content").String())
			require.False(t, gjson.GetBytes(body, "messages.1.reasoning_content").Exists(), string(body))
			require.False(t, gjson.GetBytes(body, "messages.1.reasoning").Exists(), string(body))
		})
	}
}

func TestScalarReasoningMalformedWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, raw := range []string{`""`, `null`, `{}`, `[]`, `false`, `42`} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, raw), func(t *testing.T) {
				loadConfigIn(t, t.TempDir())
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					scalarToolResponse(w, stream, "reasoning_content", raw)
				}))
				defer srv.Close()
				opts := scalarTestOptions(srv.URL)
				prior := scalarReceive(t, newOpenAIClient(opts), stream, nil, opts.model.ID)
				require.Empty(t, prior.ReasoningParts())
			})
		}
	}
}

func TestScalarReasoningInterruptedStreamDoesNotReplay(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "10000")
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()
	opts := scalarTestOptions(srv.URL)
	client := newOpenAIClient(opts).(*openaiClient)
	prior := message.Message{Role: message.Assistant, Model: opts.model.ID}
	var sawError bool
	for event := range client.stream(context.Background(), nil, nil) {
		switch event.Type {
		case EventThinkingDelta:
			prior.AppendReasoningContent(event.Thinking)
		case EventComplete:
			t.Fatal("interrupted stream completed")
		case EventError:
			sawError = true
		}
	}
	require.True(t, sawError)
	require.EqualValues(t, 1, attempts.Load(), "retrying after visible output duplicates previews")
	require.Equal(t, "partial", prior.ReasoningContent().Thinking)
	loaded := restartReasoningHistory(t, []message.Message{prior})
	require.Empty(t, client.reasoningReplay(loaded[0]))
}

func TestScalarReasoningCaptureFragments(t *testing.T) {
	opts := scalarTestOptions("https://compatible.example/v1")
	client := newOpenAIClient(opts).(*openaiClient)
	var capture scalarReasoningCapture
	capture.add(`{"reasoning_content":"first ","reasoning":"other "}`)
	capture.add(`{"reasoning_content":null,"reasoning":"field"}`)
	capture.add(`{"reasoning_content":"second"}`)
	parts := client.scalarReasoningParts(capture)
	require.Len(t, parts, 2)
	require.Equal(t, "first second", parts[0].Thinking)
	require.Equal(t, "other field", parts[1].Thinking)
	capture.add(`{"reasoning_content":{"opaque":"payload"}}`)
	require.Len(t, client.scalarReasoningParts(capture), 1)
}

func TestScalarReasoningFirstPartyAndEndpointIdentity(t *testing.T) {
	opts := scalarTestOptions("https://api.openai.com/v1")
	opts.model.Provider = models.ProviderOpenAI
	opts.model.ID = "openai.gpt-test"
	opts.model.APIModel = "gpt-test"
	client := newOpenAIClient(opts).(*openaiClient)
	require.Empty(t, client.scalarReasoning(`{"reasoning":"unsupported"}`))
	t.Setenv("OPENAI_BASE_URL", "https://compatible.example/v1")
	opts.baseURL = ""
	a := newOpenAIClient(opts).(*openaiClient)
	t.Setenv("OPENAI_BASE_URL", "https://other.example/v1")
	b := newOpenAIClient(opts).(*openaiClient)
	require.NotEqual(t, a.scalarOrigin("reasoning"), b.scalarOrigin("reasoning"))
	require.NotContains(t, a.scalarOrigin("reasoning").Route, "compatible.example")
	// A normal GPT turn must serialize exactly as before, including no scalar fields.
	msgs := []message.Message{{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "answer"}, message.ReasoningContent{Thinking: "legacy"}}}}
	body, err := json.Marshal(client.convertMessages(msgs))
	require.NoError(t, err)
	require.False(t, strings.Contains(string(body), "reasoning"))
}

func TestScalarReasoningCanceledOrUnfinishedStream(t *testing.T) {
	for _, cancelStream := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelStream), func(t *testing.T) {
			loadConfigIn(t, t.TempDir())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if cancelStream {
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			opts := scalarTestOptions(srv.URL)
			client := newOpenAIClient(opts).(*openaiClient)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prior := message.Message{Role: message.Assistant, Model: opts.model.ID}
			for event := range client.stream(ctx, nil, nil) {
				switch event.Type {
				case EventThinkingDelta:
					prior.AppendReasoningContent(event.Thinking)
					if cancelStream {
						cancel()
					}
				case EventComplete:
					require.Empty(t, event.Response.Reasoning)
				case EventError:
					require.True(t, cancelStream)
				}
			}
			require.Equal(t, "partial", prior.ReasoningContent().Thinking)
			loaded := restartReasoningHistory(t, []message.Message{prior})
			require.Empty(t, client.reasoningReplay(loaded[0]))
		})
	}
}

func TestScalarReasoningRetryBeforeOutput(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Content-Length", "10000")
			w.(http.Flusher).Flush()
			return
		}
		scalarToolResponse(w, true, "reasoning_content", `"final attempt"`)
	}))
	defer srv.Close()
	opts := scalarTestOptions(srv.URL)
	prior := scalarReceive(t, newOpenAIClient(opts), true, nil, opts.model.ID)
	require.EqualValues(t, 2, attempts.Load())
	require.Len(t, prior.ReasoningParts(), 1)
	require.Equal(t, "final attempt", prior.ReasoningContent().Thinking)
	require.NotNil(t, prior.ReasoningParts()[0].Scalar)
}

func TestScalarReasoningFirstPartyWireUnchanged(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	bodies := make(chan []byte, 2)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
		require.Equal(t, "api.openai.com", r.Host)
		scalarToolResponse(w, false, "reasoning", `"not a supported first-party field"`)
	}))
	defer srv.Close()
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	opts := scalarTestOptions("https://api.openai.com/v1")
	opts.baseTransport = transport
	opts.model = models.Model{ID: "openai.gpt-test", Provider: models.ProviderOpenAI, APIModel: "gpt-test"}
	client := newOpenAIClient(opts)
	user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}
	assistant := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "answer"}}}
	response, err := client.send(context.Background(), []message.Message{user, assistant}, nil)
	require.NoError(t, err)
	require.Empty(t, response.Reasoning)
	baseline := <-bodies
	foreign := newOpenAIClient(scalarTestOptions("https://compatible.example/v1")).(*openaiClient).scalarReasoning(`{"reasoning_content":"private"}`)
	assistant.Parts = append(assistant.Parts, foreign[0], message.ReasoningContent{Thinking: "legacy preview"})
	history := restartReasoningHistory(t, []message.Message{user, assistant})
	_, err = client.send(context.Background(), history, nil)
	require.NoError(t, err)
	require.Equal(t, string(baseline), string(<-bodies))
}

func TestNativeClaudeReasoningPersistedWireUnchanged(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	bodies := make(chan []byte, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"thinking","thinking":"Check α first.","signature":"signature=="},{"type":"redacted_thinking","data":"opaque=="},{"type":"text","text":"Checking."},{"type":"tool_use","id":"call_a","name":"read","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":20}}`)
	}))
	defer srv.Close()
	opts := providerClientOptions{apiKey: "test", baseURL: srv.URL, model: models.SupportedModels[models.Claude46Opus], maxTokens: 1024, systemMessage: "stable system"}
	client := newAnthropicClient(opts)
	user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Check file."}}}
	prior := scalarReceive(t, client, false, []message.Message{user}, opts.model.ID)
	<-bodies
	require.Len(t, prior.ReasoningParts(), 2)
	result := message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_a", Name: "read", Content: "ok"}}}
	history := []message.Message{user, prior, result}
	_, err := client.send(context.Background(), history, nil)
	require.NoError(t, err)
	baseline := <-bodies
	history = restartReasoningHistory(t, history)
	_, err = newAnthropicClient(opts).send(context.Background(), history, nil)
	require.NoError(t, err)
	body := <-bodies
	require.Equal(t, string(baseline), string(body), "native signed and cached request must survive restart byte-for-byte")
	require.Equal(t, "Check α first.", gjson.GetBytes(body, "messages.1.content.0.thinking").String())
	require.Equal(t, "signature==", gjson.GetBytes(body, "messages.1.content.0.signature").String())
	require.Equal(t, "opaque==", gjson.GetBytes(body, "messages.1.content.1.data").String())
	require.False(t, gjson.GetBytes(body, "messages.1.content.0.cache_control").Exists())
	require.Equal(t, "ephemeral", gjson.GetBytes(body, "messages.1.content.3.cache_control.type").String())
	require.False(t, gjson.GetBytes(body, "messages.1.reasoning_content").Exists())
}
