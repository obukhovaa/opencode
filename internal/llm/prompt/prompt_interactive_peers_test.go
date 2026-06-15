package prompt

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/stretchr/testify/assert"
)

// These tests exercise interactiveStructuredOutputPromptFor and its
// per-channel renderers directly — without invoking the full
// getAgentPromptInternal — so they don't need the agent registry, model
// config, etc. set up. The full-pipeline gating (Interactive flag,
// non-interactive call ignoring BoundPeers) is exercised by the
// downstream wiring tests in internal/llm/agent/* / internal/flow/*.

func TestInteractivePrompt_NoBoundPeers_PreservesLegacyBase(t *testing.T) {
	// Scenario: caller hasn't been updated to populate BoundPeers
	// (TUI, ACP, older embedder). Output is the unchanged base const.
	got := interactiveStructuredOutputPromptFor(nil)
	assert.Equal(t, interactiveStructuredOutputPromptBase, got,
		"empty BoundPeers MUST return the base const verbatim — no Reviewer details section appended")
	assert.NotContains(t, got, "## Reviewer details",
		"no Reviewer details section when BoundPeers is empty")
}

func TestInteractivePrompt_SingleSlackPeer_RendersMentionAndChannel(t *testing.T) {
	peers := []bridge.PeerRef{{
		Channel:  "slack",
		Identity: "default",
		PeerID:   "C0123FAKEXX",
		Mention:  "@reviewer.example",
	}}
	got := interactiveStructuredOutputPromptFor(peers)

	assert.True(t, strings.HasPrefix(got, interactiveStructuredOutputPromptBase),
		"output MUST start with the base const")
	assert.Contains(t, got, "## Reviewer details")
	assert.Contains(t, got, "You are bound to @reviewer.example in Slack channel `C0123FAKEXX`.")
	assert.Contains(t, got, "Begin your FIRST `router_send` with the mention `@reviewer.example` to ping them.")
	assert.NotContains(t, got, "outbound fans out to all",
		"single-peer render MUST NOT include the multi-reviewer preamble")
}

func TestInteractivePrompt_EmptyMention_DropsPingSentence(t *testing.T) {
	// Slack DM peerId — Mention is empty because pinging a DM makes no
	// sense. The channel + peerId still render, but the "begin with
	// <mention>" sentence is omitted.
	peers := []bridge.PeerRef{{
		Channel:  "slack",
		Identity: "default",
		PeerID:   "D0123ABCDEF",
		Mention:  "",
	}}
	got := interactiveStructuredOutputPromptFor(peers)

	assert.Contains(t, got, "You are bound to the bound peer in Slack channel `D0123ABCDEF`.")
	assert.NotContains(t, got, "Begin your FIRST",
		"empty Mention MUST drop the ping-sentence — agent should not be told to ping a non-existent mention")
}

func TestInteractivePrompt_TwoPeers_RendersFanOutPreambleAndNumberedList(t *testing.T) {
	peers := []bridge.PeerRef{
		{
			Channel:  "slack",
			Identity: "default",
			PeerID:   "C0123FAKEXX",
			Mention:  "@alice",
		},
		{
			Channel:  "telegram",
			Identity: "default",
			PeerID:   "344281281",
			Mention:  "@Bob",
		},
	}
	got := interactiveStructuredOutputPromptFor(peers)

	assert.Contains(t, got, "## Reviewer details")
	assert.Contains(t, got, "You are bound to 2 reviewers; outbound fans out to all, inbound from any routes back here with `[<who> via <channel>]: ` attribution prefix.")
	assert.Contains(t, got, "1. You are bound to @alice in Slack channel `C0123FAKEXX`.")
	assert.Contains(t, got, "2. You are bound to chat `344281281` on Telegram.")
	assert.Contains(t, got, "The reviewer's first-message ping handle is `@Bob` (use it once in your FIRST `router_send`).")
}

func TestInteractivePrompt_MattermostChannelAndMention(t *testing.T) {
	peers := []bridge.PeerRef{{
		Channel:  "mattermost",
		Identity: "local",
		PeerID:   "x1234567890abcdef12345678",
		Mention:  "@reviewer",
	}}
	got := interactiveStructuredOutputPromptFor(peers)

	assert.Contains(t, got, "You are bound to @reviewer in Mattermost channel `x1234567890abcdef12345678`.")
	assert.Contains(t, got, "Begin your FIRST `router_send` with the mention `@reviewer` to ping them.")
}

func TestInteractivePrompt_UnknownChannel_FallsBackGenerically(t *testing.T) {
	// Defensive: a future channel (say, "discord") not handled by the
	// per-channel switch MUST NOT silently drop the peer. The default
	// branch emits a generic line so the agent at least sees something.
	peers := []bridge.PeerRef{{
		Channel: "discord",
		PeerID:  "session-xyz",
		Mention: "@user",
	}}
	got := interactiveStructuredOutputPromptFor(peers)

	assert.Contains(t, got, "You are bound to peer `session-xyz` on channel `discord`.")
	assert.Contains(t, got, "First-message handle: `@user`.")
}

func TestInteractivePrompt_NonInteractiveGatingIsCallersResponsibility(t *testing.T) {
	// interactiveStructuredOutputPromptFor itself doesn't gate on
	// Interactive — getAgentPromptInternal does (only calls this
	// helper inside the `opts.Interactive || info.Interactive`
	// branch). This test guards the contract by exercising the helper
	// directly with BoundPeers: it always renders the section.
	// Non-interactive callers in production never reach this helper
	// because the prompt builder routes them to structuredOutputPrompt
	// instead.
	peers := []bridge.PeerRef{{Channel: "slack", PeerID: "C0", Mention: "@x"}}
	got := interactiveStructuredOutputPromptFor(peers)
	assert.Contains(t, got, "## Reviewer details",
		"the helper itself always renders for non-empty peers; gating lives in the caller")
}
