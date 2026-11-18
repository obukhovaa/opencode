package service

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/agent"
)

// htmlBodyStart finds where an HTML or XML error page begins inside an
// error string. A proxy in front of the model (nginx, an ingress, a load
// balancer) answers a failed upstream with its own HTML page, and the
// provider SDK appends that body to its error verbatim: tags, a title,
// a server banner, over several lines. None of it is a reason a human
// can act on, and pasted into chat it reads as garbage.
var htmlBodyStart = regexp.MustCompile(`(?i)<(!doctype|html|head|body|\?xml)\b`)

// urlQuery matches a URL's query string or fragment. Query strings can
// carry signatures or tokens (pre-signed URLs, api_key=…), so they never
// travel to chat; the scheme, host and path stay, since they say which
// endpoint failed.
var urlQuery = regexp.MustCompile(`(https?://[^\s"'?#<>]+)[?#][^\s"'<>]*`)

// sanitizeRunError renders a failed run's error as the one-line reason
// shown in chat: on the progress card's terminal line and in the failure
// reply. It drops an HTML error page and anything after it, strips URL
// query strings, flattens the rest to one line and caps it at
// toolErrorPreviewRunes, like a failed tool call's reason. Empty for a
// nil error.
func sanitizeRunError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	htmlPage := false
	if loc := htmlBodyStart.FindStringIndex(s); loc != nil {
		s, htmlPage = s[:loc[0]], true
	}
	s = urlQuery.ReplaceAllString(s, "$1")
	s = strings.TrimRight(strings.TrimSpace(s), ":")
	s = truncateOneLine(s, toolErrorPreviewRunes)
	if s == "" && htmlPage {
		return "the model endpoint answered with an HTML error page"
	}
	return s
}

// isCancelledRun reports whether a run's terminal error is a
// cancellation (/abort, a session cancel, shutdown) rather than a
// failure. A cancellation has no reason to show and gets no failure
// reply: the person who cancelled already knows why it stopped.
func isCancelledRun(err error) bool {
	return errors.Is(err, agent.ErrRequestCancelled) || errors.Is(err, context.Canceled)
}

// runFailureLine is the reply posted when a run fails without producing
// any text to deliver, so the thread never ends on a bare status card
// (or, for a peer that cannot edit messages, on nothing at all).
func runFailureLine(reason string) string {
	if reason == "" {
		return "⚠️ The run failed. Resend your message to retry."
	}
	return "⚠️ The run failed: " + reason + " — resend your message to retry."
}
