package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

type bridgeFlowQueries struct{ *db.Queries }

func (q bridgeFlowQueries) WithTx(tx *sql.Tx) db.QuerierWithTx {
	return bridgeFlowQueries{q.Queries.WithTx(tx)}
}

type testSessionFlows struct {
	steeringQuestion string
	steeringHinted   bool
	run              flowrun.Run
	lease            flowrun.Lease
	queued           []app.FlowSteeringMessage
	controls         []string
	receipts         []string
	broker           *pubsub.Broker[app.FlowRunEvent]
}

func (c *testSessionFlows) CurrentFlow(context.Context, string) (flowrun.Run, error) {
	return c.run, nil
}
func (c *testSessionFlows) QueueFlowMessages(ctx context.Context, _ string, m []app.FlowSteeringMessage) error {
	c.steeringQuestion, c.steeringHinted = question.SteeringQuestion(ctx)
	c.queued = append(c.queued, m...)
	return nil
}
func (c *testSessionFlows) ControlFlow(_ context.Context, _, _, action string) error {
	c.controls = append(c.controls, action)
	return nil
}
func (c *testSessionFlows) SessionFlowConversation(_ context.Context, sid string) (string, bool, error) {
	return "S1", sid == "step", nil
}
func (c *testSessionFlows) SubscribeFlowEvents(ctx context.Context) <-chan pubsub.Event[app.FlowRunEvent] {
	return c.broker.Subscribe(ctx)
}
func (c *testSessionFlows) AckFlowEvent(_ context.Context, _, id, _ string) error {
	c.receipts = append(c.receipts, id)
	return nil
}
func (c *testSessionFlows) FlowLease(string) (flowrun.Lease, bool) { return c.lease, c.lease.Token > 0 }

