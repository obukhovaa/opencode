package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/logging"
)

// progressMinInterval paces edits to a run's progress card. Slack's
// chat.update sits in rate tier 3 (about 50 calls per minute per
// channel); an agent finishing ten small reads in a second would be
// throttled, and the throttled edits are the ones carrying the newest
// count. Completions that land inside the interval collapse into one
// edit (see runProgress.wake), so the card is never more than one
// interval behind and never edits more often than this. A variable
// rather than a const so tests can shrink it — a test that does so MUST
// NOT call t.Parallel(), because newRunProgress reads this without
// synchronisation and a parallel mutator would race every other test
// that starts a run.
var progressMinInterval = 2 * time.Second

// progressFinishWait bounds how long the dispatcher waits for the final
// edit to flush before it goes back to serving inbound. One pacing
// interval plus a send is the expected case; the bound only matters
// when the platform is slow, and then the next run's card simply
// appears before this one's terminal state.
const progressFinishWait = 5 * time.Second

// Progress card terminal states.
const (
	progressStatusOK    = "ok"
	progressStatusError = "error"
)

// runProgress aggregates one agent run's tool activity into a single
// chat message per bound peer that is edited in place: "Thinking..."
// when the run starts, then the running count of completed tool calls,
// the tool in flight, elapsed time and any failure, and a terminal state
// when the run ends. It replaces one message per tool call at compact
// verbosity (see the chat-bridge spec, "Compact tool updates are one
// progress card per run, updated in place").
//
// Concurrency: counters are updated from the parts goroutine and read
// by a single flush worker (run), which also owns tokens. The worker is
// what keeps edits in order — one goroutine per event, as emitToolRender
// does for per-call cards, would let "8 tool calls done" land before "7".
type runProgress struct {
	startedAt time.Time
	interval  time.Duration
	now       func() time.Time

	mu          sync.Mutex
	toolsDone   int
	failed      int
	lastFailure string
	// pendingFailures holds every failure line not yet handed to a
	// flush. It is a QUEUE, not a single slot: two calls failing inside
	// one pacing interval collapse into one card edit (the card shows
	// only the most recent reason, by spec), but a text-only peer must
	// still receive BOTH lines — that is the "failures always surface"
	// invariant, and before the card existed each failure was its own
	// message. Drained by snapshot, one line per failure.
	pendingFailures []string
	inflight        map[string]string // raw tool-call ID → tool name
	inflightOrder   []string          // insertion order, for "most recently started"
	final           string            // "" while running, else progressStatusOK / progressStatusError
	finished        bool              // final snapshot taken; later updates are ignored

	// tokens maps a bound peer (progressPeerKey) to the message the card
	// lives in on that platform, plus the text last written to it.
	// Written and read by the worker only.
	tokens map[string]progressPeerCard

	// wake has capacity one so a burst of completions coalesces into a
	// single pending flush. done is closed when the worker exits, after
	// the terminal flush or on ctx cancellation.
	wake chan struct{}
	done chan struct{}

	// finalCh is closed by the first finish() call. The worker selects
	// on it during the pacing sleep so the TERMINAL flush does not wait
	// out a full interval: progressFinish blocks the session's inbound
	// dispatch loop until the card closes, and a run that ends while
	// the worker is mid-sleep would otherwise add up to one interval of
	// latency to every turn. Pacing still applies to running updates —
	// only the one terminal edit skips the wait.
	finalCh chan struct{}
}

func newRunProgress() *runProgress {
	return &runProgress{
		startedAt: time.Now(),
		interval:  progressMinInterval,
		now:       time.Now,
		inflight:  map[string]string{},
		tokens:    map[string]progressPeerCard{},
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		finalCh:   make(chan struct{}),
	}
}

// signal asks the worker to flush. Non-blocking; a pending signal
// already covers this update.
func (p *runProgress) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// toolStarted records a call as in flight. Idempotent for a repeated
// ID (streaming providers publish a ToolCall more than once).
func (p *runProgress) toolStarted(callID, name string) {
	if callID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	if _, seen := p.inflight[callID]; !seen {
		p.inflightOrder = append(p.inflightOrder, callID)
	}
	p.inflight[callID] = name
	p.signal()
}

// toolDone counts a completed call. reason is the failure's one-line,
// rune-capped body (empty on success); pairing is the "#id" suffix
// shown next to the tool name.
func (p *runProgress) toolDone(callID, name, pairing string, isError bool, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	if _, ok := p.inflight[callID]; ok {
		delete(p.inflight, callID)
		for i, id := range p.inflightOrder {
			if id == callID {
				p.inflightOrder = append(p.inflightOrder[:i], p.inflightOrder[i+1:]...)
				break
			}
		}
	}
	p.toolsDone++
	if isError {
		p.failed++
		p.lastFailure = "✗ " + name + pairing
		if reason != "" {
			p.lastFailure += " · " + reason
		}
		p.pendingFailures = append(p.pendingFailures, p.lastFailure)
	}
	p.signal()
}

