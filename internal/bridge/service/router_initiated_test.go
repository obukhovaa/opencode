package service

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// Phase 7.5 test scenarios — verify router-initiated conversation flow
// works end-to-end against the public Service API + stub adapters.

// TestRouterInitiatedFirstMessageReachesPeer is the headline scenario:
// the bridge binds a session to a peer that has NEVER messaged the bot,
// then SendBySessionID delivers the agent's first turn to that peer.
// Per the chat-bridge-router-initiated spec, this is the primary flow
// for c2-agent's interactive flow steps.
func TestRouterInitiatedFirstMessageReachesPeer(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	// Bind first — the peer has never DM'd the bot.
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D012345", Mention: "<@U01ABC>"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// First outbound to the bound session — agent's first turn.
	results, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{
		Text: "Please review the spec",
	})
	if err != nil {
		t.Fatalf("SendBySessionID: %v", err)
	}
	if len(results) != 1 || !results[0].Delivered {
		t.Fatalf("results = %+v", results)
	}

	sends := ad.Sends()
	if len(sends) != 1 {
		t.Fatalf("adapter Sends = %d, want 1", len(sends))
	}
	if sends[0].Peer.PeerID != "D012345" || sends[0].Text != "Please review the spec" {
		t.Errorf("send 0 = %+v", sends[0])
	}
	if sends[0].Mention != "<@U01ABC>" {
		t.Errorf("first-send mention = %q, want <@U01ABC>", sends[0].Mention)
	}
}

// TestSlackChannelToThreadMutationFullPath verifies the channel→thread
// mutation: bind to a channel-only peer "C0DEF456" → first send returns
// "C0DEF456|<ts>" → binding row's peer_id is rewritten → second send
// uses the new form.
func TestSlackChannelToThreadMutationFullPath(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	ad := newStubAdapter("slack", "default")
	ad.resolveOnSend = "C0DEF456|1700000123.000200"
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "C0DEF456"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// First turn.
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "first"}); err != nil {
		t.Fatalf("Send 1: %v", err)
	}

	// Binding should now be at C0DEF456|ts form.
	mutated, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "C0DEF456|1700000123.000200")
	if err != nil {
		t.Fatalf("Get mutated: %v", err)
	}
	if mutated.SessionID != "S1" {
		t.Errorf("mutated SessionID = %q", mutated.SessionID)
	}

	// Second turn — Send should now hit the thread form via the binding.
	// (Stub's resolveOnSend is still set; the spec accepts repeated mutation
	// returns, so the value gets re-applied. For this test we only care
	// that the second send happens.)
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "second"}); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	sends := ad.Sends()
	if len(sends) != 2 {
		t.Fatalf("sends = %d", len(sends))
	}
	// The second send's peer should be the mutated form.
	if sends[1].Peer.PeerID != "C0DEF456|1700000123.000200" {
		t.Errorf("second send peer = %q, want C0DEF456|1700000123.000200", sends[1].Peer.PeerID)
	}
}

// TestMattermostChannelToRootPostMutationFullPath mirrors the Slack case
// for Mattermost. The orchestrator code is platform-agnostic — adapters
// just need to surface ResolvedPeer = "channelID|rootPostID".
func TestMattermostChannelToRootPostMutationFullPath(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	ad := newStubAdapter("mattermost", "default")
	ad.resolveOnSend = "ch_only|root_post_abc"
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "mattermost", Identity: "default", PeerID: "ch_only"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "first"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := svc.store.GetBinding(context.Background(), "proj", "mattermost", "default", "ch_only|root_post_abc")
	if err != nil {
		t.Fatalf("Get mutated: %v", err)
	}
	if got.SessionID != "S1" {
		t.Errorf("mutated SessionID = %q", got.SessionID)
	}
}

// TestBindResolvesUserIDViaAdapter verifies Bind calls
// adapter.ResolveUserToDM for U-id form peers (Slack/Mattermost) and
// persists the resolved D-id. The stub adapter's resolvedTo field
// substitutes for the platform's conversations.open / channels/direct
// round-trip.
func TestBindResolvesUserIDViaAdapter(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	ad := newStubAdapter("slack", "default")
	ad.resolvedTo = "D012345" // every peer resolves to this DM channel
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	results, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "U01ABC"},
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d", len(results))
	}
	if results[0].ResolvedPeerID != "D012345" {
		t.Errorf("ResolvedPeerID = %q", results[0].ResolvedPeerID)
	}
	// Persisted row should use the resolved D-id, not the U-id.
	b, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D012345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.SessionID != "S1" {
		t.Errorf("SessionID = %q", b.SessionID)
	}
}

