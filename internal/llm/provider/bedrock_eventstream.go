package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
	"github.com/tidwall/gjson"
)

// Bedrock streams responses as binary AWS EventStream frames
// (Content-Type "application/vnd.amazon.eventstream"), not as SSE. The SDK's
// ssestream package picks a decoder by exact Content-Type match and otherwise
// falls back to a line scanner that looks for "data:" prefixes — which finds
// nothing at all in binary frames, yielding a stream that ends with zero
// events and no MessageStopEvent.
//
// anthropic-sdk-go <= v1.37 registered an EventStream decoder for that
// Content-Type from the bedrock package's init(), so merely importing the
// package was enough. v1.75 removed that init() and moved the translation
// inside the middleware installed by bedrock.WithConfig. We cannot use
// WithConfig: it matches the Messages path exactly (breaking proxy base URLs
// that carry a path prefix) and it adds SigV4 signing, while we reach Bedrock
// through the LiteLLM proxy with a bearer key — which is why bedrockMiddleware
// in bedrock.go is hand-rolled in the first place.
//
// So register the decoder ourselves, restoring the pre-v1.75 behaviour that
// bedrockMiddleware's Content-Type normalisation already assumes.
func init() {
	ssestream.RegisterDecoder(bedrockEventStreamContentType, func(rc io.ReadCloser) ssestream.Decoder {
		return &bedrockEventStreamDecoder{rc: rc}
	})
}

const bedrockEventStreamContentType = "application/vnd.amazon.eventstream"

// bedrockEventStreamChunk is the envelope Bedrock wraps each Anthropic event
// in: the event JSON, base64-encoded, under "bytes".
type bedrockEventStreamChunk struct {
	Bytes string `json:"bytes"`
	P     string `json:"p"`
}

// bedrockEventStreamDecoder adapts AWS EventStream frames to ssestream.Event
// values, so the rest of the Anthropic streaming path is unchanged.
type bedrockEventStreamDecoder struct {
	eventstream.Decoder

	rc  io.ReadCloser
	evt ssestream.Event
	err error
}

var _ ssestream.Decoder = (*bedrockEventStreamDecoder)(nil)

func (d *bedrockEventStreamDecoder) Close() error { return d.rc.Close() }

func (d *bedrockEventStreamDecoder) Err() error { return d.err }

func (d *bedrockEventStreamDecoder) Event() ssestream.Event { return d.evt }

func (d *bedrockEventStreamDecoder) Next() bool {
	if d.err != nil {
		return false
	}

	msg, err := d.Decoder.Decode(d.rc, nil)
	if err != nil {
		// io.EOF ends the stream normally. It surfaces through Err() as-is,
		// like the SDK's pre-v1.75 decoder; the stream consumer in
		// anthropic.go treats io.EOF as a clean end.
		d.err = err
		return false
	}

	messageType := msg.Headers.Get(eventstreamapi.MessageTypeHeader)
	if messageType == nil {
		d.err = fmt.Errorf("%s event header not present", eventstreamapi.MessageTypeHeader)
		return false
	}

	switch messageType.String() {
	case eventstreamapi.EventMessageType:
		eventType := msg.Headers.Get(eventstreamapi.EventTypeHeader)
		if eventType == nil {
			d.err = fmt.Errorf("%s event header not present", eventstreamapi.EventTypeHeader)
			return false
		}
		if eventType.String() != "chunk" {
			// Unknown frame kind: skip it rather than ending the stream, so a
			// future Bedrock-side addition cannot truncate a response.
			d.evt = ssestream.Event{}
			return true
		}

		chunk := bedrockEventStreamChunk{}
		if err := json.Unmarshal(msg.Payload, &chunk); err != nil {
			d.err = err
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(chunk.Bytes)
		if err != nil {
			d.err = err
			return false
		}
		d.evt = ssestream.Event{
			Type: gjson.GetBytes(decoded, "type").String(),
			Data: decoded,
		}
		return true

	case eventstreamapi.ExceptionMessageType:
		exceptionType := msg.Headers.Get(eventstreamapi.ExceptionTypeHeader)
		if exceptionType == nil {
			d.err = fmt.Errorf("%s event header not present", eventstreamapi.ExceptionTypeHeader)
			return false
		}
		var errInfo struct {
			Code    string
			Type    string `json:"__type"`
			Message string
		}
		if err := json.Unmarshal(msg.Payload, &errInfo); err != nil && err != io.EOF {
			d.err = fmt.Errorf("received exception %s: parsing exception payload failed: %w", exceptionType.String(), err)
			return false
		}

		errorCode := "UnknownError"
		errorMessage := errorCode
		if ev := exceptionType.String(); len(ev) > 0 {
			errorCode = ev
		} else if len(errInfo.Code) > 0 {
			errorCode = errInfo.Code
		} else if len(errInfo.Type) > 0 {
			errorCode = errInfo.Type
		}
		if len(errInfo.Message) > 0 {
			errorMessage = errInfo.Message
		}
		d.err = fmt.Errorf("received exception %s: %s", errorCode, errorMessage)
		return false

	case eventstreamapi.ErrorMessageType:
		errorCode := "UnknownError"
		errorMessage := errorCode
		if header := msg.Headers.Get(eventstreamapi.ErrorCodeHeader); header != nil {
			errorCode = header.String()
		}
		if header := msg.Headers.Get(eventstreamapi.ErrorMessageHeader); header != nil {
			errorMessage = header.String()
		}
		d.err = fmt.Errorf("received error %s: %s", errorCode, errorMessage)
		return false
	}

	// Unrecognised message type: skip without ending the stream.
	d.evt = ssestream.Event{}
	return true
}
