package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/question"
)

func pendingReplyRequest(t *testing.T, svc *Service) (question.Request, <-chan [][]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	created := svc.app.Questions.Subscribe(ctx)
	result := make(chan [][]string, 1)
	go func() {
		answers, _ := svc.app.Questions.Ask(ctx, "S1", []question.Prompt{{Question: "Approve?", Options: []question.Option{{Label: "Approve"}, {Label: "Reject"}}}})
		result <- answers
	}()
	select {
	case ev := <-created:
		return ev.Payload, result
	case <-time.After(time.Second):
		t.Fatal("question was not created")
		return question.Request{}, nil
	}
}

func replyThroughRouter(t *testing.T, svc *Service, id string, body any) int {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/router/questions/"+id+"/reply", bytes.NewReader(data)))
	return w.Code
}

func TestBoundQuestionReplyClearsNudgesAndSuppressesDuplicateInbound(t *testing.T) {
	svc, router, adapter, _ := newInboxTestService(t, false)
	req, result := pendingReplyRequest(t, svc)
	router.handleNewRequest(context.Background(), req)
	before := len(adapter.Sends())
	if status := replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}}}); status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	select {
	case got := <-result:
		if !reflect.DeepEqual(got, [][]string{{"Approve"}}) {
			t.Fatalf("answers=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("question was not answered")
	}
	router.nudgeDue(context.Background(), time.Now().Add(time.Hour))
	if len(adapter.Sends()) != before {
		t.Fatal("answered question was nudged")
	}
	if !router.TryHandleQuestionReply(context.Background(), "S1", bridge.Inbound{Text: "Approve"}) {
		t.Fatal("duplicate answer was not suppressed")
	}
	if router.bufferedLen("S1") != 0 || len(svc.inboundCh) != 0 {
		t.Fatal("reply entered generic inbound")
	}
}

func TestBoundQuestionReplyRejectsWrongIdentityAndCannotAnswerNextQuestion(t *testing.T) {
	svc, router, _, _ := newInboxTestService(t, false)
	req, _ := pendingReplyRequest(t, svc)
	router.handleNewRequest(context.Background(), req)
	for _, body := range []any{
		map[string]any{"sessionId": "other", "answers": [][]string{{"Approve"}}},
		map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}, {"Reject"}}},
	} {
		if replyThroughRouter(t, svc, req.ID, body) != http.StatusConflict {
			t.Fatal("out-of-scope reply accepted")
		}
	}
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}}}) != http.StatusOK {
		t.Fatal("valid reply refused")
	}
	next, _ := pendingReplyRequest(t, svc)
	router.handleNewRequest(context.Background(), next)
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}}}) != http.StatusConflict {
		t.Fatal("stale reply accepted")
	}
	if len(svc.app.Questions.List()) != 1 || svc.app.Questions.List()[0].ID != next.ID {
		t.Fatal("stale reply answered the next question")
	}
}

func TestBoundQuestionReplyBeforeFanoutDoesNotResurrectRequest(t *testing.T) {
	svc, router, adapter, _ := newInboxTestService(t, false)
	req, _ := pendingReplyRequest(t, svc)
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}}}) != http.StatusOK {
		t.Fatal("early reply refused")
	}
	router.handleNewRequest(context.Background(), req)
	router.nudgeDue(context.Background(), time.Now().Add(time.Hour))
	if len(adapter.Sends()) != 0 || len(router.pending) != 0 {
		t.Fatal("late Created event resurrected answered question")
	}
}

func TestBoundQuestionReplyValidatesBody(t *testing.T) {
	svc, _, _, _ := newInboxTestService(t, false)
	for _, body := range []any{nil, map[string]any{"sessionId": "S1"}, map[string]any{"sessionId": "S1", "answers": [][]string{{" "}}}} {
		if replyThroughRouter(t, svc, "q", body) != http.StatusBadRequest {
			t.Fatal("malformed reply accepted")
		}
	}
}
