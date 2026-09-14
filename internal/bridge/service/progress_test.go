package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// fixedClock returns a runProgress whose clock is pinned so header text
// is deterministic.
func fixedClock(elapsed time.Duration) *runProgress {
	p := newRunProgress()
	start := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	p.startedAt = start
	p.now = func() time.Time { return start.Add(elapsed) }
	return p
}

// TestRunProgress_HeaderStates pins the card text at each stage of a run:
// the reporter asked for "Thinking..." first and the real tool-call
// count after that, with a terminal line when the run ends.
func TestRunProgress_HeaderStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(p *runProgress)
		want  string
	}{
		{
			name:  "fresh run is thinking",
			setup: func(p *runProgress) {},
			want:  "⏳ Thinking...",
		},
		{
			name: "first call in flight",
			setup: func(p *runProgress) {
				p.toolStarted("toolu_1", "bash")
			},
			want: "⏳ 0 tool calls done · running bash · 1m12s",
		},
		{
			name: "one call done",
			setup: func(p *runProgress) {
				p.toolStarted("toolu_1", "bash")
				p.toolDone("toolu_1", "bash", "#toolu_1", false, "")
			},
			want: "⏳ 1 tool call done · 1m12s",
		},
		{
			name: "several done, two in flight",
			setup: func(p *runProgress) {
				for i := 0; i < 5; i++ {
					id := "toolu_" + string(rune('a'+i))
					p.toolStarted(id, "read")
					p.toolDone(id, "read", "#"+id, false, "")
				}
				p.toolStarted("toolu_x", "bash")
				p.toolStarted("toolu_y", "grep")
			},
			want: "⏳ 5 tool calls done · 2 running · 1m12s",
		},
		{
			name: "failure adds a count",
			setup: func(p *runProgress) {
				p.toolDone("toolu_1", "read", "#a1b2c3", false, "")
				p.toolDone("toolu_2", "bash", "#d4e5f6", true, "permission denied exit 1")
			},
			want: "⏳ 2 tool calls done · 1 failed · 1m12s",
		},
		{
			name: "finished ok",
			setup: func(p *runProgress) {
				for i := 0; i < 8; i++ {
					p.toolDone("id", "read", "#id", false, "")
				}
				p.finish(progressStatusOK)
			},
			want: "✓ Done · 8 tool calls · 1m12s",
		},
		{
			name: "finished with agent error",
			setup: func(p *runProgress) {
				p.toolDone("id", "read", "#id", false, "")
				p.toolDone("id2", "bash", "#id2", true, "boom")
				p.finish(progressStatusError)
			},
			want: "✗ Run failed · 2 tool calls · 1 failed · 1m12s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fixedClock(72 * time.Second)
			tt.setup(p)
			f := p.snapshot()
			header := strings.SplitN(f.text, "\n", 2)[0]
			if header != tt.want {
				t.Errorf("header = %q; want %q", header, tt.want)
			}
		})
	}
}

// TestRunProgress_FailureLineAndFallback: a failure appears as the card's
// second line, and exactly one flush carries it as plain text for peers
// that cannot edit messages.
func TestRunProgress_FailureLineAndFallback(t *testing.T) {
	t.Parallel()
	p := fixedClock(3 * time.Second)
	p.toolDone("toolu_1", "bash", "#a1b2c3", true, "permission denied exit 1")

	f := p.snapshot()
	wantLine := "✗ bash#a1b2c3 · permission denied exit 1"
	if !strings.HasSuffix(f.text, "\n"+wantLine) {
		t.Errorf("card text = %q; want second line %q", f.text, wantLine)
	}
	if len(f.failureTexts) != 1 || f.failureTexts[0] != wantLine {
		t.Errorf("failureTexts = %q; want exactly [%q] (first flush after a failure carries it)", f.failureTexts, wantLine)
	}

	// The next flush still shows the failure on the card but does not
	// re-send it as text.
	p.toolDone("toolu_2", "read", "#z", false, "")
	f2 := p.snapshot()
	if !strings.Contains(f2.text, wantLine) {
		t.Errorf("second flush lost the failure line: %q", f2.text)
	}
	if len(f2.failureTexts) != 0 {
		t.Errorf("failureTexts on second flush = %q; want empty", f2.failureTexts)
	}
}