// finish marks the run over with progressStatusOK or
// progressStatusError. The worker's next flush is the terminal one.
func (p *runProgress) finish(status string) {
	p.mu.Lock()
	first := p.final == ""
	if first {
		p.final = status
	}
	p.mu.Unlock()
	if first {
		// Closed once, under the same first-caller guard that sets
		// p.final, so a repeated finish cannot close a closed channel.
		close(p.finalCh)
	}
	p.signal()
}

// progressPeerCard is the card's message on one peer's platform, plus
// the text it currently displays. lastText exists so an edit that would
// not change anything is skipped: Telegram answers editMessageText with
// 400 "message is not modified" for a no-op edit, and deliverProgress
// treats any edit error as a stale token and posts the card fresh —
// turning a harmless no-op into a duplicate card. The same guard the
// queued-ack path already applies via its lastPosition check.
type progressPeerCard struct {
	token    bridge.EditableMessageToken
	lastText string
}

// progressFlush is one rendered state of the card, ready to send.
type progressFlush struct {
	// text is the whole card: the header line and, when a call has
	// failed, the most recent failure on a second line.
	text string
	// failureTexts carries every failure no earlier flush has delivered,
	// oldest first. Peers whose adapter cannot edit a message get
	// nothing else from the card, but they do get these lines — the
	// invariant that failures always surface. It is a slice because two
	// failures can land inside one pacing interval and both must reach
	// a text-only peer, even though the card itself only names the most
	// recent one.
	failureTexts []string
	final        bool
}

// snapshot renders the current state and marks it consumed: a carried
// failure is now "sent", and a terminal state freezes the counters.
func (p *runProgress) snapshot() progressFlush {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.final != "" {
		p.finished = true
	}
	f := progressFlush{text: p.headerLocked(), final: p.final != ""}
	if p.lastFailure != "" {
		f.text += "\n" + p.lastFailure
	}
	if len(p.pendingFailures) > 0 {
		f.failureTexts = p.pendingFailures
		p.pendingFailures = nil
	}
	return f
}

// headerLocked builds the card's first line. Caller holds p.mu.
func (p *runProgress) headerLocked() string {
	elapsed := formatElapsed(p.now().Sub(p.startedAt))
	calls := pluralToolCalls(p.toolsDone)
	failed := ""
	if p.failed > 0 {
		failed = " · " + strconv.Itoa(p.failed) + " failed"
	}
	switch p.final {
	case progressStatusOK:
		return "✓ Done · " + calls + failed + " · " + elapsed
	case progressStatusError:
		return "✗ Run failed · " + calls + failed + " · " + elapsed
	}
	if p.toolsDone == 0 && len(p.inflightOrder) == 0 {
		return "⏳ Thinking..."
	}
	running := ""
	switch n := len(p.inflightOrder); {
	case n == 1:
		// Last element: inflightOrder is insertion-ordered, and the
		// spec names the MOST RECENTLY started unfinished tool. With
		// exactly one in flight the two coincide, but indexing from the
		// front would be wrong the moment this branch widens.
		running = " · running " + p.inflight[p.inflightOrder[n-1]]
	case n > 1:
		running = " · " + strconv.Itoa(n) + " running"
	}
	return "⏳ " + calls + " done" + running + failed + " · " + elapsed
}

func pluralToolCalls(n int) string {
	if n == 1 {
		return "1 tool call"
	}
	return fmt.Sprintf("%d tool calls", n)
}

// formatElapsed renders a run's wall-clock age coarsely — a progress
// card is glanced at, not measured: "12s", "1m12s", "1h02m".
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

// run is the flush worker. It waits for a signal, renders a snapshot
// and hands it to deliver, and — unless that snapshot was terminal —
// sleeps one pacing interval before looking again, so a burst of
// signals costs one edit. Exits after the terminal flush or when ctx is
// cancelled; done is closed either way so finish's waiter never hangs.
func (p *runProgress) run(ctx context.Context, deliver func(progressFlush)) {
	defer close(p.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		}
		f := p.snapshot()
		deliver(f)
		if f.final {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-p.finalCh:
			// The run ended during the pacing sleep. Loop straight back
			// so the terminal flush goes out now rather than after the
			// remaining interval; finish() also signalled wake, so the
			// next select returns immediately.
		case <-time.After(p.interval):
		}
	}
}

// progressStart opens the run's progress card when compact tool updates
// are on. Returns nil when no card is wanted (tool updates disabled, or
// the live verbosity is full, which renders per-call cards instead).
// The decision is made once per run: the card then tracks every
// completion of this run regardless of later /verbosity switches.
func (d *sessionDispatch) progressStart(ctx context.Context) *runProgress {
	if d.svc.cfg == nil || !d.svc.cfg.ToolUpdatesEnabled {
		return nil
	}
	if d.svc.ToolVerbosity() != bridge.ToolUpdateVerbosityCompact {
		return nil
	}
	p := newRunProgress()
	d.progress.Store(p)
	d.svc.launchSupervisedCtx("dispatch-progress/"+d.sessionID, ctx, func(ctx context.Context) {
		p.run(ctx, func(f progressFlush) { d.deliverProgress(ctx, p, f) })
	})
	p.signal()
	return p
}

