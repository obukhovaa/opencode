package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
)

// deliveryAckAdapter is a stubAdapter that also implements bridge.DeliveryAcker
// and records every acknowledgement it is asked to send.
type deliveryAckAdapter struct {
	*stubAdapter
	ackMu sync.Mutex
	acks  [][]string
}

func (a *deliveryAckAdapter) SendDelivered(_ context.Context, _ bridge.PeerRef, ids []string) error {
	a.ackMu.Lock()
	defer a.ackMu.Unlock()
	a.acks = append(a.acks, append([]string(nil), ids...))
	return nil
}

func (a *deliveryAckAdapter) Acks() [][]string {
	a.ackMu.Lock()
	defer a.ackMu.Unlock()
	return append([][]string(nil), a.acks...)
}

var (
	extPeer   = bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "run-1"}
	slackPeer = bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C1|1700000000.000100"}
)

// newInboxTestService wires a service whose session S1 is an interactive
// step bound to an external peer (with delivery acks) and, optionally, a
// Slack peer.
func newInboxTestService(t *testing.T, withSlack bool) (*Service, *QuestionRouter, *deliveryAckAdapter, *stubAdapter) {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	svc.app = &app.App{
		Permissions: permission.NewPermissionService(),
		Questions:   question.NewService(),
	}
	svc.app.Permissions.MarkInteractiveSession("S1")
	r := newBufferRouter(svc)
	svc.questionRouter = r

	ext := &deliveryAckAdapter{stubAdapter: newStubAdapter("external", "c3")}
	svc.adapters[adapterKey("external", "c3")] = ext
	bind := func(p bridge.PeerRef) {
		if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
			ProjectID: "proj", Channel: p.Channel, IdentityID: p.Identity,
			PeerID: p.PeerID, SessionID: "S1",
		}); err != nil {
			t.Fatalf("UpsertBinding: %v", err)
		}
	}
	bind(extPeer)
	var slack *stubAdapter
	if withSlack {
		slack = newStubAdapter("slack", "default")
		svc.adapters[adapterKey("slack", "default")] = slack
		bind(slackPeer)
	}
	return svc, r, ext, slack
}

func batch(ids ...string) bridge.Inbound {
	in := bridge.Inbound{Peer: extPeer}
	for _, id := range ids {
		in.Messages = append(in.Messages, bridge.InboundMessage{ID: id, Text: "text " + id})
	}
	return in
}

