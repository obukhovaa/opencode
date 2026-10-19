package provider

import (
	"context"
	"errors"
	"net"
	"strings"
)

// retriedTransientSignatures are lowercased substrings of surfaced error
// messages that indicate a transient LLM-provider / gateway failure the
// provider client already exhausted its in-call retry budget on: a rate limit,
// an overload or an upstream blip that is expected to clear on its own.
// Matching is on the message because the typed provider error is often
// re-wrapped (shouldRetry's "maximum retry attempts reached …") before it
// leaves the client. A peer-initiated HTTP/2 RST_STREAM is deliberately not
// here: it needs two co-occurring markers (isRetryableRSTStreamError), and a
// proxy can surface a permanent error, an oversized request included, as one.
var retriedTransientSignatures = []string{
	"http 429",                       // provider retry budget exhausted on a 429
	"maximum retry attempts reached", // retry-exhaustion wrapper (shouldRetry)
	"rate limit",
	"rate_limit",
	"too many requests",
	"overloaded",                  // Anthropic 529
	"service unavailable",         // 503
	"serviceunavailableexception", // Bedrock 503
	"throttlingexception",         // Bedrock 429
	"modeltimeoutexception",       // Bedrock 408
	"internalserverexception",     // Bedrock 500
}

// IsRetriedTransientError reports whether err is a transient provider or
// gateway failure of a class the provider clients retry (rate limit, overload,
// Bedrock's transient exceptions), surfaced once that retry budget ran out.
// The flow runner parks a step on it (postpone-and-auto-resume); the agent
// loop does not read it as a sign of a context overflow.
func IsRetriedTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range retriedTransientSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// timeoutSignatures are lowercased substrings of timeout errors that lost
// their net.Error type on the way out of a provider client.
var timeoutSignatures = []string{
	"client.timeout exceeded", // net/http Client.Timeout
	"timeout awaiting response headers",
	"tls handshake timeout",
	"i/o timeout",
	"context deadline exceeded",
}

// IsTimeoutError reports whether err is a timeout of the model call: the HTTP
// client's, a deadline on the request, or the stream inactivity watchdog
// (ErrStreamStalled).
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStreamStalled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range timeoutSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
