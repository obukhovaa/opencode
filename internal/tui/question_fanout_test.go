package tui

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

func TestConcurrentQuestionsArePresentedOneAtATime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := question.NewService()
	events := svc.Subscribe(ctx)
	ask := func(session string) {
		go func() { _, _ = svc.Ask(ctx, session, []question.Prompt{{Question: session}}) }()
	}
	receive := func() question.Request {
		select {
		case e := <-events:
			return e.Payload
		case <-time.After(time.Second):
			t.Fatal("question event missing")
			return question.Request{}
		}
	}
	ask("first")
	first := receive()
	ask("second")
	second := receive()
	m := appModel{app: &app.App{Questions: svc}, questionDialog: dialog.NewQuestionDialogCmp(), showQuestionDialog: true}
	// A new request cannot replace the visible dialog.
	updated, cmd := m.Update(pubsub.Event[question.Request]{Payload: second})
	if cmd != nil {
		t.Fatal("second request replaced visible question")
	}
	m = updated.(appModel)
	updated, _ = m.Update(dialog.QuestionResponseMsg{RequestID: first.ID, Answers: [][]string{{"yes"}}})
	m = updated.(appModel)
	if !m.showQuestionDialog {
		t.Fatal("pending second question not shown")
	}
	updated, _ = m.Update(dialog.QuestionResponseMsg{RequestID: second.ID, Rejected: true})
	if updated.(appModel).showQuestionDialog {
		t.Fatal("dialog remains after last question")
	}
}