func itemIDs(items []inbox.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// askAsync starts an Ask on S1 and returns its result channel plus the
// request the question service published.
func askAsync(t *testing.T, svc *Service) (<-chan error, <-chan [][]string, question.Request) {
	t.Helper()
	ctx := context.Background()
	sub := svc.app.Questions.Subscribe(ctx)
	errCh := make(chan error, 1)
	ansCh := make(chan [][]string, 1)
	go func() {
		ans, err := svc.app.Questions.Ask(ctx, "S1", []question.Prompt{{
			Question: "Approve?",
			Options:  []question.Option{{Label: "Approve"}, {Label: "Reject"}},
		}})
		ansCh <- ans
		errCh <- err
	}()
	select {
	case ev := <-sub:
		return errCh, ansCh, ev.Payload
	case <-time.After(2 * time.Second):
		t.Fatal("no question CreatedEvent observed")
	}
	return nil, nil, question.Request{}
}

func wantUnanswered(t *testing.T, errCh <-chan error, ansCh <-chan [][]string) {
	t.Helper()
	select {
	case err := <-errCh:
		var ue *question.UnansweredError
		if !errors.As(err, &ue) || ue.Note != unansweredQuestionNote {
			t.Fatalf("Ask err = %v, want the unanswered note", err)
		}
		if ans := <-ansCh; ans != nil {
			t.Fatalf("queued input recorded as an answer: %v", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask never returned")
	}
}

// A batch that arrives while a question is pending closes it as unanswered
// and waits in the inbox, in order, for the agent to drain.
func TestEnqueueBatch_ClosesPendingQuestionAsUnanswered(t *testing.T) {
	svc, r, _, _ := newInboxTestService(t, false)
	errCh, ansCh, req := askAsync(t, svc)
	r.handleNewRequest(context.Background(), req)

	svc.dispatchInbound(context.Background(), batch("a", "b"))

	wantUnanswered(t, errCh, ansCh)
	if got := itemIDs(r.box().DrainAll("S1")); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("inbox = %v, want [a b]", got)
	}
	r.mu.Lock()
	_, stillPending := r.pending["S1"]
	r.mu.Unlock()
	if stillPending {
		t.Fatal("question still pending after the unanswered close")
	}
}

// A question asked while queued input waits is closed as unanswered and
// never shown to the peers.
func TestHandleNewRequest_QueuedInputClosesQuestion(t *testing.T) {
	svc, r, ext, slack := newInboxTestService(t, true)
	svc.dispatchInbound(context.Background(), batch("a"))

	errCh, ansCh, req := askAsync(t, svc)
	r.handleNewRequest(context.Background(), req)

	wantUnanswered(t, errCh, ansCh)
	if n := len(ext.Sends()) + len(slack.Sends()); n != 0 {
		t.Fatalf("question reached the peers (%d sends)", n)
	}
	if got := itemIDs(r.box().DrainAll("S1")); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("inbox = %v, want [a]", got)
	}
}

// Plain text still answers a pending question.
func TestPlainTextStillAnswersPendingQuestion(t *testing.T) {
	svc, r, _, _ := newInboxTestService(t, true)
	errCh, ansCh, req := askAsync(t, svc)
	r.handleNewRequest(context.Background(), req)

	svc.dispatchInbound(context.Background(), bridge.Inbound{Peer: slackPeer, Text: "Approve"})

	if err := <-errCh; err != nil {
		t.Fatalf("Ask err = %v", err)
	}
	if ans := <-ansCh; len(ans) != 1 || len(ans[0]) != 1 || ans[0][0] != "Approve" {
		t.Fatalf("answer = %v, want [[Approve]]", ans)
	}
}

// With only plain messages buffered, the head answers the next question and
// the rest stay queued for the agent loop to drain after the answer.
func TestHandleNewRequest_PlainHeadAnswersRestStaysQueued(t *testing.T) {
	svc, r, _, _ := newInboxTestService(t, true)
	ctx := context.Background()
	svc.dispatchInbound(ctx, bridge.Inbound{Peer: slackPeer, Text: "Approve"})
	svc.dispatchInbound(ctx, bridge.Inbound{Peer: slackPeer, Text: "and keep the trial"})

	errCh, ansCh, req := askAsync(t, svc)
	r.handleNewRequest(ctx, req)
	if err := <-errCh; err != nil {
		t.Fatalf("Ask err = %v", err)
	}
	if ans := <-ansCh; ans[0][0] != "Approve" {
		t.Fatalf("answer = %v", ans)
	}
	left := r.box().DrainAll("S1")
	if len(left) != 1 || itemInbound(left[0]).Text != "and keep the trial" {
		t.Fatalf("remainder = %+v, want the second message", left)
	}
}

// A batch is pushed as one unit: no single pushed concurrently lands
// between its messages.
func TestEnqueueBatch_NeverInterleavedWithSingle(t *testing.T) {
	svc, r, _, _ := newInboxTestService(t, true)
	ctx := context.Background()
	const rounds = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			r.EnqueueBatch(ctx, "S1", batch(fmt.Sprintf("x%d", i), fmt.Sprintf("y%d", i)))
			r.box().DrainAll("S1")
		}
	}()
	var mu sync.Mutex
	var drained []inbox.Item
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			r.BufferInbound(ctx, "S1", bridge.Inbound{Peer: slackPeer, Text: "single"})
			mu.Lock()
			drained = append(drained, r.box().DrainAll("S1")...)
			mu.Unlock()
		}
	}()
	wg.Wait()
	_ = svc
	for i, it := range drained {
		if strings.HasPrefix(it.ID, "x") {
			if i+1 >= len(drained) || drained[i+1].ID != "y"+it.ID[1:] {
				t.Fatalf("batch %s split at %d: %v", it.ID, i, itemIDs(drained))
			}
		}
	}
}

