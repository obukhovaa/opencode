package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/question"
)

type nativeChoiceCapture struct {
	*stubAdapter
	choices []bridge.QuestionChoice
}

func (c *nativeChoiceCapture) SendInteractiveQuestion(_ context.Context, _ bridge.PeerRef, _ string, choices []bridge.QuestionChoice) (string, error) {
	c.choices = choices
	return "", nil
}
func TestFlowWidgetsUseOpaqueValuesOnlyForManagedQuestions(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	capture := &nativeChoiceCapture{stubAdapter: newStubAdapter("telegram", "default")}
	svc.adapters[adapterKey("telegram", "default")] = capture
	router := &QuestionRouter{svc: svc}
	peer := bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "1"}
	prompt := question.Prompt{Question: "Continue?", Options: []question.Option{{Label: "Yes"}}}
	if _, err := router.tryInteractiveSend(context.Background(), peer, prompt); err != nil {
		t.Fatal(err)
	}
	if capture.choices[0].Value != "Yes" {
		t.Fatal("legacy question value changed")
	}
	id := strings.Repeat("a", 64)
	ctx := bridge.ContextWithExternalQuestion(context.Background(), bridge.ExternalQuestionContext{ConversationID: "S1", RequestID: id})
	if _, err := router.tryInteractiveSend(ctx, peer, prompt); err != nil {
		t.Fatal(err)
	}
	choice := capture.choices[0]
	got, index, err := bridge.DecodeFlowQuestionValue(choice.Value)
	if choice.Label != "Yes" || err != nil || got != id || index != 0 || len(choice.Value) > 60 {
		t.Fatalf("choice=%+v decoded=%s %d %v", choice, got, index, err)
	}
}
func TestOldNativeFlowButtonCannotAnswerNewQuestionWithSameLabel(t *testing.T) {
	svc, conn := newOrchestratorForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := bridgeFlowQueries{db.New(conn)}
	runs := flowrun.New(conn, queries, "proj", "sqlite")
	run, _, err := runs.Create(ctx, flowrun.Run{ConversationID: "S1", InvocationID: "native", Kind: "generated", Snapshot: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := runs.Claim(ctx, run.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc.sessionFlows = &testSessionFlows{run: run, lease: lease}
	svc.app = &app.App{Questions: question.NewService(), FlowRuns: runs}
	if _, err = queries.CreateSession(ctx, db.CreateSessionParams{ID: "step", ProjectID: sql.NullString{String: "proj", Valid: true}, RootSessionID: sql.NullString{String: "S1", Valid: true}, Title: "step"}); err != nil {
		t.Fatal(err)
	}
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	if _, err = svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: peer.Channel, IdentityID: peer.Identity, PeerID: peer.PeerID, SessionID: "S1"}); err != nil {
		t.Fatal(err)
	}
	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	svc.questionRouter = router
	sub := svc.app.Questions.Subscribe(ctx)
	prompts := []question.Prompt{{Question: "Continue?", Options: []question.Option{{Label: "Yes"}}}}
	ask := func(call string) (question.Request, string, <-chan [][]string) {
		answers := make(chan [][]string, 1)
		go func() {
			a, _ := svc.app.Questions.Ask(question.WithFlowRunID(question.WithToolCallID(ctx, call), run.ID), "step", prompts)
			answers <- a
		}()
		request := (<-sub).Payload
		router.handleNewRequest(ctx, request)
		saved, err := runs.FindQuestionByRequest(ctx, request.ID)
		if err != nil {
			t.Fatal(err)
		}
		token, err := bridge.FlowQuestionValue(saved.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		return request, token, answers
	}
	_, oldToken, first := ask("one")
	if !svc.routeRequestQuestionReply(ctx, "S1", bridge.Inbound{Peer: peer, Source: bridge.InboundSourceButton, Text: oldToken}) {
		t.Fatal("first button not routed")
	}
	<-first
	router.advanceFlowQuestions(ctx)
	secondRequest, newToken, second := ask("two")
	svc.routeRequestQuestionReply(ctx, "S1", bridge.Inbound{Peer: peer, Source: bridge.InboundSourceButton, Text: oldToken})
	select {
	case <-second:
		t.Fatal("old same-label button answered newer question")
	default:
	}
	found := false
	for _, pending := range svc.app.Questions.List() {
		if pending.ID == secondRequest.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("new question was removed by old callback")
	}
	svc.tryFlowQuestionReply(ctx, "S1", bridge.Inbound{Peer: peer, Source: bridge.InboundSourceButton, Text: "Yes"})
	select {
	case <-second:
		t.Fatal("unidentified legacy widget answered newer flow question")
	default:
	}
	svc.routeRequestQuestionReply(ctx, "S1", bridge.Inbound{Peer: peer, Source: bridge.InboundSourceButton, Text: newToken})
	select {
	case answers := <-second:
		if len(answers) != 1 || answers[0][0] != "Yes" {
			t.Fatalf("answers=%v", answers)
		}
	case <-time.After(time.Second):
		t.Fatal("new button did not answer exact question")
	}
}

func TestOrdinarySubmittingQuestionKeepsLegacyRootRouting(t *testing.T) {
	for _, source := range []string{bridge.InboundSourceMessage, bridge.InboundSourceButton} {
		t.Run(source, func(t *testing.T) { testOrdinarySubmittingQuestionRouting(t, source) })
	}
}
func testOrdinarySubmittingQuestionRouting(t *testing.T, source string) {
	svc, conn := newOrchestratorForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := bridgeFlowQueries{db.New(conn)}
	runs := flowrun.New(conn, queries, "proj", "sqlite")
	run, _, err := runs.Create(ctx, flowrun.Run{ConversationID: "S1", InvocationID: "submitted", Kind: "generated", Status: "planning", Snapshot: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	controller := &testSessionFlows{run: run}
	svc.sessionFlows = controller
	svc.app = &app.App{Questions: question.NewService(), FlowRuns: runs}
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	if _, err = svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: peer.Channel, IdentityID: peer.Identity, PeerID: peer.PeerID, SessionID: "S1"}); err != nil {
		t.Fatal(err)
	}
	prompts := []question.Prompt{{Question: "Continue?", Options: []question.Option{{Label: "Yes"}}}}
	sub := svc.app.Questions.Subscribe(ctx)
	answer := make(chan [][]string, 1)
	go func() { a, _ := svc.app.Questions.Ask(ctx, "S1", prompts); answer <- a }()
	request := (<-sub).Payload
	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{"S1": {requestID: request.ID, prompts: prompts}}}
	svc.questionRouter = router
	_, logical, handled, err := router.flowQuestionBindings(ctx, request)
	if err != nil || handled || logical != "" {
		t.Fatalf("ordinary question captured: %q %v %v", logical, handled, err)
	}
	svc.dispatchInbound(ctx, bridge.Inbound{Peer: peer, Text: "Yes", Source: source})
	select {
	case got := <-answer:
		if len(got) != 1 || got[0][0] != "Yes" {
			t.Fatalf("answer=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary root question was stranded behind submitted plan")
	}
	if len(controller.queued) > 0 {
		t.Fatal("ordinary question answer queued to not-yet-started plan")
	}
}

func TestNativeQuestionReplyRejectsBatchedInput(t *testing.T) {
	if reason := validateInboundBody(bridge.Inbound{QuestionRequestID: "q", Messages: []bridge.InboundMessage{{ID: "m", Text: "steer"}}}); reason == "" {
		t.Fatal("question identity accepted on a steering batch")
	}
	if reason := validateInboundBody(bridge.Inbound{QuestionRequestID: "q", Text: "custom answer"}); reason != "" {
		t.Fatal(reason)
	}
}
