package slack

import (
	"reflect"
	"testing"

	slackgo "github.com/slack-go/slack"
)

func TestBuildAnsweredBlocksPreservesOriginalPromptSection(t *testing.T) {
	t.Parallel()
	prompt := slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.MarkdownType, "Ship it?", false, false),
		nil, nil,
	)
	actions := slackgo.NewActionBlock("router_question",
		slackgo.NewButtonBlockElement("router_q_0", "Yes",
			slackgo.NewTextBlockObject(slackgo.PlainTextType, "Yes", false, false)),
	)
	original := []slackgo.Block{prompt, actions}

	got := buildAnsweredBlocks(original, "", []string{"Yes"})

	if len(got) != 2 {
		t.Fatalf("expected 2 blocks (prompt + confirmation), got %d", len(got))
	}
	if got[0] != prompt {
		t.Errorf("first block must be the preserved prompt section")
	}
	section, ok := got[1].(*slackgo.SectionBlock)
	if !ok {
		t.Fatalf("second block must be a section, got %T", got[1])
	}
	if section.Text == nil || section.Text.Text != "✓ Answered: Yes" {
		t.Errorf("unexpected confirmation text: %+v", section.Text)
	}
}

func TestBuildAnsweredBlocksMultiSelectCommaJoinsLabels(t *testing.T) {
	t.Parallel()
	prompt := slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.MarkdownType, "Pick caps", false, false),
		nil, nil,
	)
	got := buildAnsweredBlocks([]slackgo.Block{prompt}, "",
		[]string{"auth", "ui"})

	section, ok := got[1].(*slackgo.SectionBlock)
	if !ok {
		t.Fatalf("second block must be a section, got %T", got[1])
	}
	if section.Text.Text != "✓ Answered: auth, ui" {
		t.Errorf("unexpected text: %q", section.Text.Text)
	}
}

func TestBuildAnsweredBlocksFallbackWhenNoSectionInOriginal(t *testing.T) {
	t.Parallel()
	// Original has only an actions block — defensive path.
	actions := slackgo.NewActionBlock("router_question",
		slackgo.NewButtonBlockElement("router_q_0", "Yes",
			slackgo.NewTextBlockObject(slackgo.PlainTextType, "Yes", false, false)),
	)
	got := buildAnsweredBlocks([]slackgo.Block{actions}, "Fallback prompt", []string{"Yes"})

	if len(got) != 2 {
		t.Fatalf("expected 2 blocks even on fallback, got %d", len(got))
	}
	first, ok := got[0].(*slackgo.SectionBlock)
	if !ok {
		t.Fatalf("first block should be synthesised section, got %T", got[0])
	}
	if first.Text.Text != "Fallback prompt" {
		t.Errorf("fallback section text not set, got %q", first.Text.Text)
	}
}

func TestSplitMultiSelectValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"Yes", []string{"Yes"}},
		{"auth, ui", []string{"auth", "ui"}},
		{"  auth ,, ui  ", []string{"auth", "ui"}},
		{"", []string{""}}, // edge case: empty input still produces a one-element slice
	}
	for _, tc := range cases {
		got := splitMultiSelectValue(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitMultiSelectValue(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