// TestMentionPrefixAppearsOnceNeverAgain is the canonical mention
// lifecycle test: first send prefixes the mention, subsequent sends
// don't.
func TestMentionPrefixAppearsOnceNeverAgain(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1", Mention: "<@U1>"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "turn"}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	sends := ad.Sends()
	if len(sends) != 3 {
		t.Fatalf("sends = %d", len(sends))
	}
	if sends[0].Mention != "<@U1>" {
		t.Errorf("send 0 mention = %q", sends[0].Mention)
	}
	if sends[1].Mention != "" || sends[2].Mention != "" {
		t.Errorf("sends 1+2 mentions = %q, %q (must be empty)", sends[1].Mention, sends[2].Mention)
	}
}

// TestPartialUnbindPreservesDispatcher verifies that Unbind with a
// subset of peers leaves other bindings alive and keeps the dispatcher
// running.
func TestPartialUnbindPreservesDispatcher(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	slack := newStubAdapter("slack", "default")
	tg := newStubAdapter("telegram", "default")
	_ = svc.RegisterAdapter(context.Background(), slack)
	_ = svc.RegisterAdapter(context.Background(), tg)

	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
		{Channel: "telegram", Identity: "default", PeerID: "12345"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// Drop just the Slack peer.
	if err := svc.Unbind(context.Background(), "S1", bridge.PeerRef{
		Channel: "slack", Identity: "default", PeerID: "D1",
	}); err != nil {
		t.Fatalf("Unbind partial: %v", err)
	}
	svc.dispatchMu.Lock()
	_, dispatcherStillRunning := svc.dispatchers["S1"]
	svc.dispatchMu.Unlock()
	if !dispatcherStillRunning {
		t.Errorf("dispatcher torn down on partial unbind; want preserved")
	}
	// Send should now only hit Telegram.
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(slack.Sends()) != 0 {
		t.Errorf("Slack received send after partial unbind: %+v", slack.Sends())
	}
	if len(tg.Sends()) != 1 {
		t.Errorf("Telegram sends = %d, want 1", len(tg.Sends()))
	}
}

// TestReBindResetsMentionConsumed verifies the bridge-storage spec
// scenario: unbind a peer and rebind with the same mention; the new
// row's mention_consumed_at is NULL so the next outbound uses the
// prefix again.
func TestReBindResetsMentionConsumed(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	ad := newStubAdapter("slack", "default")
	_ = svc.RegisterAdapter(context.Background(), ad)

	// Bind 1 — first send consumes the mention.
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1", Mention: "<@U1>"},
	}); err != nil {
		t.Fatalf("Bind 1: %v", err)
	}
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "hi"}); err != nil {
		t.Fatalf("Send 1: %v", err)
	}

	// Unbind.
	if err := svc.Unbind(context.Background(), "S1"); err != nil {
		t.Fatalf("Unbind: %v", err)
	}

	// Re-bind with same mention.
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1", Mention: "<@U1>"},
	}); err != nil {
		t.Fatalf("Bind 2: %v", err)
	}
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "hi again"}); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	sends := ad.Sends()
	if len(sends) != 2 {
		t.Fatalf("sends = %d", len(sends))
	}
	if sends[0].Mention != "<@U1>" {
		t.Errorf("first bind first send mention = %q", sends[0].Mention)
	}
	if sends[1].Mention != "<@U1>" {
		t.Errorf("re-bind first send mention = %q; want <@U1> (re-bind resets)", sends[1].Mention)
	}
}

// TestUnknownChannelInBindRejected verifies validation per the
// chat-bridge-router-initiated spec scenario "Bind fails on unknown
// channel".
func TestUnknownChannelInBindRejected(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	results, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "irc", Identity: "default", PeerID: "x"},
	})
	if err != nil {
		// Bind returns aggregated per-peer results, not a top-level
		// error, when the failure is per-peer (no adapter for channel).
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Errorf("expected per-peer error, got: %+v", results)
	}
	if !strings.Contains(results[0].Err.Error(), "no adapter") {
		t.Errorf("err = %v; want adapter-missing error", results[0].Err)
	}
}