// Delivered ids are acknowledged in order; a re-send in a later step of
// the same run is skipped and acknowledged again, and a batch of repeats
// leaves a pending question alone.
func TestDeliveredIDs_SkipAndReacknowledgeAcrossSteps(t *testing.T) {
	svc, r, ext, _ := newInboxTestService(t, false)
	svc.remoteJobID.Store("job-1")
	ctx := context.Background()

	// Step 1: a and b are queued and drained by the agent loop.
	svc.dispatchInbound(ctx, batch("a", "b"))
	box := r.box()
	box.Delivered("S1", box.DrainAll("S1"))
	waitFor(t, "ack of a,b", func() bool { return len(ext.Acks()) == 1 })
	if got := ext.Acks()[0]; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("ack = %v, want [a b]", got)
	}

	// Step boundary: the step ends and the next binds the same peer.
	if err := svc.Unbind(ctx, "S1"); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if _, err := svc.store.UpsertBinding(ctx, store.Binding{
		ProjectID: "proj", Channel: extPeer.Channel, IdentityID: extPeer.Identity,
		PeerID: extPeer.PeerID, SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	// Step 2: a question is pending and the lost-ack sender re-sends a.
	errCh, _, req := askAsync(t, svc)
	r.handleNewRequest(ctx, req)
	svc.dispatchInbound(ctx, batch("a"))
	waitFor(t, "re-ack of a", func() bool { return len(ext.Acks()) == 2 })
	if got := ext.Acks()[1]; !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("re-ack = %v, want [a]", got)
	}
	if n := box.Len("S1"); n != 0 {
		t.Fatalf("repeat was queued again (%d items)", n)
	}
	r.mu.Lock()
	_, pending := r.pending["S1"]
	r.mu.Unlock()
	if !pending {
		t.Fatal("a batch of repeats closed the pending question")
	}
	select {
	case err := <-errCh:
		t.Fatalf("Ask returned early: %v", err)
	default:
	}

	// A new job forgets the run's ids.
	svc.SetRemoteJobID("job-2")
	if svc.delivered.has(extPeer, "a") {
		t.Fatal("delivered ids survived a job change")
	}
}

// An id still waiting in the inbox is not queued twice, and nothing is
// acknowledged until the agent loop stores it.
func TestEnqueueBatch_QueuedIDNotDuplicated(t *testing.T) {
	svc, r, ext, _ := newInboxTestService(t, false)
	ctx := context.Background()
	svc.dispatchInbound(ctx, batch("a"))
	svc.dispatchInbound(ctx, batch("a", "b"))
	if got := itemIDs(r.box().DrainAll("S1")); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("inbox = %v, want [a b]", got)
	}
	time.Sleep(20 * time.Millisecond)
	if acks := ext.Acks(); len(acks) != 0 {
		t.Fatalf("acknowledged before delivery: %v", acks)
	}
}

// Drained plain chat messages produce no acknowledgement.
func TestOnInboxDelivered_PlainItemsNotAcked(t *testing.T) {
	svc, r, ext, _ := newInboxTestService(t, true)
	svc.dispatchInbound(context.Background(), bridge.Inbound{Peer: slackPeer, Text: "hi"})
	box := r.box()
	box.Delivered("S1", box.DrainAll("S1"))
	time.Sleep(20 * time.Millisecond)
	if acks := ext.Acks(); len(acks) != 0 {
		t.Fatalf("plain message acknowledged: %v", acks)
	}
}

// A drained item carries the attribution envelope when the session has
// several peers, while its raw inbound keeps the bare text.
func TestBufferInbound_AttributesForAgent(t *testing.T) {
	svc, r, _, _ := newInboxTestService(t, true)
	svc.dispatchInbound(context.Background(), bridge.Inbound{Peer: slackPeer, Text: "hi"})
	items := r.box().DrainAll("S1")
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	if items[0].Text == "hi" || !strings.Contains(items[0].Text, "hi") {
		t.Fatalf("agent text = %q, want an attributed form of %q", items[0].Text, "hi")
	}
	if itemInbound(items[0]).Text != "hi" {
		t.Fatalf("raw text = %q", itemInbound(items[0]).Text)
	}
}
