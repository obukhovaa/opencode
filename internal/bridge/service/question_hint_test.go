package service

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/question"
)

func TestRenderQuestionPromptCustomEnabledAppendsTrailingClause(t *testing.T) {
	t.Parallel()
	customOn := true
	got := renderQuestionPrompt([]question.Prompt{{
		Question: "Pick capabilities",
		Options: []question.Option{
			{Label: "auth"},
			{Label: "billing"},
		},
		Custom: &customOn,
	}})
	if !strings.Contains(got, "or type your own answer") {
		t.Errorf("expected custom-answer clause; got:\n%s", got)
	}
	if !strings.Contains(got, "1) auth") {
		t.Errorf("expected numbered list preserved; got:\n%s", got)
	}
}

func TestRenderQuestionPromptCustomDisabledOmitsTrailingClause(t *testing.T) {
	t.Parallel()
	customOff := false
	got := renderQuestionPrompt([]question.Prompt{{
		Question: "Pick capabilities",
		Options: []question.Option{
			{Label: "auth"},
			{Label: "billing"},
		},
		Custom: &customOff,
	}})
	if strings.Contains(got, "or type your own answer") {
		t.Errorf("custom-disabled prompt must not advertise typed answers; got:\n%s", got)
	}
	if !strings.HasSuffix(got, "Reply with the number of your choice.") {
		t.Errorf("expected base trailing clause; got:\n%s", got)
	}
}

func TestRenderQuestionPromptDefaultCustomEnabled(t *testing.T) {
	t.Parallel()
	// Custom defaults to true when the field is nil.
	got := renderQuestionPrompt([]question.Prompt{{
		Question: "Ship it?",
		Options:  []question.Option{{Label: "Yes"}, {Label: "No"}},
	}})
	if !strings.Contains(got, "or type your own answer") {
		t.Errorf("default-Custom prompt should advertise typed answers; got:\n%s", got)
	}
}

func TestRenderQuestionPromptNoOptionsHasNoTrailingClause(t *testing.T) {
	t.Parallel()
	// A free-text prompt has no numbered list — the "Reply with the number"
	// clause should be suppressed because there are no numbers.
	got := renderQuestionPrompt([]question.Prompt{{
		Question: "What do you think?",
	}})
	if strings.Contains(got, "Reply with the number") {
		t.Errorf("free-text prompt must not show numbered-reply clause; got:\n%s", got)
	}
}