// progressFinish closes the card with the run's outcome and waits,
// bounded, for the terminal edit to flush so the next run's card does
// not overtake it.
func (d *sessionDispatch) progressFinish(p *runProgress, status string) {
	if p == nil {
		return
	}
	d.progress.CompareAndSwap(p, nil)
	p.finish(status)
	select {
	case <-p.done:
	case <-time.After(progressFinishWait):
		logging.Warn("bridge: progress card terminal edit did not flush in time",
			"session", d.sessionID)
	}
}

// deliverProgress puts one card state in front of every peer bound to
// the session. A peer whose adapter edits messages in place gets the
// card posted once and edited thereafter; if an edit fails (message
// deleted, token stale) the card is posted fresh and the chain
// continues from the new message. A peer whose adapter cannot edit
// (the external relay) is skipped, except for an undelivered failure,
// which reaches it as one plain-text line. Runs on the worker
// goroutine; ordering across flushes is the worker's guarantee.
func (d *sessionDispatch) deliverProgress(ctx context.Context, p *runProgress, f progressFlush) {
	bindings, err := d.svc.store.ListBindingsBySession(ctx, d.svc.projectID, d.sessionID)
	if err != nil {
		logging.Warn("bridge: progress card: list bindings failed", "session", d.sessionID, "err", err)
		return
	}
	// Keys currently owned by a live binding. A token may only be
	// migrated off a base key that no live binding still answers to —
	// otherwise a "<channel>|<thread>" peer would steal the card of a
	// bare-"<channel>" peer bound to the same session in that channel.
	live := make(map[string]struct{}, len(bindings))
	for _, b := range bindings {
		live[progressPeerKey(b)] = struct{}{}
	}
	for _, b := range bindings {
		peer := bridge.PeerRef{Channel: b.Channel, Identity: b.IdentityID, PeerID: b.PeerID}
		adapter := d.svc.Adapter(b.Channel, b.IdentityID)
		if adapter == nil {
			continue
		}
		editor, ok := adapter.(bridge.MessageEditor)
		if !ok {
			for _, line := range f.failureTexts {
				if _, err := d.svc.Send(ctx, peer, line, "", nil); err != nil {
					logging.Warn("bridge: progress card: failure line send failed",
						"session", d.sessionID, "peer", b.PeerID, "err", err)
				}
			}
			continue
		}
		key := progressPeerKey(b)
		card, ok := p.tokens[key]
		if !ok {
			// The binding's peer_id may have gained its thread suffix
			// since the card was posted: sendToOnePeer rewrites a
			// channel-form peer to "<channel>|<thread>" on the first
			// outbound that resolves one (Slack thread ts, Mattermost
			// root post), and the agent's own reply does exactly that
			// before this run's terminal flush. Re-find the card under
			// the stable channel part and migrate it, or the terminal
			// edit posts a second card and strands the first.
			if base := progressPeerBaseKey(b); base != key {
				if _, stillOwned := live[base]; !stillOwned {
					if card, ok = p.tokens[base]; ok {
						delete(p.tokens, base)
						p.tokens[key] = card
					}
				}
			}
		}
		if ok {
			if card.lastText == f.text {
				// Nothing changed; editing would be a no-op at best and
				// a Telegram 400 at worst.
				continue
			}
			if err := editor.EditMessage(ctx, peer, card.token, f.text); err == nil {
				card.lastText = f.text
				p.tokens[key] = card
				continue
			} else {
				logging.Warn("bridge: progress card edit failed, posting fresh",
					"session", d.sessionID, "peer", b.PeerID, "err", err)
			}
		}
		tok, err := editor.SendEditable(ctx, peer, f.text)
		if err != nil {
			logging.Warn("bridge: progress card post failed",
				"session", d.sessionID, "peer", b.PeerID, "err", err)
			continue
		}
		p.tokens[key] = progressPeerCard{token: tok, lastText: f.text}
	}
}

func progressPeerKey(b store.Binding) string {
	return b.Channel + ":" + b.IdentityID + ":" + b.PeerID
}

// progressPeerBaseKey is progressPeerKey with any thread/root-post
// suffix dropped. Slack and Mattermost peer IDs are "<channel>|<thread>"
// once resolved and a bare channel before that; Telegram peer IDs carry
// no suffix, so this is identity there.
func progressPeerBaseKey(b store.Binding) string {
	peerID := b.PeerID
	if i := strings.IndexByte(peerID, '|'); i >= 0 {
		peerID = peerID[:i]
	}
	return b.Channel + ":" + b.IdentityID + ":" + peerID
}
