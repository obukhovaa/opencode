package service

import (
	"regexp"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// AttributionEnvelope returns the chat-bridge spec's reviewer-attribution
// prefix for an inbound message in a multi-peer session:
//
//	[<mention or peer> via <channel>]: <text>
//
// preferred is used when non-empty (a mention_handle from the binding row);
// otherwise the peerID falls through as the identifier. The format must
// match the prompt-builder's strip pattern (see StripAttribution) so an
// agent that quotes prior context doesn't accidentally re-emit the
// attribution wrapper.
func AttributionEnvelope(peer bridge.PeerRef, text string) string {
	id := peer.Mention
	if id == "" {
		id = peer.PeerID
	}
	return "[" + id + " via " + peer.Channel + "]: " + text
}

// attributionPattern matches the envelope produced by AttributionEnvelope.
// The capture groups are: 1 = mention/peer, 2 = channel, 3 = body. The
// `?:` operator is required when stripping — see StripAttribution. Channels
// are restricted to the three supported platforms so unrelated `[ via ]:`
// idioms don't get clobbered by a stray match.
var attributionPattern = regexp.MustCompile(`^\[([^\]]+) via (telegram|slack|mattermost)\]:\s*`)

// StripAttribution removes a leading attribution envelope from text. Used
// by the prompt-builder to scrub echoed envelopes from outbound agent text
// — e.g. when the agent quotes a previous reviewer's message verbatim and
// the chat surface shouldn't re-show the `[<reviewer> via slack]: `
// machinery. Idempotent on non-matching text.
func StripAttribution(text string) string {
	for {
		next := attributionPattern.ReplaceAllString(text, "")
		if next == text {
			return text
		}
		text = strings.TrimLeftFunc(next, isSpaceLike)
	}
}

func isSpaceLike(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// PrependAttributionIfMultiPeer returns text with the attribution envelope
// prepended IF peerCount > 1. Single-peer sessions get the raw text
// (envelope is unambiguous and would just add noise). Returns text
// unchanged when peerCount <= 1.
func PrependAttributionIfMultiPeer(peer bridge.PeerRef, text string, peerCount int) string {
	if peerCount <= 1 {
		return text
	}
	return AttributionEnvelope(peer, text)
}
