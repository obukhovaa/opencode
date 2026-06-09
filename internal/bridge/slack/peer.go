// Package slack is the bridge's Slack adapter. It uses
// github.com/slack-go/slack (the de-facto Go SDK) with its Socket Mode
// sub-package for inbound event dispatch.
package slack

import (
	"errors"
	"regexp"
	"strings"
)

// Peer is a parsed Slack peer ID.
type Peer struct {
	// ChannelID is the Slack channel identifier. Possible prefixes:
	//   D...  direct message channel
	//   C...  public channel
	//   G...  private channel / group
	// Always populated for a valid peer.
	ChannelID string
	// ThreadTS is the timestamp of the thread root, populated when the
	// peer addresses a specific thread within a channel.
	ThreadTS string
}

// FormatPeerID renders a Peer back to the string form used as
// bridge_sessions.peer_id and in /router/* HTTP request bodies. DM peers
// render as just the channel ID; thread peers render as
// "<channelID>|<threadTS>". The `|` separator matches the TS bridge and
// avoids clashing with ALLOW_FROM's `channel:peer` parsing.
func FormatPeerID(p Peer) string {
	if p.ThreadTS == "" {
		return p.ChannelID
	}
	return p.ChannelID + "|" + p.ThreadTS
}

// ParsePeerID inverts FormatPeerID. Whitespace is trimmed; malformed
// input yields a Peer with empty ChannelID (invalid; caller MUST reject).
func ParsePeerID(s string) Peer {
	t := strings.TrimSpace(s)
	if t == "" {
		return Peer{}
	}
	parts := strings.SplitN(t, "|", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return Peer{ChannelID: parts[0], ThreadTS: parts[1]}
	}
	return Peer{ChannelID: parts[0]}
}

// ErrInvalidPeerID is returned by Send when peerId is empty or malformed.
var ErrInvalidPeerID = errors.New("slack: peerId must be a channel ID, optionally followed by |<thread_ts>")

// userIDPattern recognises the canonical Slack user-id shape: starts
// with 'U' followed by alphanumerics (typically 9-13 chars). Used by
// ResolveUserToDM to distinguish user IDs (which need conversations.open)
// from already-resolved DM channel IDs (D-prefix).
var userIDPattern = regexp.MustCompile(`^U[A-Z0-9]+$`)

// LooksLikeUserID reports whether s is a Slack user ID shape — the
// adapter's ResolveUserToDM uses this to decide whether to call
// conversations.open.
func LooksLikeUserID(s string) bool { return userIDPattern.MatchString(s) }

// mentionPattern matches "<@UXXX>" (Slack's canonical bot-mention format).
// We allow trailing punctuation after the closing > so the leading-mention
// strip pulls "<@UBOT>: hello" down to "hello".
var mentionTokenPattern = regexp.MustCompile(`<@[A-Z0-9]+>`)

// leadingPunctRegex matches leading colon/comma/dash sequences left
// behind by a mention removal.
var leadingPunctRegex = regexp.MustCompile(`^\s*[:,\-]+\s*`)

// StripMention removes "<@botUserID>" tokens from text and any leading
// punctuation/whitespace left behind by the removal. The TS bridge uses
// the same pattern.
//
// When botUserID is empty all "<@U...>" tokens are removed (defensive
// scrub for echoed envelopes); typically callers pass the bot's own
// user ID.
func StripMention(text, botUserID string) string {
	if botUserID == "" {
		return strings.TrimSpace(leadingPunctRegex.ReplaceAllString(text, ""))
	}
	out := strings.ReplaceAll(text, "<@"+botUserID+">", " ")
	out = leadingPunctRegex.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

// MentionsBot reports whether text references the bot's user ID via Slack's
// canonical "<@UXXX>" form.
func MentionsBot(text, botUserID string) bool {
	if botUserID == "" {
		return false
	}
	return strings.Contains(text, "<@"+botUserID+">")
}

// IsDM reports whether channelID is a direct-message channel (D-prefix
// in Slack's canonical encoding).
func IsDM(channelID string) bool {
	return strings.HasPrefix(channelID, "D")
}
