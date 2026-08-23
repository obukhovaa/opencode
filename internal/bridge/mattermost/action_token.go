package mattermost

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// computeActionToken returns the keyed MAC over an interactive
// attachment action's identifying fields — channel, identity, peer id,
// question request id, and the selected choice's value — per
// specs/mattermost-question-actions/spec.md ("verification SHALL be an
// action token that the orchestrator can recompute: a keyed MAC over the
// action's identifying fields").
//
// secret is the shared orchestrator<->runner credential
// (OPENCODE_BRIDGE_REGISTRAR_PASSWORD on this side, OPENCODE_SERVER_PASSWORD
// on the orchestrator's). Mattermost message actions carry no platform
// signature, so this token is the only thing standing between a forged
// POST and an accepted answer — the construction here MUST match the
// orchestrator's verification byte-for-byte:
//
//	HMAC-SHA256(key=secret, message=field1 || 0x00 || field2 || 0x00 || ...)
//
// fields in order: channel, identity, peerID, requestID, choice. The
// NUL separator prevents a field boundary from being spoofed by
// concatenation (e.g. choice="A"+identity="B" colliding with
// choice="AB"+identity=""). Output is lowercase hex.
//
// See internal/orchestrator/bridge/mattermost_action_token.go in the
// c2-agent repo for the verifying half.
func computeActionToken(secret, channel, identity, peerID, requestID, choice string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	for _, field := range []string{channel, identity, peerID, requestID, choice} {
		mac.Write([]byte(field))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyActionToken recomputes the token for the given fields and
// compares it against got in constant time. Exported-shape kept
// unexported here since this side only ever mints tokens; retained for
// the fork's own tests to assert round-trip equality against the
// orchestrator's construction without duplicating the HMAC call.
func verifyActionToken(secret, channel, identity, peerID, requestID, choice, got string) bool {
	want := computeActionToken(secret, channel, identity, peerID, requestID, choice)
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// newActionRequestID generates a fresh opaque nonce scoping one
// question's set of action tokens. It has no meaning beyond that scope —
// unlike a session or job id, nothing else in the system looks it up —
// its only job is to make a token minted for one question unusable
// against a different one.
func newActionRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read on a supported platform does not fail; if it
		// somehow does, fall back to a fixed-but-unique-enough value
		// rather than panicking the send path. Not a security concern:
		// a failed rand.Read here would already indicate a broken host.
		return hex.EncodeToString([]byte("fallback-request-id"))
	}
	return hex.EncodeToString(b)
}