// TestRunProgress_AllFailuresInOneIntervalSurface: two calls failing between
// two flushes collapse into ONE card edit (the card names only the most
// recent reason, by spec) but BOTH lines must be handed to the flush, so a
// text-only peer — the external relay, which cannot edit a message — still
// sees every failure. Regression: a single lastFailure slot dropped the
// first one silently.
func TestRunProgress_AllFailuresInOneIntervalSurface(t *testing.T) {
	t.Parallel()
	p := fixedClock(5 * time.Second)
	p.toolDone("toolu_1", "curl", "#aaa", true, "connection refused")
	p.toolDone("toolu_2", "bash", "#bbb", true, "exit status 2")

	f := p.snapshot()
	want := []string{
		"✗ curl#aaa · connection refused",
		"✗ bash#bbb · exit status 2",
	}
	if len(f.failureTexts) != len(want) {
		t.Fatalf("failureTexts = %q; want both failures %q", f.failureTexts, want)
	}
	for i, w := range want {
		if f.failureTexts[i] != w {
			t.Errorf("failureTexts[%d] = %q; want %q (oldest first)", i, f.failureTexts[i], w)
		}
	}
	// The card itself still names only the most recent failure.
	if !strings.HasSuffix(f.text, "\n"+want[1]) {
		t.Errorf("card text = %q; want it to end with the most recent failure %q", f.text, want[1])
	}
	if strings.Contains(f.text, want[0]) {
		t.Errorf("card text = %q; should not carry the superseded failure %q", f.text, want[0])
	}
	// Drained: a later flush re-sends nothing.
	if f2 := p.snapshot(); len(f2.failureTexts) != 0 {
		t.Errorf("failureTexts on second flush = %q; want empty", f2.failureTexts)
	}
}

// TestRunProgress_IgnoresUpdatesAfterFinish: once the terminal snapshot is
// taken, trailing part events cannot flip the card back to pending.
func TestRunProgress_IgnoresUpdatesAfterFinish(t *testing.T) {
	t.Parallel()
	p := fixedClock(time.Second)
	p.toolDone("a", "read", "#a", false, "")
	p.finish(progressStatusOK)
	final := p.snapshot()
	if !final.final {
		t.Fatal("snapshot after finish should be final")
	}
	p.toolDone("b", "read", "#b", false, "")
	p.toolStarted("c", "bash")
	again := p.snapshot()
	if again.text != final.text {
		t.Errorf("post-finish update changed the card: %q → %q", final.text, again.text)
	}
}

// TestRunProgress_WorkerOrdersAndCoalesces: the flush worker delivers
// counts in non-decreasing order, collapses a burst into fewer edits than
// events, and ends on the terminal state.
func TestRunProgress_WorkerOrdersAndCoalesces(t *testing.T) {
	t.Parallel()
	p := newRunProgress()
	p.interval = 20 * time.Millisecond

	var mu sync.Mutex
	var flushes []progressFlush
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx, func(f progressFlush) {
		mu.Lock()
		flushes = append(flushes, f)
		mu.Unlock()
	})

	// Wait for the "Thinking..." flush to actually land before the burst.
	// A bare sleep would race: if the worker is not scheduled in time the
	// 30 completions and finish() all land first, the single snapshot is
	// terminal, and the first-flush assertion below fails on a loaded box.
	p.signal() // "Thinking..."
	waitFor(t, "the Thinking... flush", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(flushes) > 0
	})
	const n = 30
	for i := 0; i < n; i++ {
		p.toolDone("id", "read", "#id", false, "")
	}
	p.finish(progressStatusOK)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(flushes) == 0 {
		t.Fatal("no flushes")
	}
	if len(flushes) >= n {
		t.Errorf("flushes = %d; want fewer than the %d events (coalescing)", len(flushes), n)
	}
	if got := flushes[0].text; got != "⏳ Thinking..." {
		t.Errorf("first flush = %q; want Thinking...", got)
	}
	last := flushes[len(flushes)-1]
	if !last.final || !strings.HasPrefix(last.text, "✓ Done · 30 tool calls · ") {
		t.Errorf("last flush = %+v; want final ✓ Done · 30 tool calls", last)
	}
	prev := -1
	for _, f := range flushes {
		c := countIn(t, f.text)
		if c < prev {
			t.Errorf("count went backwards: %d after %d (%q)", c, prev, f.text)
		}
		prev = c
	}
}

