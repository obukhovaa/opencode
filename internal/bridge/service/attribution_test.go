package service

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func TestAttributionEnvelopeFormat(t *testing.T) {
	t.Parallel()
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "DBOB", Mention: "<@U07BOB>"}
	got := AttributionEnvelope(peer, "looks good")
	want := "[<@U07BOB> via slack]: looks good"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAttributionEnvelopeFallsBackToPeerID(t *testing.T) {
	t.Parallel()
	peer := bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"}
	got := AttributionEnvelope(peer, "hi")
	want := "[12345 via telegram]: hi"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAttributionRemovesEnvelope(t *testing.T) {
	t.Parallel()
	in := "[<@U07BOB> via slack]: looks good"
	got := StripAttribution(in)
	if got != "looks good" {
		t.Errorf("got %q", got)
	}
}

func TestStripAttributionRemovesNestedEnvelopes(t *testing.T) {
	t.Parallel()
	// Defensive: an agent quoting prior context could include multiple
	// stacked envelopes. StripAttribution should iterate until it's a
	// fixed point.
	in := "[a via slack]: [b via telegram]: hello"
	got := StripAttribution(in)
	if got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestStripAttributionIgnoresNonMatching(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"no envelope at all",
		"[looks-like-it]: but not really",
		"[<@U> via unknown]: x", // channel not in pattern allowlist
	} {
		if got := StripAttribution(s); got != s {
			t.Errorf("non-envelope %q got %q", s, got)
		}
	}
}

func TestPrependAttributionMultiVsSingle(t *testing.T) {
	t.Parallel()
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1", Mention: "<@U1>"}
	if got := PrependAttributionIfMultiPeer(peer, "hi", 1); got != "hi" {
		t.Errorf("single-peer got %q", got)
	}
	if got := PrependAttributionIfMultiPeer(peer, "hi", 3); got != "[<@U1> via slack]: hi" {
		t.Errorf("multi-peer got %q", got)
	}
}
