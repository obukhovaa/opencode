package flow

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsTransientProviderError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// The two errors observed on real MICRO-1014 runs:
		{"429 retry exhausted", errors.New("maximum retry attempts reached for HTTP 429: 8 retries"), true},
		{"http2 RST INTERNAL_ERROR", errors.New("stream error: stream ID 147; INTERNAL_ERROR; received from peer"), true},
		// Other transient shapes.
		{"anthropic overloaded", errors.New("Overloaded"), true},
		{"bedrock throttling", errors.New("received exception ThrottlingException: rate exceeded"), true},
		{"bedrock 503", errors.New("ServiceUnavailableException: Bedrock is unable to process"), true},
		{"wrapped 429", fmt.Errorf("step %q failed: %w", "implement", errors.New("maximum retry attempts reached for HTTP 429: 8 retries")), true},
		// Must NOT match: a local stream error not from the peer, or unrelated failures.
		{"local stream error, not peer", errors.New("stream error: stream ID 5; CANCEL; sent by client"), false},
		{"plain build failure", errors.New("go build failed: undefined: Foo"), false},
		{"generic error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientProviderError(tt.err); got != tt.want {
				t.Fatalf("isTransientProviderError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStepPostponesOnProviderError(t *testing.T) {
	dur := func(s string) *string { return &s }
	tests := []struct {
		name string
		step Step
		want bool
	}{
		{"no resume_after", Step{ID: "resolve-team"}, false},
		{"resume_after set", Step{ID: "implement", ResumeAfter: dur("30m")}, true},
		{"resume_after blank", Step{ID: "x", ResumeAfter: dur("   ")}, false},
		{"resume_after empty string", Step{ID: "x", ResumeAfter: dur("")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stepPostponesOnProviderError(tt.step); got != tt.want {
				t.Fatalf("stepPostponesOnProviderError(%+v) = %v, want %v", tt.step, got, tt.want)
			}
		})
	}
}