// countIn extracts the tool-call count from a card header.
func countIn(t *testing.T, text string) int {
	t.Helper()
	header := strings.SplitN(text, "\n", 2)[0]
	fields := strings.Fields(header)
	for i, f := range fields {
		if f == "tool" && i > 0 {
			n := 0
			for _, r := range fields[i-1] {
				if r < '0' || r > '9' {
					return 0
				}
				n = n*10 + int(r-'0')
			}
			return n
		}
	}
	return 0
}

func TestFormatElapsed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{12 * time.Second, "12s"},
		{72 * time.Second, "1m12s"},
		{3723 * time.Second, "1h02m"},
	}
	for _, tt := range tests {
		if got := formatElapsed(tt.d); got != tt.want {
			t.Errorf("formatElapsed(%v) = %q; want %q", tt.d, got, tt.want)
		}
	}
}

// TestShouldEmitToolCards is the gate table behind compact vs full: only
// full posts per-call cards; compact posts nothing per call except a
// failure that has no progress card to fold into.
func TestShouldEmitToolCards(t *testing.T) {
	t.Parallel()
	if shouldEmitToolCallCard(bridge.ToolUpdateVerbosityCompact) {
		t.Error("compact must not post per-call pending cards")
	}
	if !shouldEmitToolCallCard(bridge.ToolUpdateVerbosityFull) {
		t.Error("full must post per-call pending cards")
	}
	tests := []struct {
		mode    string
		hasCard bool
		isError bool
		want    bool
	}{
		{bridge.ToolUpdateVerbosityFull, true, false, true},
		{bridge.ToolUpdateVerbosityFull, false, true, true},
		{bridge.ToolUpdateVerbosityCompact, true, false, false},
		{bridge.ToolUpdateVerbosityCompact, true, true, false},
		{bridge.ToolUpdateVerbosityCompact, false, false, false},
		{bridge.ToolUpdateVerbosityCompact, false, true, true},
	}
	for _, tt := range tests {
		if got := shouldEmitToolResultCard(tt.mode, tt.hasCard, tt.isError); got != tt.want {
			t.Errorf("shouldEmitToolResultCard(%q, card=%v, err=%v) = %v; want %v",
				tt.mode, tt.hasCard, tt.isError, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Dispatcher-level: the card reaches peers through MessageEditor
// ---------------------------------------------------------------------------

// editorStubAdapter is a stubAdapter that also edits messages in place.
type editorStubAdapter struct {
	stubAdapter

	emu        sync.Mutex
	posts      []string
	edits      []string
	editTokens []string
	// editErr, when set, is returned by the next EditMessage call and
	// then cleared — for exercising the fresh-post recovery path.
	editErr error
	// postToken is handed back by SendEditable; incremented per post so
	// a test can tell one peer's card from another's.
	postToken int
}

// failNextEdit makes the next EditMessage return err.
func (a *editorStubAdapter) failNextEdit(err error) {
	a.emu.Lock()
	defer a.emu.Unlock()
	a.editErr = err
}

// Tokens returns the token handed out for each post, in order.
func (a *editorStubAdapter) Tokens() []string {
	a.emu.Lock()
	defer a.emu.Unlock()
	out := make([]string, 0, a.postToken)
	for i := 1; i <= a.postToken; i++ {
		out = append(out, "tok-"+strconv.Itoa(i))
	}
	return out
}

func newEditorStubAdapter(channel, identity string) *editorStubAdapter {
	return &editorStubAdapter{stubAdapter: *newStubAdapter(channel, identity)}
}

func (a *editorStubAdapter) SendEditable(_ context.Context, _ bridge.PeerRef, text string) (bridge.EditableMessageToken, error) {
	a.emu.Lock()
	defer a.emu.Unlock()
	a.posts = append(a.posts, text)
	a.postToken++
	return bridge.EditableMessageToken("tok-" + strconv.Itoa(a.postToken)), nil
}

func (a *editorStubAdapter) EditMessage(_ context.Context, _ bridge.PeerRef, token bridge.EditableMessageToken, text string) error {
	a.emu.Lock()
	defer a.emu.Unlock()
	if err := a.editErr; err != nil {
		a.editErr = nil
		return err
	}
	a.edits = append(a.edits, text)
	a.editTokens = append(a.editTokens, string(token))
	return nil
}

// EditTokens returns the token each successful edit targeted, in order.
func (a *editorStubAdapter) EditTokens() []string {
	a.emu.Lock()
	defer a.emu.Unlock()
	return append([]string(nil), a.editTokens...)
}

func (a *editorStubAdapter) Posts() []string {
	a.emu.Lock()
	defer a.emu.Unlock()
	return append([]string(nil), a.posts...)
}

func (a *editorStubAdapter) Edits() []string {
	a.emu.Lock()
	defer a.emu.Unlock()
	return append([]string(nil), a.edits...)
}

// newProgressTestSvc wires a service with two peers bound to session S1:
// a Slack-like adapter that edits in place and a relay-like adapter that
// only has Send.
func newProgressTestSvc(t *testing.T, cfg *bridge.Config) (*Service, *editorStubAdapter, *stubAdapter) {
	t.Helper()
	svc, conn := newOrchestratorForTest(t)
	svc.cfg = cfg
	// emitToolRender fans out on the service context; Start is not
	// called here, so give it one.
	svc.ctx = context.Background()
	// The fixture's database is a shared-cache in-memory SQLite that, in
	// this build, is only visible through the connection that created
	// it: a second pooled connection sees an empty database ("no such
	// table: bridge_sessions"). Tests that only ever query from one
	// goroutine never open a second connection; these tests fan out
	// from the progress worker and emitToolRender concurrently with the
	// test goroutine, so serialise the pool on the one connection.
	conn.SetMaxOpenConns(1)
	svc.app = &app.App{Messages: &stubMessageSvc{}}
	svc.toolVerbosity.Store(cfg.ToolVerbosity())
	ed := newEditorStubAdapter("slack", "default")
	relay := newStubAdapter("external", "relay")
	svc.adapters[adapterKey("slack", "default")] = ed
	svc.adapters[adapterKey("external", "relay")] = relay
	for _, b := range []store.Binding{
		{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"},
		{ProjectID: "proj", Channel: "external", IdentityID: "relay", PeerID: "job-1", SessionID: "S1"},
	} {
		if _, err := svc.store.UpsertBinding(context.Background(), b); err != nil {
			t.Fatalf("UpsertBinding: %v", err)
		}
	}
	return svc, ed, relay
}

func partEvent(sessionID string, part message.ContentPart) pubsub.Event[message.PartEvent] {
	return pubsub.Event[message.PartEvent]{
		Type:    pubsub.CreatedEvent,
		Payload: message.PartEvent{SessionID: sessionID, Part: part},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestDispatch_ProgressCard_Compact is the change's end-to-end contract at
// compact verbosity: one message posted, edited as calls complete, no
// per-call messages, a failure folded into the card and relayed once as
// text to the peer that cannot edit, and a terminal edit at the end.
func TestDispatch_ProgressCard_Compact(t *testing.T) {
	old := progressMinInterval
	progressMinInterval = 10 * time.Millisecond
	t.Cleanup(func() { progressMinInterval = old })

	svc, ed, relay := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	d := newBareDispatch(svc, "S1")
	ctx := context.Background()

	prog := d.progressStart(ctx)
	if prog == nil {
		t.Fatal("progressStart returned nil at compact with tool updates on")
	}
	waitFor(t, "Thinking... post", func() bool { return len(ed.Posts()) == 1 })
	if got := ed.Posts()[0]; got != "⏳ Thinking..." {
		t.Errorf("first post = %q; want ⏳ Thinking...", got)
	}

	// Three calls: two succeed, one fails.
	calls := []struct {
		id, name string
		fail     bool
	}{
		{"toolu_01aaaaaa", "read", false},
		{"toolu_02bbbbbb", "bash", true},
		{"toolu_03cccccc", "grep", false},
	}
	for _, c := range calls {
		d.handlePartEvent(partEvent("S1", message.ToolCall{ID: c.id, Name: c.name, Input: "{}", Finished: true}))
		content := "ok"
		if c.fail {
			content = "permission denied\nexit 1"
		}
		d.handlePartEvent(partEvent("S1", message.ToolResult{ToolCallID: c.id, Name: c.name, Content: content, IsError: c.fail}))
	}
	d.progressFinish(prog, progressStatusOK)

	// One post, then edits only.
	if got := ed.Posts(); len(got) != 1 {
		t.Errorf("posts = %v; want exactly one (the card)", got)
	}
	edits := ed.Edits()
	if len(edits) == 0 {
		t.Fatal("card was never edited")
	}
	last := edits[len(edits)-1]
	if !strings.HasPrefix(last, "✓ Done · 3 tool calls · 1 failed · ") {
		t.Errorf("terminal edit = %q; want ✓ Done · 3 tool calls · 1 failed · <elapsed>", last)
	}
	if !strings.Contains(last, "\n✗ bash#bbbbbb · permission denied exit 1") {
		t.Errorf("terminal edit lacks the failure line: %q", last)
	}

	// No per-call messages reached the Slack-like peer.
	if sends := ed.Sends(); len(sends) != 0 {
		t.Errorf("per-call Send calls at compact = %d; want 0: %+v", len(sends), sends)
	}
	// The relay peer got the failure as text, once, and nothing else.
	rs := relay.Sends()
	if len(rs) != 1 {
		t.Fatalf("relay sends = %d; want exactly 1 (the failure line): %+v", len(rs), rs)
	}
	if rs[0].Text != "✗ bash#bbbbbb · permission denied exit 1" {
		t.Errorf("relay text = %q; want the failure line", rs[0].Text)
	}
	if d.progress.Load() != nil {
		t.Error("progress pointer should be cleared after finish")
	}
}

// TestDispatch_ProgressCard_FullPostsPerCall: at full verbosity no card is
// opened and each call is its own message, as before.
func TestDispatch_ProgressCard_FullPostsPerCall(t *testing.T) {
	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true, ToolUpdateVerbosity: "full"})
	d := newBareDispatch(svc, "S1")

	if prog := d.progressStart(context.Background()); prog != nil {
		t.Fatal("progressStart opened a card at full verbosity")
	}
	d.handlePartEvent(partEvent("S1", message.ToolCall{ID: "toolu_01", Name: "read", Input: `{"path":"x"}`, Finished: true}))
	d.handlePartEvent(partEvent("S1", message.ToolResult{ToolCallID: "toolu_01", Name: "read", Content: "12 lines"}))
	waitFor(t, "two per-call sends", func() bool { return len(ed.Sends()) == 2 })
	if got := ed.Posts(); len(got) != 0 {
		t.Errorf("SendEditable called at full: %v", got)
	}
}

// TestDispatch_ProgressCard_DisabledKeepsFailureLine: with tool updates
// off there is no card, successes are silent and a failure still posts
// as a fresh ✗ line.
func TestDispatch_ProgressCard_DisabledKeepsFailureLine(t *testing.T) {
	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: false})
	d := newBareDispatch(svc, "S1")

	if prog := d.progressStart(context.Background()); prog != nil {
		t.Fatal("progressStart opened a card with tool updates disabled")
	}
	d.handlePartEvent(partEvent("S1", message.ToolCall{ID: "toolu_01", Name: "read", Input: "{}", Finished: true}))
	d.handlePartEvent(partEvent("S1", message.ToolResult{ToolCallID: "toolu_01", Name: "read", Content: "fine"}))
	d.handlePartEvent(partEvent("S1", message.ToolResult{ToolCallID: "toolu_02", Name: "bash", Content: "nope", IsError: true}))
	waitFor(t, "failure send", func() bool { return len(ed.Sends()) == 1 })
	if got := ed.Sends()[0].Text; !strings.HasPrefix(got, "✗ bash") {
		t.Errorf("send = %q; want a ✗ bash line", got)
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(ed.Sends()); n != 1 {
		t.Errorf("sends = %d; want 1 (successes stay silent)", n)
	}
}

// TestDeliverProgress_FollowsPeerIDMutation: sendToOnePeer rewrites a
// channel-form binding's peer_id to "<channel>|<thread>" on the first
// outbound that resolves a thread — and the agent's own reply does that
// before this run's terminal flush. The card's token must follow the
// rewrite, or the terminal edit posts a SECOND message and strands the
// "⏳ Thinking..." one. Reachable on every Mattermost binding and on any
// non-DM Slack channel.
func TestDeliverProgress_FollowsPeerIDMutation(t *testing.T) {
	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	d := newBareDispatch(svc, "S1")
	ctx := context.Background()

	// Re-point the editor binding at a bare channel (the pre-thread form
	// a router-initiated Mattermost/Slack channel binding starts in).
	if err := svc.store.UpdateBindingPeerID(ctx, "proj", "slack", "default", "D1", "C1"); err != nil {
		t.Fatalf("UpdateBindingPeerID (setup): %v", err)
	}

	p := newRunProgress()
	d.deliverProgress(ctx, p, progressFlush{text: "⏳ Thinking..."})
	if got := ed.Posts(); len(got) != 1 {
		t.Fatalf("posts after first flush = %v; want exactly one", got)
	}

	// The agent's reply resolves the thread; the binding is rewritten.
	if err := svc.store.UpdateBindingPeerID(ctx, "proj", "slack", "default", "C1", "C1|1700000000.5"); err != nil {
		t.Fatalf("UpdateBindingPeerID (mutation): %v", err)
	}

	want := "✓ Done · 1 tool call · 2s"
	d.deliverProgress(ctx, p, progressFlush{text: want, final: true})

	if got := ed.Posts(); len(got) != 1 {
		t.Errorf("posts = %v; want still exactly one — the card was re-posted after the peer_id gained its thread suffix", got)
	}
	edits := ed.Edits()
	if len(edits) != 1 || edits[0] != want {
		t.Errorf("edits = %v; want exactly [%q] (terminal edit applied to the original card)", edits, want)
	}
}

// TestDeliverProgress_SkipsIdenticalEdit: a flush whose text matches what
// the card already shows must not issue an edit. formatElapsed drops to
// minute granularity past 1h, so a wake that changes no counter renders
// byte-identical text; Telegram answers that with 400 "message is not
// modified", and deliverProgress treats any edit error as a stale token
// and posts a duplicate card.
func TestDeliverProgress_SkipsIdenticalEdit(t *testing.T) {
	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	d := newBareDispatch(svc, "S1")
	ctx := context.Background()

	same := "⏳ 42 tool calls done · running bash · 1h05m"
	p := newRunProgress()
	d.deliverProgress(ctx, p, progressFlush{text: same})
	d.deliverProgress(ctx, p, progressFlush{text: same})

	if got := ed.Posts(); len(got) != 1 {
		t.Errorf("posts = %v; want exactly one", got)
	}
	if got := ed.Edits(); len(got) != 0 {
		t.Errorf("edits = %v; want none — the text did not change", got)
	}

	// A real change still edits.
	changed := "⏳ 43 tool calls done · 1h05m"
	d.deliverProgress(ctx, p, progressFlush{text: changed})
	if got := ed.Edits(); len(got) != 1 || got[0] != changed {
		t.Errorf("edits after a real change = %v; want [%q]", got, changed)
	}
}

// TestDeliverProgress_EditFailureRepostsAndContinues covers the spec
// scenario "Edit failure recovers with a fresh post" (chat-bridge delta),
// which had no test: when the card's message is gone and the edit fails,
// the card is posted fresh and every later flush edits the NEW message
// rather than retrying the dead token.
func TestDeliverProgress_EditFailureRepostsAndContinues(t *testing.T) {
	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	d := newBareDispatch(svc, "S1")
	ctx := context.Background()
	p := newRunProgress()

	d.deliverProgress(ctx, p, progressFlush{text: "⏳ Thinking..."})
	if got := ed.Posts(); len(got) != 1 {
		t.Fatalf("posts after first flush = %v; want one", got)
	}

	// The message was deleted out from under us.
	ed.failNextEdit(errors.New("message_not_found"))
	d.deliverProgress(ctx, p, progressFlush{text: "⏳ 1 tool call done · 1s"})

	if got := ed.Posts(); len(got) != 2 || got[1] != "⏳ 1 tool call done · 1s" {
		t.Fatalf("posts = %v; want the card re-posted with the current text", got)
	}
	if got := ed.Edits(); len(got) != 0 {
		t.Errorf("edits = %v; want none — the only edit attempt failed", got)
	}

	// The chain continues from the NEW message, not the dead one.
	d.deliverProgress(ctx, p, progressFlush{text: "✓ Done · 2 tool calls · 3s", final: true})
	if got := ed.Posts(); len(got) != 2 {
		t.Errorf("posts = %v; want no third post — the new token should be reused", got)
	}
	edits, tokens := ed.Edits(), ed.EditTokens()
	if len(edits) != 1 || edits[0] != "✓ Done · 2 tool calls · 3s" {
		t.Fatalf("edits = %v; want the terminal edit", edits)
	}
	if len(tokens) != 1 || tokens[0] != "tok-2" {
		t.Errorf("edit targeted token %v; want tok-2 (the re-posted message), not the stale tok-1", tokens)
	}
}

// errorAgent is an agent.Service whose Run yields a single terminal
// AgentEventTypeError, so handleInbound takes the failed-run path.
type errorAgent struct{ agentpkg.Service }

func (a *errorAgent) Run(
	_ context.Context, _, _ string, _ int, _ ...message.Attachment,
) (<-chan agentpkg.AgentEvent, error) {
	ch := make(chan agentpkg.AgentEvent, 1)
	ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: errors.New("boom")}
	close(ch)
	return ch, nil
}

// TestHandleInbound_AgentErrorClosesCardAsFailed wires the dispatcher's
// terminal-event handling to the card: an AgentEventTypeError must close
// the card as "✗ Run failed", not "✓ Done". Covers the spec scenario
// "Agent error ends the card as failed" end-to-end — the mapping at
// handleInbound's `ev.Type == AgentEventTypeError` had no test.
func TestHandleInbound_AgentErrorClosesCardAsFailed(t *testing.T) {
	old := progressMinInterval
	progressMinInterval = 10 * time.Millisecond
	t.Cleanup(func() { progressMinInterval = old })

	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	svc.app.PrimaryAgents = map[config.AgentName]agentpkg.Service{config.AgentCoder: &errorAgent{}}
	svc.app.PrimaryAgentKeys = []config.AgentName{config.AgentCoder}

	d := newBareDispatch(svc, "S1")
	d.handleInbound(context.Background(), testInbound("do the thing"))

	edits := ed.Edits()
	if len(edits) == 0 {
		t.Fatal("card was never edited to a terminal state")
	}
	last := edits[len(edits)-1]
	if !strings.HasPrefix(last, "✗ Run failed · ") {
		t.Errorf("terminal edit = %q; want it to start with ✗ Run failed", last)
	}
	if strings.HasPrefix(last, "✓ Done") {
		t.Errorf("terminal edit = %q; a failed run must not close as Done", last)
	}
}
