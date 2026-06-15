package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	slackgo "github.com/slack-go/slack"
)

// CustomAnswerMetadata mirrors the orchestrator-side
// `bridge.CustomAnswerMetadata` field-for-field. Stored in the
// modal's `private_metadata` field so the orchestrator (running in
// mediated-inbound mode) can reassemble the binding key + request id
// when the reviewer submits the form.
//
// Wire format MUST stay byte-compatible with c2-agent's
// orchestrator/bridge.CustomAnswerMetadata. Adding fields is allowed;
// renaming or removing is a breaking change both sides must
// coordinate.
type CustomAnswerMetadata struct {
	PeerID    string `json:"peerId"`
	RequestID string `json:"requestId,omitempty"`
}

// OpenCustomAnswerModal opens a Slack views.open modal with a single
// plain_text_input field labelled "Your answer". The reviewer's
// submitted value lands at the orchestrator's view_submission
// handler (openspec change bridge-orchestrator-mediated-inbound,
// Phase I), which decodes `private_metadata` to find the bound
// peer + request and forwards a synthesized Inbound back to this
// runner.
//
// Callers MUST set trigger_id (Slack assigns one per user
// interaction — the action that led the reviewer to want to type a
// custom answer). meta.PeerID is required; meta.RequestID is
// optional but recommended so the question-router can match the
// inbound to the right pending Ask call instead of falling through
// to generic-input handling.
func (a *Adapter) OpenCustomAnswerModal(ctx context.Context, triggerID string, meta CustomAnswerMetadata) error {
	if triggerID == "" {
		return errors.New("slack: OpenCustomAnswerModal: trigger_id is required")
	}
	if meta.PeerID == "" {
		return errors.New("slack: OpenCustomAnswerModal: meta.PeerID is required")
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("slack: OpenCustomAnswerModal: marshal metadata: %w", err)
	}
	// Slack caps private_metadata at 3000 chars. Our payload is
	// tiny (two short strings) but guard against future growth so
	// the API call doesn't 400 with an opaque error.
	if len(metaJSON) > 3000 {
		return fmt.Errorf("slack: OpenCustomAnswerModal: metadata exceeds 3000-char Slack limit (got %d)", len(metaJSON))
	}

	input := slackgo.NewPlainTextInputBlockElement(
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Type your answer…", false, false),
		"custom_answer",
	)
	input.Multiline = true
	inputBlock := slackgo.NewInputBlock(
		"custom_answer_block",
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Your answer", false, false),
		nil, // hint
		input,
	)
	view := slackgo.ModalViewRequest{
		Type:            slackgo.VTModal,
		CallbackID:      "custom_answer",
		Title:           slackgo.NewTextBlockObject(slackgo.PlainTextType, "Custom answer", false, false),
		Submit:          slackgo.NewTextBlockObject(slackgo.PlainTextType, "Submit", false, false),
		Close:           slackgo.NewTextBlockObject(slackgo.PlainTextType, "Cancel", false, false),
		Blocks:          slackgo.Blocks{BlockSet: []slackgo.Block{inputBlock}},
		PrivateMetadata: string(metaJSON),
	}
	if _, err := a.api.OpenViewContext(ctx, triggerID, view); err != nil {
		return fmt.Errorf("slack: views.open: %w", err)
	}
	return nil
}
