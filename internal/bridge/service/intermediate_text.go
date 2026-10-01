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

// intermediateFlushWait bounds how long the question router waits for an
// in-flight intermediate post of the same message before it posts the
// question widget anyway.
const intermediateFlushWait = 5 * time.Second

// textClaim marks one assistant message as owned by whichever path
// posts it to chat; done is closed once that post has returned.
type textClaim struct{ done chan struct{} }

// runTextGuard records, for one bridge-dispatched run, which assistant
// messages have already been relayed. Three goroutines can try to post
// the same message — the run goroutine (terminal event), the parts
// goroutine (intermediate ToolUse message) and the question router
// (flush before a widget) — so the claim is atomic.
type runTextGuard struct {
	m sync.Map // messageID -> *textClaim
}

func newRunTextGuard() *runTextGuard { return &runTextGuard{} }

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

// postIntermediateText relays the text of an assistant message that ended
// in tool_use, under a header naming the tools it called. The send is
// synchronous so a caller on the parts goroutine gets the text into chat
// before the tool card it emits next. A message with no text is neither
// posted nor claimed. No-op when no bridge run is in flight.
func (d *sessionDispatch) postIntermediateText(ctx context.Context, msg message.Message) {
	g := d.textGuard.Load()
	if g == nil {
		return
	}
	if msg.Role != message.Assistant || msg.FinishReason() != message.FinishReasonToolUse {
		return
	}
	mediaRoot, _ := d.svc.MediaDir()
	clean, atts, unsafe := ParseFileTokens(agentMessageText(msg), mediaRoot)
	if clean == "" && len(atts) == 0 {
		return
	}
	c, won := g.claim(msg.ID)
	if !won {
		return
	}
	defer close(c.done)
	if len(unsafe) > 0 {
		logging.Warn("bridge: dropped unsafe FILE: paths from agent output",
			"session", d.sessionID, "paths", unsafe)
	}

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

// relayIntermediateTextForPart is the parts-path trigger. Only the
// EventComplete republish of a ToolCall (Finished with its merged Input)
// qualifies: by then processEvent has stored the message with its text,
// every tool call and the tool_use Finish part. The earlier streaming
// publish (Finished, Input still "") arrives before the trailing calls
// have streamed and would yield an incomplete header. Subagent sessions
// are skipped — their text is not this conversation's.
func (d *sessionDispatch) relayIntermediateTextForPart(ev message.PartEvent, part message.ToolCall) {
	if !part.Finished || part.Input == "" || ev.SessionID != d.sessionID || ev.MessageID == "" {
		return
	}
	g := d.textGuard.Load()
	if g == nil || g.has(ev.MessageID) {
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
	d.postIntermediateText(ctx, msg)
}

// flushIntermediateText posts the text of the session's latest assistant
// message before a question widget goes out. The question tool runs on
// its own goroutine and races the parts goroutine that would otherwise
// post that text, so without the flush the widget can land above the
// text that introduces it. The latest assistant message is the one that
// owns the pending question call: the tool-result message is only
// created after the tool returns.
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
		if c, ok := g.lookup(msg.ID); ok {
			select {
			case <-c.done:
			case <-time.After(intermediateFlushWait):
				logging.Warn("bridge: timed out waiting for intermediate text before question",
					"session", sessionID, "message", msg.ID)
			case <-ctx.Done():
			}
			return
		}
		d.postIntermediateText(ctx, msg)
		return
	}
}
