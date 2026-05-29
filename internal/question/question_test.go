package question

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestAskReply(t *testing.T) {
	svc := NewService()

	// Subscribe to get the request
	ch := svc.Subscribe(context.Background())

	done := make(chan struct{})
	var answers [][]string
	var askErr error

	go func() {
		defer close(done)
		answers, askErr = svc.Ask(context.Background(), "sess1", []Prompt{
			{
				Question: "Pick a color",
				Options:  []Option{{Label: "Red", Description: "The color red"}, {Label: "Blue", Description: "The color blue"}},
			},
		})
	}()

	// Wait for event
	select {
	case event := <-ch:
		if event.Type != pubsub.CreatedEvent {
			t.Fatalf("expected CreatedEvent, got %v", event.Type)
		}
		req := event.Payload
		if len(req.Questions) != 1 {
			t.Fatalf("expected 1 question, got %d", len(req.Questions))
		}
		if req.SessionID != "sess1" {
			t.Fatalf("expected session sess1, got %s", req.SessionID)
		}

		// Reply with answer
		err := svc.Reply(req.ID, [][]string{{"Red"}})
		if err != nil {
			t.Fatalf("reply failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for question event")
	}

	// Wait for Ask to return
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask to return")
	}

	if askErr != nil {
		t.Fatalf("unexpected error: %v", askErr)
	}
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if len(answers[0]) != 1 || answers[0][0] != "Red" {
		t.Fatalf("unexpected answer: %v", answers[0])
	}
}

func TestAskReject(t *testing.T) {
	svc := NewService()
	ch := svc.Subscribe(context.Background())

	done := make(chan struct{})
	var askErr error

	go func() {
		defer close(done)
		_, askErr = svc.Ask(context.Background(), "sess1", []Prompt{
			{Question: "Pick", Options: []Option{{Label: "A", Description: "A"}}},
		})
	}()

	select {
	case event := <-ch:
		err := svc.Reject(event.Payload.ID)
		if err != nil {
			t.Fatalf("reject failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for question event")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask to return")
	}

	if askErr != ErrQuestionRejected {
		t.Fatalf("expected ErrQuestionRejected, got %v", askErr)
	}
}

func TestAskContextCancellation(t *testing.T) {
	svc := NewService()
	ch := svc.Subscribe(context.Background())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var askErr error

	go func() {
		defer close(done)
		_, askErr = svc.Ask(ctx, "sess1", []Prompt{
			{Question: "Pick", Options: []Option{{Label: "A", Description: "A"}}},
		})
	}()

	// Wait for event to be published
	select {
	case <-ch:
		// Cancel the context
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for question event")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask to return")
	}

	if askErr != ErrQuestionRejected {
		t.Fatalf("expected ErrQuestionRejected, got %v", askErr)
	}
}

func TestReplyNotFound(t *testing.T) {
	svc := NewService()
	err := svc.Reply("nonexistent", [][]string{{"A"}})
	if err == nil {
		t.Fatal("expected error for nonexistent request")
	}
}

func TestRejectNotFound(t *testing.T) {
	svc := NewService()
	err := svc.Reject("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent request")
	}
}

func TestIsCustomEnabled(t *testing.T) {
	tests := []struct {
		name     string
		custom   *bool
		expected bool
	}{
		{name: "nil (default true)", custom: nil, expected: true},
		{name: "explicit true", custom: boolPtr(true), expected: true},
		{name: "explicit false", custom: boolPtr(false), expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Prompt{Custom: tt.custom}
			if p.IsCustomEnabled() != tt.expected {
				t.Errorf("IsCustomEnabled() = %v, want %v", p.IsCustomEnabled(), tt.expected)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestMultipleQuestions(t *testing.T) {
	svc := NewService()
	ch := svc.Subscribe(context.Background())

	done := make(chan struct{})
	var answers [][]string
	var askErr error

	go func() {
		defer close(done)
		answers, askErr = svc.Ask(context.Background(), "sess1", []Prompt{
			{Question: "Q1", Options: []Option{{Label: "A", Description: "A"}}},
			{Question: "Q2", Options: []Option{{Label: "B", Description: "B"}, {Label: "C", Description: "C"}}},
		})
	}()

	select {
	case event := <-ch:
		if len(event.Payload.Questions) != 2 {
			t.Fatalf("expected 2 questions, got %d", len(event.Payload.Questions))
		}
		err := svc.Reply(event.Payload.ID, [][]string{{"A"}, {"B", "C"}})
		if err != nil {
			t.Fatalf("reply failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	<-done
	if askErr != nil {
		t.Fatalf("unexpected error: %v", askErr)
	}
	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(answers))
	}
	if answers[0][0] != "A" {
		t.Fatalf("expected A, got %s", answers[0][0])
	}
	if len(answers[1]) != 2 || answers[1][0] != "B" || answers[1][1] != "C" {
		t.Fatalf("unexpected second answer: %v", answers[1])
	}
}

func TestList(t *testing.T) {
	svc := NewService()
	ch := svc.Subscribe(context.Background())

	// No pending questions initially
	if pending := svc.List(); len(pending) != 0 {
		t.Fatalf("expected 0 pending, got %d", len(pending))
	}

	// Start a question (blocks in goroutine)
	go func() {
		_, _ = svc.Ask(context.Background(), "sess1", []Prompt{
			{Question: "Q1", Options: []Option{{Label: "A", Description: "A"}}},
		})
	}()

	// Wait for event
	select {
	case event := <-ch:
		// Now List should return 1 pending question
		pending := svc.List()
		if len(pending) != 1 {
			t.Fatalf("expected 1 pending, got %d", len(pending))
		}
		if pending[0].ID != event.Payload.ID {
			t.Fatalf("expected ID %s, got %s", event.Payload.ID, pending[0].ID)
		}
		if pending[0].Questions[0].Question != "Q1" {
			t.Fatalf("expected Q1, got %s", pending[0].Questions[0].Question)
		}

		// Reply to clean up
		_ = svc.Reply(event.Payload.ID, [][]string{{"A"}})
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	// After reply, list should be empty again
	time.Sleep(10 * time.Millisecond) // let Ask() return and defer cleanup
	if pending := svc.List(); len(pending) != 0 {
		t.Fatalf("expected 0 pending after reply, got %d", len(pending))
	}
}
