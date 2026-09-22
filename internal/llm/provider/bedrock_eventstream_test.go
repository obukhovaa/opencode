package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
)

// encodeBedrockChunk builds one binary AWS EventStream "chunk" frame carrying
// the given Anthropic event JSON, the way Bedrock streams it.
func encodeBedrockChunk(t *testing.T, eventJSON string) []byte {
	t.Helper()
	payload, err := json.Marshal(bedrockEventStreamChunk{
		Bytes: base64.StdEncoding.EncodeToString([]byte(eventJSON)),
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
			{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue("chunk")},
		},
		Payload: payload,
	}

	var buf bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&buf, msg); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return buf.Bytes()
}

// TestBedrockEventStreamDecoderRegistered is the regression guard for the
// anthropic-sdk-go v1.75 bump. Before the bump the SDK's bedrock package
// registered this decoder from its init(); v1.75 removed that, and without our
// own registration ssestream falls back to a text/event-stream line scanner
// that yields zero events from binary frames — the stream then ends with no
// message_stop, and the response comes back empty ("Finished without output").
func TestBedrockEventStreamDecoderRegistered(t *testing.T) {
	var body bytes.Buffer
	events := []string{
		`{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	}
	for _, e := range events {
		body.Write(encodeBedrockChunk(t, e))
	}

	res := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{bedrockEventStreamContentType}},
		Body:       io.NopCloser(bytes.NewReader(body.Bytes())),
	}

	decoder := ssestream.NewDecoder(res)
	if decoder == nil {
		t.Fatal("NewDecoder returned nil")
	}

	var gotTypes []string
	for decoder.Next() {
		if evt := decoder.Event(); evt.Type != "" {
			gotTypes = append(gotTypes, evt.Type)
		}
	}
	if err := decoder.Err(); err != nil && err != io.EOF {
		t.Fatalf("decoder error: %v", err)
	}

	want := []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	if strings.Join(gotTypes, ",") != strings.Join(want, ",") {
		t.Errorf("decoded event types\n got: %v\nwant: %v", gotTypes, want)
	}
}

// TestBedrockEventStreamDecoderSurfacesException checks that an error frame
// becomes a decoder error rather than a silently truncated stream.
func TestBedrockEventStreamDecoderSurfacesException(t *testing.T) {
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.ExceptionMessageType)},
			{Name: eventstreamapi.ExceptionTypeHeader, Value: eventstream.StringValue("ThrottlingException")},
		},
		Payload: []byte(`{"message":"Too many requests"}`),
	}
	var buf bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&buf, msg); err != nil {
		t.Fatalf("encode frame: %v", err)
	}

	d := &bedrockEventStreamDecoder{rc: io.NopCloser(bytes.NewReader(buf.Bytes()))}
	if d.Next() {
		t.Fatal("Next() returned true on an exception frame")
	}
	err := d.Err()
	if err == nil {
		t.Fatal("expected an error from the exception frame")
	}
	if !strings.Contains(err.Error(), "ThrottlingException") || !strings.Contains(err.Error(), "Too many requests") {
		t.Errorf("error lost detail: %v", err)
	}
}
