package provider

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// newAPIErrWithStatus constructs an *anthropic.Error suitable for
// driving shouldRetry. The Anthropic SDK's Error struct exposes only
// StatusCode + Response; Request is irrelevant for the retry-gate
// decision (it's used for the Error() string but shouldRetry only
// reads StatusCode + Header).
func newAPIErrWithStatus(status int, header http.Header) error {
	if header == nil {
		header = http.Header{}
	}
	return &anthropic.Error{
		StatusCode: status,
		Response:   &http.Response{Header: header, StatusCode: status},
	}
}

// TestShouldRetry_BedrockServiceUnavailableRetries pins the fix for
// the AWS Bedrock `serviceUnavailableException` failure mode — when
// Bedrock returns HTTP 503 mid-flow ("Bedrock is unable to process
// your request"), the call MUST retry instead of bubbling the error
// up to the flow runner and failing the whole step.
func TestShouldRetry_BedrockServiceUnavailableRetries(t *testing.T) {
	t.Parallel()
	a := &anthropicClient{}
	err := newAPIErrWithStatus(http.StatusServiceUnavailable, nil)
	retry, after, retryErr := a.shouldRetry(1, err)
	if !retry {
		t.Fatalf("503 must trigger retry; got retry=%v retryErr=%v", retry, retryErr)
	}
	if retryErr != nil {
		t.Errorf("retryErr should be nil on a retryable status; got %v", retryErr)
	}
	if after <= 0 {
		t.Errorf("backoff must be positive; got %d", after)
	}
}

// TestShouldRetry_RateLimitStillRetries pins the legacy 429/529 path
// so the broadened retry whitelist doesn't accidentally drop it.
func TestShouldRetry_RateLimitStillRetries(t *testing.T) {
	t.Parallel()
	a := &anthropicClient{}
	for _, status := range []int{http.StatusTooManyRequests, 529} {
		err := newAPIErrWithStatus(status, nil)
		retry, _, retryErr := a.shouldRetry(1, err)
		if !retry {
			t.Errorf("status %d must still trigger retry; retryErr=%v", status, retryErr)
		}
	}
}

// TestShouldRetry_NonTransientDoesNotRetry pins that genuinely
// unrecoverable status codes (400 bad request, 401 unauthorized, 500
// — the catch-all server error that often signals a real bug, not an
// overload) still bubble up to the caller. Adding 503 to the whitelist
// must not loosen this.
func TestShouldRetry_NonTransientDoesNotRetry(t *testing.T) {
	t.Parallel()
	a := &anthropicClient{}
	for _, status := range []int{400, 401, 403, 404, 500, 502, 504} {
		err := newAPIErrWithStatus(status, nil)
		retry, _, retryErr := a.shouldRetry(1, err)
		if retry {
			t.Errorf("status %d must NOT retry", status)
		}
		if !errors.Is(retryErr, err) {
			t.Errorf("status %d: returned err should be the original; got %v", status, retryErr)
		}
	}
}

// TestShouldRetry_MaxRetriesExhausted pins the bounded-retry contract:
// the gate must give up cleanly with a descriptive error after
// maxRetries attempts so the flow step can fail loudly rather than
// silently loop forever.
func TestShouldRetry_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()
	a := &anthropicClient{}
	err := newAPIErrWithStatus(http.StatusServiceUnavailable, nil)
	retry, _, retryErr := a.shouldRetry(maxRetries+1, err)
	if retry {
		t.Fatal("retry must NOT be allowed past maxRetries")
	}
	if retryErr == nil || !strings.Contains(retryErr.Error(), "maximum retry attempts") {
		t.Errorf("retryErr should describe exhaustion; got %v", retryErr)
	}
}

// TestShouldRetry_HonorsRetryAfterHeader pins that an explicit
// Retry-After header from upstream overrides the default exponential
// backoff. The conversion is seconds → milliseconds.
func TestShouldRetry_HonorsRetryAfterHeader(t *testing.T) {
	t.Parallel()
	a := &anthropicClient{}
	h := http.Header{}
	h.Set("Retry-After", "7")
	err := newAPIErrWithStatus(http.StatusTooManyRequests, h)
	retry, after, retryErr := a.shouldRetry(1, err)
	if !retry || retryErr != nil {
		t.Fatalf("retry should be allowed; got retry=%v err=%v", retry, retryErr)
	}
	if after != 7000 {
		t.Errorf("Retry-After=7 should produce 7000ms backoff; got %d", after)
	}
}
