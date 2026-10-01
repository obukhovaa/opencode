package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

// intermediateTextGlyph prefixes the header of a relayed intermediate
// assistant message. Unicode rather than the `:hourglass:` shortcode:
// Telegram and Mattermost would print the shortcode literally. Distinct
// from the ⏳ glyph the queued-ack and the progress card use.
const intermediateTextGlyph = "⌛"

// intermediateFlushWait bounds how long the question router waits for the
// text that introduces a question widget before it posts the widget
// anyway, and how long the final reply waits for the run's in-flight
// intermediate posts. Only the wait is bounded: the post carries on under
// intermediateSendTimeout. A variable so tests can shrink it.
var intermediateFlushWait = 5 * time.Second

// intermediateSendTimeout bounds one intermediate-text post, and
// intermediateUploadTimeout one that carries FILE: attachments. The parts
// path posts synchronously and slack-go's default HTTP client has no
// timeout, so without a bound a hung chat API would stall every tool
// update of the session and overflow d.parts. Variables so tests can
// shrink them.
var (
	intermediateSendTimeout   = 10 * time.Second
	intermediateUploadTimeout = 60 * time.Second
)

// textClaim marks one assistant message as owned by whichever path
// posts it to chat; done is closed once that post has returned.
type textClaim struct{ done chan struct{} }

// runTextGuard records, for one bridge-dispatched run, which assistant
// messages have already been relayed. Three goroutines can try to post
// the same message — the run goroutine (terminal event), the parts
// goroutine (intermediate ToolUse message) and the question router
// (flush before a widget) — so the claim is atomic.
type runTextGuard struct {
	// sinceMs is the wall-clock time (unix millis) taken just before the
	// Run attempt that started this run. handleInbound subscribes to parts
	// before its ErrSessionBusy retry loop, so the subscription also
	// buffers the parts of another actor's run on the session (a task
	// auto-resume, an API run) that held it meanwhile. Those are stamped
	// earlier and are not relayed.
	sinceMs int64
	m       sync.Map // messageID -> *textClaim
}

func newRunTextGuard(sinceMs int64) *runTextGuard { return &runTextGuard{sinceMs: sinceMs} }

// claim returns the claim for id and whether this caller created it. The
// winner posts and must close c.done; losers return or wait on c.done.
func (g *runTextGuard) claim(id string) (c *textClaim, won bool) {
	fresh := &textClaim{done: make(chan struct{})}
	v, loaded := g.m.LoadOrStore(id, fresh)
	return v.(*textClaim), !loaded
}

func (g *runTextGuard) lookup(id string) (*textClaim, bool) {
	v, ok := g.m.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*textClaim), true
}

func (g *runTextGuard) has(id string) bool {
	_, ok := g.m.Load(id)
	return ok
}

// waitInFlight waits until every claim taken so far has been released,
// for at most wait in total, so the final reply does not land above an
// intermediate text that is still being posted. A message the parts
// goroutine has not reached yet is not waited for.
func (g *runTextGuard) waitInFlight(ctx context.Context, wait time.Duration) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	g.m.Range(func(_, v any) bool {
		select {
		case <-v.(*textClaim).done:
			return true
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	})
}

// intermediateTextHeader renders "⌛ bash, question" from the message's
// tool calls in call order. Duplicate names are kept: two bash calls
// read as "bash, bash", which is what the model actually asked for.
func intermediateTextHeader(calls []message.ToolCall) string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Name)
	}
	return intermediateTextGlyph + " " + strings.Join(names, ", ")
}

// endsInToolUse reports whether msg is an assistant message that ended in
// tool_use, the only kind the intermediate path relays.
func endsInToolUse(msg message.Message) bool {
	return msg.Role == message.Assistant && msg.FinishReason() == message.FinishReasonToolUse
}

// postIntermediateText claims and relays the text of an assistant message
// that ended in tool_use, on the guard g of the run the message belongs
// to. The send is synchronous so a caller on the parts goroutine gets the
// text into chat before the tool card it emits next. A message with no
// text is claimed but not posted, so its remaining ToolCall parts skip
// the store read. No-op when g is nil (no bridge run).
func (d *sessionDispatch) postIntermediateText(ctx context.Context, g *runTextGuard, msg message.Message) {
	if g == nil || !endsInToolUse(msg) {
		return
	}
	c, won := g.claim(msg.ID)
	if !won {
		return
	}
	defer close(c.done)
	d.sendIntermediateText(ctx, msg)
}

