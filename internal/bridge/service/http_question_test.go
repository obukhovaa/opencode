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
	return pendingReplyPrompts(t, svc, []question.Prompt{{Question: "Approve?", Options: []question.Option{{Label: "Approve"}, {Label: "Reject"}}}})
}

func pendingReplyPrompts(t *testing.T, svc *Service, prompts []question.Prompt) (question.Request, <-chan [][]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	created := svc.app.Questions.Subscribe(ctx)
	result := make(chan [][]string, 1)
	go func() {
		answers, _ := svc.app.Questions.Ask(ctx, "S1", prompts)
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
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "other", "answers": [][]string{{"Approve"}}}) != http.StatusConflict {
		t.Fatal("out-of-scope reply accepted")
	}
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}, {"Reject"}}}) != http.StatusBadRequest {
		t.Fatal("bad answer count accepted")
	}
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}}}) != http.StatusOK {
		t.Fatal("valid reply refused")
	}
	next, _ := pendingReplyRequest(t, svc)
	router.handleNewRequest(context.Background(), next)
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Approve"}}}) != http.StatusOK {
		t.Fatal("identical replay was not acknowledged")
	}
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "answers": [][]string{{"Reject"}}}) != http.StatusConflict {
		t.Fatal("conflicting replay was accepted")
	}
	if len(svc.app.Questions.List()) != 1 || svc.app.Questions.List()[0].ID != next.ID {
		t.Fatal("stale reply answered the next question")
	}
}

func TestBoundQuestionReplyValidatesAndCanonicalizesAtRunner(t *testing.T) {
	customOff := false
	for _, tc := range []struct {
		name             string
		multiple, custom bool
		body             map[string]any
		want             int
		answers          [][]string
		labels           []string
	}{
		{"numeric", false, false, map[string]any{"text": "1"}, 200, [][]string{{"Approve"}}, nil},
		{"case", false, false, map[string]any{"answers": [][]string{{"approve"}}}, 200, [][]string{{"Approve"}}, nil},
		{"multiple", true, false, map[string]any{"text": "1, reject"}, 200, [][]string{{"Approve", "Reject"}}, nil},
		{"custom allowed", false, true, map[string]any{"text": "my own answer"}, 200, [][]string{{"my own answer"}}, nil},
		{"custom refused", false, false, map[string]any{"text": "my own answer"}, 400, nil, nil},
		{"partial invalid refused", true, false, map[string]any{"text": "1, unknown"}, 400, nil, nil},
		{"too many", false, false, map[string]any{"answers": [][]string{{"Approve", "Reject"}}}, 400, nil, nil},
		{"two representations", false, false, map[string]any{"text": "Approve", "answers": [][]string{{"Approve"}}}, 400, nil, nil},
		{"numeric label beats index", false, false, map[string]any{"text": "3"}, 200, [][]string{{"3"}}, []string{"3", "6", "12"}},
		{"index without a matching label", false, false, map[string]any{"text": "2"}, 200, [][]string{{"6"}}, []string{"3", "6", "12"}},
		{"numeric labels on multiple", true, false, map[string]any{"text": "12, 3"}, 200, [][]string{{"12", "3"}}, []string{"3", "6", "12"}},
		{"comma label stays whole", true, false, map[string]any{"text": "Yes, ship it"}, 200, [][]string{{"Yes, ship it"}}, []string{"Yes, ship it", "No"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newInboxTestService(t, false)
			options := []question.Option{{Label: "Approve"}, {Label: "Reject"}}
			if tc.labels != nil {
				options = options[:0]
				for _, label := range tc.labels {
					options = append(options, question.Option{Label: label})
				}
			}
			prompt := question.Prompt{Question: "Approve?", Options: options, Multiple: tc.multiple}
			if !tc.custom {
				prompt.Custom = &customOff
			}
			req, result := pendingReplyPrompts(t, svc, []question.Prompt{prompt})
			tc.body["sessionId"] = "S1"
			if got := replyThroughRouter(t, svc, req.ID, tc.body); got != tc.want {
				t.Fatalf("status=%d want=%d", got, tc.want)
			}
			if tc.want == 200 {
				select {
				case got := <-result:
					if !reflect.DeepEqual(got, tc.answers) {
						t.Fatalf("answers=%v", got)
					}
				case <-time.After(time.Second):
					t.Fatal("no answer")
				}
			} else if len(svc.app.Questions.List()) != 1 {
				t.Fatal("invalid answer settled the request")
			}
		})
	}
}

func TestBoundCustomReplyDoesNotSuppressTypedFollowup(t *testing.T) {
	svc, router, _, _ := newInboxTestService(t, false)
	req, _ := pendingReplyRequest(t, svc)
	body := map[string]any{"sessionId": "S1", "text": "looks good"}
	if replyThroughRouter(t, svc, req.ID, body) != 200 {
		t.Fatal("custom reply refused")
	}
	if router.TryHandleQuestionReply(context.Background(), "S1", bridge.Inbound{Text: "looks good"}) {
		t.Fatal("typed custom followup swallowed as stale click")
	}
	if replyThroughRouter(t, svc, req.ID, body) != 200 {
		t.Fatal("custom retry not acknowledged")
	}
}

func TestBoundQuestionReplyPreservesQueuedInputBeforeFanout(t *testing.T) {
	svc, router, adapter, _ := newInboxTestService(t, false)
	req, result := pendingReplyRequest(t, svc)
	router.EnqueueBatch(context.Background(), "S1", batch("queued"))
	if replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "text": "Approve"}) != 409 {
		t.Fatal("queued input did not retain priority")
	}
	select {
	case got := <-result:
		if len(got) != 0 {
			t.Fatal("question was answered by queued input")
		}
	case <-time.After(time.Second):
		t.Fatal("question not closed as unanswered")
	}
	router.handleNewRequest(context.Background(), req)
	if router.bufferedLen("S1") != 1 || len(adapter.Sends()) != 0 {
		t.Fatal("queued item lost or stale question sent")
	}
}

func TestBoundQuestionReplyReceiptsAreBoundedAndExpire(t *testing.T) {
	svc, router, _, _ := newInboxTestService(t, false)
	req, _ := pendingReplyRequest(t, svc)
	body := map[string]any{"sessionId": "S1", "text": "Approve"}
	if replyThroughRouter(t, svc, req.ID, body) != 200 {
		t.Fatal("reply refused")
	}
	router.mu.Lock()
	receipt := router.settledReplies[req.ID]
	receipt.settledAt = time.Now().Add(-boundReplyReceiptTTL)
	router.settledReplies[req.ID] = receipt
	router.mu.Unlock()
	if replyThroughRouter(t, svc, req.ID, body) != 409 {
		t.Fatal("expired receipt reused")
	}
	for i := 0; i <= boundReplyReceiptCap; i++ {
		req, _ := pendingReplyRequest(t, svc)
		if replyThroughRouter(t, svc, req.ID, body) != 200 {
			t.Fatal("reply refused at capacity")
		}
	}
	if len(router.settledReplies) > boundReplyReceiptCap {
		t.Fatal("receipt cache grew without bound")
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
