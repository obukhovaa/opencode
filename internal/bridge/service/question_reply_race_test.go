package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/question"
)

type interleavedQuestionReply struct {
	question.Service
	claimed atomic.Bool
	before  func()
}

func (s *interleavedQuestionReply) Reply(id string, answers [][]string) error {
	if s.claimed.CompareAndSwap(false, true) {
		s.before()
	}
	return s.Service.Reply(id, answers)
}

func TestBoundReplyWinsBufferedRaceWithoutResurrectionOrLostInput(t *testing.T) {
	svc, router, adapter, _ := newInboxTestService(t, false)
	req, result := pendingReplyRequest(t, svc)
	router.BufferInbound(context.Background(), "S1", bridge.Inbound{Peer: extPeer, Text: "follow up"})
	original := svc.app.Questions
	svc.app.Questions = &interleavedQuestionReply{Service: original, before: func() {
		if status := replyThroughRouter(t, svc, req.ID, map[string]any{"sessionId": "S1", "text": "Approve"}); status != 200 {
			t.Fatalf("bound reply=%d", status)
		}
	}}
	router.handleNewRequest(context.Background(), req)
	select {
	case got := <-result:
		if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "Approve" {
			t.Fatalf("answer=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("question not answered")
	}
	router.nudgeDue(context.Background(), time.Now().Add(time.Hour))
	if len(router.pending) != 0 || len(adapter.Sends()) != 0 {
		t.Fatal("settled request resurrected or sent stale widget/nudge")
	}
	items := router.box().DrainAll("S1")
	if len(items) != 1 || items[0].Meta.(bridge.Inbound).Text != "follow up" {
		t.Fatalf("buffered input lost: %+v", items)
	}
}

func TestBufferedReplyFailureCannotRestoreAfterClearSession(t *testing.T) {
	svc, router, adapter, _ := newInboxTestService(t, false)
	req, _ := pendingReplyRequest(t, svc)
	router.BufferInbound(context.Background(), "S1", bridge.Inbound{Peer: extPeer, Text: "old step input"})
	original := svc.app.Questions
	svc.app.Questions = &interleavedQuestionReply{Service: original, before: func() {
		router.ClearSession("S1")
		if err := original.Reject(req.ID); err != nil {
			t.Fatal(err)
		}
	}}
	router.handleNewRequest(context.Background(), req)
	if router.bufferedLen("S1") != 0 || len(router.pending) != 0 || len(adapter.Sends()) != 0 {
		t.Fatal("old step restored state after unbind")
	}
}