func TestSessionFlowSteeringPreservesIDsAndOrigin(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	c := &testSessionFlows{run: flowrun.Run{ID: "run", ConversationID: "S1", Status: "running"}}
	svc.sessionFlows = c
	in := bridge.Inbound{Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}, Messages: []bridge.InboundMessage{{ID: "one", Text: "keep tests"}, {ID: "two", Text: "also docs"}}}
	if !svc.routeSessionFlowInput(context.Background(), "S1", in) {
		t.Fatal("flow input fell through to root turn")
	}
	if len(c.queued) != 2 || c.queued[0].ID != "one" {
		t.Fatalf("queued=%+v", c.queued)
	}
	var peer bridge.PeerRef
	if json.Unmarshal(c.queued[0].Origin, &peer) != nil || peer != in.Peer {
		t.Fatal("lost exact sender identity")
	}
	if len(c.receipts) != 0 {
		t.Fatal("input was acknowledged before boundary delivery")
	}
}
func TestSessionFlowOutputOutlivesSubmittingTurn(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	adapter := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = adapter
	_, err := svc.store.UpsertBinding(context.Background(), store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	c := &testSessionFlows{run: flowrun.Run{ID: "run", ConversationID: "S1", Status: "running"}}
	svc.sessionFlows = c
	svc.deliverSessionFlowEvent(context.Background(), app.FlowRunEvent{ID: "event", RunID: "run", ConversationID: "S1", Type: "session.flow.completed", Output: "result"})
	sends := adapter.Sends()
	if len(sends) != 1 || len(c.receipts) != 1 {
		t.Fatalf("sends=%v receipts=%v", sends, c.receipts)
	}
	if sends[0].Text != "Workflow completed." {
		t.Fatalf("completion output=%q", sends[0].Text)
	}
}
func TestSessionFlowCompletionDoesNotRepeatTerminalStepOutput(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	adapter := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = adapter
	_, err := svc.store.UpsertBinding(context.Background(), store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	c := &testSessionFlows{run: flowrun.Run{ID: "run", ConversationID: "S1", Status: "running"}}
	svc.sessionFlows = c
	svc.deliverSessionFlowEvent(context.Background(), app.FlowRunEvent{ID: "step-event", RunID: "run", ConversationID: "S1", Type: "session.flow.step", StepID: "finish", Status: "completed", Output: "result"})
	svc.deliverSessionFlowEvent(context.Background(), app.FlowRunEvent{ID: "completion-event", RunID: "run", ConversationID: "S1", Type: "session.flow.completed", Output: "Step finish:\nresult"})
	sends := adapter.Sends()
	if len(sends) != 2 || len(c.receipts) != 2 {
		t.Fatalf("sends=%v receipts=%v", sends, c.receipts)
	}
	if sends[0].Text != "finish · completed\n\nresult" {
		t.Fatalf("step output=%q", sends[0].Text)
	}
	if sends[1].Text != "Workflow completed." {
		t.Fatalf("completion output=%q", sends[1].Text)
	}
}
func TestSessionFlowFailureIncludesOutput(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	adapter := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = adapter
	_, err := svc.store.UpsertBinding(context.Background(), store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	c := &testSessionFlows{run: flowrun.Run{ID: "run", ConversationID: "S1", Status: "running"}}
	svc.sessionFlows = c
	svc.deliverSessionFlowEvent(context.Background(), app.FlowRunEvent{ID: "event", RunID: "run", ConversationID: "S1", Type: "session.flow.failed", Output: "failure"})
	sends := adapter.Sends()
	if len(sends) != 1 || sends[0].Text != "Workflow failed.\n\nfailure" {
		t.Fatalf("sends=%v", sends)
	}
}
func TestSessionFlowQuestionSurvivesWaiterReplacement(t *testing.T) {
	for _, sid := range []string{"step", "S1"} {
		t.Run(sid, func(t *testing.T) { testSessionFlowQuestionRecovery(t, sid) })
	}
}
func testSessionFlowQuestionRecovery(t *testing.T, sid string) {
	svc, conn := newOrchestratorForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := bridgeFlowQueries{db.New(conn)}
	runs := flowrun.New(conn, q, "proj", "sqlite")
	run, _, err := runs.Create(ctx, flowrun.Run{ConversationID: "S1", InvocationID: "invoke", Kind: "generated", Snapshot: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := runs.Claim(ctx, run.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c := &testSessionFlows{run: run, lease: lease}
	svc.sessionFlows = c
	svc.app = &app.App{FlowRuns: runs, Questions: question.NewService()}
	if _, err = q.CreateSession(ctx, db.CreateSessionParams{ID: "step", ProjectID: sql.NullString{String: "proj", Valid: true}, RootSessionID: sql.NullString{String: "S1", Valid: true}, Title: "step"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := []question.Prompt{{Question: "Continue?", Options: []question.Option{{Label: "Yes"}}}}
	sub := svc.app.Questions.Subscribe(ctx)
	firstCtx, stopFirst := context.WithCancel(question.WithToolCallID(question.WithFlowRunID(ctx, run.ID), "call-1"))
	go svc.app.Questions.Ask(firstCtx, sid, prompt)
	first := <-sub
	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	_, logical, handled, err := router.flowQuestionBindings(ctx, first.Payload)
	if err != nil || handled || logical == "" {
		t.Fatalf("persist question: %s %v %v", logical, handled, err)
	}
	secondCtx, stopSecond := context.WithCancel(question.WithToolCallID(question.WithFlowRunID(ctx, run.ID), "call-2"))
	go svc.app.Questions.Ask(secondCtx, sid, prompt)
	second := <-sub
	_, secondLogical, secondDeferred, err := router.flowQuestionBindings(ctx, second.Payload)
	if err != nil || !secondDeferred || secondLogical == logical {
		t.Fatalf("parallel question was not independently queued: %s %v %v", secondLogical, secondDeferred, err)
	}
	stopSecond()
	stopFirst()
	// Simulate replacement: no in-process waiter remains when the old widget answers.
	svc.app.Questions = question.NewService()
	yes := [][]string{{"Yes"}}
	if handled, _, refusal := svc.durableFlowReply(ctx, logical, questionReplyInput{SessionID: "foreign-step", Answers: yes}); !handled || refusal != "session_mismatch" {
		t.Fatalf("foreign answer not rejected: %v %s", handled, refusal)
	}
	handled, replay, refusal := svc.durableFlowReply(ctx, logical, questionReplyInput{SessionID: sid, Answers: yes})
	if !handled || replay || refusal != "" {
		t.Fatalf("late reply: %v %v %s", handled, replay, refusal)
	}
	sub = svc.app.Questions.Subscribe(ctx)
	answers := make(chan [][]string, 1)
	go func() {
		a, _ := svc.app.Questions.Ask(question.WithToolCallID(question.WithFlowRunID(ctx, run.ID), "call-1"), sid, prompt)
		answers <- a
	}()
	next := <-sub
	_, nextID, handled, err := router.flowQuestionBindings(ctx, next.Payload)
	if err != nil || !handled || nextID != logical {
		t.Fatalf("rebound question: %s %v %v", nextID, handled, err)
	}
	select {
	case got := <-answers:
		if len(got) != 1 || got[0][0] != "Yes" {
			t.Fatalf("answer=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("saved answer not delivered")
	}
	svc.questionRouter = router
	router.mu.Lock()
	router.pending[sid] = &pendingQuestion{requestID: "newer-question"}
	router.mu.Unlock()
	_, replay, refusal = svc.durableFlowReply(ctx, logical, questionReplyInput{SessionID: sid, Answers: yes})
	if !replay || refusal != "" {
		t.Fatalf("retry=%v %s", replay, refusal)
	}
	router.mu.Lock()
	stillPending := router.pending[sid]
	router.mu.Unlock()
	if stillPending == nil || stillPending.requestID != "newer-question" {
		t.Fatal("late old answer removed newer pending question")
	}
}

func TestSessionFlowProseSteersAndNumericChoiceAnswers(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &testSessionFlows{run: flowrun.Run{ID: "run", ConversationID: "S1", Status: "running"}}
	svc.sessionFlows = c
	svc.app = &app.App{Questions: question.NewService()}
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	if _, err := svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: peer.Channel, IdentityID: peer.Identity, PeerID: peer.PeerID, SessionID: "S1"}); err != nil {
		t.Fatal(err)
	}
	events := svc.app.Questions.Subscribe(ctx)
	answers := make(chan [][]string, 1)
	prompts := []question.Prompt{{Question: "Continue?", Options: []question.Option{{Label: "Yes"}}}}
	go func() { got, _ := svc.app.Questions.Ask(ctx, "step", prompts); answers <- got }()
	req := (<-events).Payload
	svc.questionRouter = &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{"step": {requestID: req.ID, prompts: prompts}}, flowVisible: map[string]string{"S1": req.ID}}
	svc.dispatchInbound(ctx, bridge.Inbound{Peer: peer, Text: "also add tests", Source: bridge.InboundSourceMessage})
	if len(c.queued) != 1 || len(svc.app.Questions.List()) != 1 {
		t.Fatal("ordinary prose answered the pending question instead of steering")
	}
	svc.dispatchInbound(ctx, bridge.Inbound{Peer: peer, Text: "1", Source: bridge.InboundSourceMessage})
	select {
	case got := <-answers:
		if len(got) != 1 || got[0][0] != "Yes" {
			t.Fatalf("numeric answer=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("numeric choice did not answer")
	}
}

type interleavedQuestionService struct {
	question.Service
	list func() []question.Request
}

func (s interleavedQuestionService) List() []question.Request { return s.list() }
func TestSessionFlowQuestionAdvanceDoesNotLoseConcurrentAsk(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}, flowVisible: map[string]string{"S1": "old"}, flowQueued: map[string][]question.Request{}}
	next := question.Request{ID: "new", SessionID: "S1", Questions: []question.Prompt{{Question: "New question?"}}}
	first := true
	svc.app = &app.App{Questions: interleavedQuestionService{list: func() []question.Request {
		if first {
			first = false
			router.deferFlowQuestion("S1", next)
			return nil
		}
		return []question.Request{next}
	}}}
	router.advanceFlowQuestions(context.Background())
	if _, retained := router.flowVisible["S1"]; !retained || len(router.flowQueued["S1"]) != 1 {
		t.Fatal("concurrent Ask was lost behind a stale service snapshot")
	}
	router.advanceFlowQuestions(context.Background())
	if router.flowVisible["S1"] != "new" || len(router.flowQueued["S1"]) != 0 {
		t.Fatal("next sweep did not advance the concurrent Ask")
	}
}

func TestSessionFlowResolutionAllowsIdlePoolButKeepsDaemonJobFence(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()
	peer := bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "aid:flow:conversation"}
	if _, err := svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: peer.Channel, IdentityID: peer.Identity, PeerID: peer.PeerID, SessionID: "S1"}); err != nil {
		t.Fatal(err)
	}
	svc.poolMode = true
	if id, err := svc.ResolveSessionFlowConversation(ctx, peer, "new-job"); err != nil || id != "S1" {
		t.Fatalf("idle pool existing root=%s %v", id, err)
	}
	svc.poolMode = false
	if _, err := svc.ResolveSessionFlowConversation(ctx, peer, "wrong-job"); err == nil {
		t.Fatal("daemon admitted an unrelated job identity")
	}
}

func TestSessionFlowSteeringTargetsOnlyVisibleConversationQuestion(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	controller := &testSessionFlows{run: flowrun.Run{ID: "run", ConversationID: "S1", Status: "running"}}
	svc.sessionFlows = controller
	svc.questionRouter = &QuestionRouter{svc: svc, flowVisible: map[string]string{"S1": "visible-question", "other": "foreign-question"}}
	if !svc.routeSessionFlowInput(context.Background(), "S1", bridge.Inbound{Text: "also add tests"}) {
		t.Fatal("steering not accepted")
	}
	if !controller.steeringHinted || controller.steeringQuestion != "visible-question" {
		t.Fatalf("hint=%q present=%v", controller.steeringQuestion, controller.steeringHinted)
	}
	svc.questionRouter.flowVisible = map[string]string{}
	svc.routeSessionFlowInput(context.Background(), "S1", bridge.Inbound{Text: "later change"})
	if !controller.steeringHinted || controller.steeringQuestion != "" {
		t.Fatal("absent visible question silently retargeted hidden request")
	}
}