// sendIntermediateText posts msg's text under its "⌛ <tools>" header,
// bounded by intermediateSendTimeout (intermediateUploadTimeout when it
// carries attachments). The caller holds the message's claim.
func (d *sessionDispatch) sendIntermediateText(ctx context.Context, msg message.Message) {
	if ctx == nil {
		ctx = context.Background()
	}
	mediaRoot, _ := d.svc.MediaDir()
	clean, atts, unsafe := ParseFileTokens(agentMessageText(msg), mediaRoot)
	if clean == "" && len(atts) == 0 {
		return
	}
	if len(unsafe) > 0 {
		logging.Warn("bridge: dropped unsafe FILE: paths from agent output",
			"session", d.sessionID, "paths", unsafe)
	}

	timeout := intermediateSendTimeout
	if len(atts) > 0 {
		timeout = intermediateUploadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := bridge.Outbound{
		Text:        intermediateTextHeader(msg.ToolCalls()) + "\n" + clean,
		Attachments: atts,
	}
	results, err := d.svc.SendBySessionID(ctx, d.sessionID, out)
	if err != nil {
		logging.Warn("bridge: intermediate-text fan-out failed",
			"session", d.sessionID, "err", err)
		return
	}
	for _, r := range results {
		if !r.Delivered && r.Err != nil {
			logging.Info("bridge: per-peer delivery failed",
				"session", d.sessionID, "peer", r.Binding.PeerID, "err", r.Err)
		}
	}
}

// relayIntermediateTextForPart is the parts-path trigger, on the guard g
// of the run whose subscription forwarded the part. Only the EventComplete
// republish of a ToolCall (Finished with its merged Input) qualifies: by
// then processEvent has stored the message with its text, every tool call
// and the tool_use Finish part. The earlier streaming publish (Finished,
// Input still "") arrives before the trailing calls have streamed and
// would yield an incomplete header. Subagent sessions are skipped — their
// text is not this conversation's — and so are parts published before the
// run started (runTextGuard.sinceMs).
func (d *sessionDispatch) relayIntermediateTextForPart(ev message.PartEvent, part message.ToolCall, g *runTextGuard) {
	if !part.Finished || part.Input == "" || ev.SessionID != d.sessionID || ev.MessageID == "" {
		return
	}
	if g == nil || ev.Time < g.sinceMs || g.has(ev.MessageID) {
		return
	}
	if d.svc.app == nil || d.svc.app.Messages == nil {
		return
	}
	ctx := d.svc.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	msg, err := d.svc.app.Messages.Get(ctx, ev.MessageID)
	if err != nil {
		logging.Debug("bridge: intermediate-text message lookup failed",
			"session", d.sessionID, "message", ev.MessageID, "err", err)
		return
	}
	d.postIntermediateText(ctx, g, msg)
}

// flushIntermediateText posts the text of the session's latest assistant
// message before a question widget goes out. The question tool runs on
// its own goroutine and races the parts goroutine that would otherwise
// post that text, so without the flush the widget can land above the
// text that introduces it. The latest assistant message is the one that
// owns the pending question call: the tool-result message is only
// created after the tool returns.
//
// This runs on the question router, the one goroutine that serves every
// session's questions, so the wait is bounded by intermediateFlushWait: a
// slow chat API delays this session's widget by at most that much. The
// post itself is claimed here and sent on a supervised goroutine under
// the service context, so a send slower than the wait is not cancelled.
// Cancelling it would leave the claim taken and the text lost for good,
// since the parts path skips a claimed message.
//
// Looks the dispatcher up without creating one, and does nothing unless
// a bridge-dispatched run is in flight on it.
func (s *Service) flushIntermediateText(ctx context.Context, sessionID string) {
	if s == nil || s.app == nil || s.app.Messages == nil {
		return
	}
	s.dispatchMu.Lock()
	d := s.dispatchers[sessionID]
	s.dispatchMu.Unlock()
	if d == nil {
		return
	}
	g := d.textGuard.Load()
	if g == nil {
		return
	}
	msgs, err := s.app.Messages.ListLatest(ctx, sessionID, 2)
	if err != nil {
		logging.Debug("bridge: intermediate-text flush lookup failed",
			"session", sessionID, "err", err)
		return
	}
	// ListLatest returns oldest first.
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != message.Assistant {
			continue
		}
		c, claimed := g.lookup(msg.ID)
		if !claimed {
			if !endsInToolUse(msg) {
				return
			}
			var won bool
			if c, won = g.claim(msg.ID); won {
				s.launchSupervised("intermediate-text/"+sessionID, func(ctx context.Context) {
					defer close(c.done)
					d.sendIntermediateText(ctx, msg)
				})
			}
		}
		select {
		case <-c.done:
		case <-time.After(intermediateFlushWait):
			logging.Warn("bridge: timed out waiting for intermediate text before question",
				"session", sessionID, "message", msg.ID)
		case <-ctx.Done():
		}
		return
	}
}
