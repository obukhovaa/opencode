package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "dial tcp 10.0.0.1:443: i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestIsRetriedTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"anthropic 429 after retries", errors.New("maximum retry attempts reached for HTTP 429: 8 retries"), true},
		{"openai rate limit after retries", errors.New("maximum retry attempts reached for rate limit: 8 retries"), true},
		{"anthropic overloaded", errors.New("Overloaded"), true},
		{"bedrock throttling", errors.New("received exception ThrottlingException: rate exceeded"), true},
		{"wrapped", fmt.Errorf("failed to process events: %w", errors.New("ServiceUnavailableException")), true},
		{"peer RST_STREAM is not in this class", errors.New("stream error: stream ID 147; INTERNAL_ERROR; received from peer"), false},
		{"overflow", errors.New("prompt is too long: 212000 tokens > 200000 maximum"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetriedTransientError(tt.err); got != tt.want {
				t.Fatalf("IsRetriedTransientError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsTimeoutError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"stalled stream", fmt.Errorf("stream: %w", ErrStreamStalled), true},
		{"deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), true},
		{"net.Error timeout", &url.Error{Op: "Post", URL: "https://x", Err: timeoutErr{}}, true},
		{"timeout text without its type", errors.New(`Post "https://x": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`), true},
		{"cancellation is not a timeout", context.Canceled, false},
		{"stream reset", errors.New("unexpected EOF"), false},
		{"overflow", errors.New("prompt is too long"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeoutError(tt.err); got != tt.want {
				t.Fatalf("IsTimeoutError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
