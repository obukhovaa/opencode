package mattermost

import (
	"regexp"
	"strings"
)

// Peer is a parsed Mattermost peer ID.
type Peer struct {
	// ChannelID is the Mattermost channel identifier — DM channel, group
	// DM, or regular channel. Always populated for a valid peer.
	ChannelID string
	// RootPostID is the thread root identifier, populated when the peer
	// references a thread (channelID|rootPostID form).
	RootPostID string
}

// IsThread reports whether the peer references a specific thread.
func (p Peer) IsThread() bool { return p.RootPostID != "" }

// FormatPeerID renders a Peer back to the string form used as
// bridge_sessions.peer_id and in /router/* HTTP request bodies. DM peers
// render as just the channel ID; thread peers render as "channelID|rootPostID".
// The `|` separator is chosen to match the TS bridge (and to avoid clashing
// with ALLOW_FROM's `channel:peer` parsing).
func FormatPeerID(p Peer) string {
	if p.RootPostID == "" {
		return p.ChannelID
	}
	return p.ChannelID + "|" + p.RootPostID
}

// ParsePeerID inverts FormatPeerID, tolerating whitespace and ill-formed
// input. An empty or whitespace-only string yields a zero Peer.
func ParsePeerID(peerID string) Peer {
	t := strings.TrimSpace(peerID)
	if t == "" {
		return Peer{}
	}
	parts := strings.SplitN(t, "|", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return Peer{ChannelID: parts[0], RootPostID: parts[1]}
	}
	return Peer{ChannelID: parts[0]}
}

// mattermostUserIDPattern recognises the canonical Mattermost user-ID
// shape: 26 characters from the base-32 alphabet [a-z0-9]. ResolveUserToDM
// uses this to decide whether a peerID is a user ID needing
// conversations.direct resolution vs. an already-resolved channel ID.
var mattermostUserIDPattern = regexp.MustCompile(`^[a-z0-9]{26}$`)

// LooksLikeUserID reports whether s matches the Mattermost user-id shape.
func LooksLikeUserID(s string) bool { return mattermostUserIDPattern.MatchString(s) }

// StripMention removes "@<botUsername>" tokens from text and any leading
// punctuation/whitespace left behind by the removal. Used to clean the
// inbound text in channel/group-channel contexts where the user @-mentions
// the bot to address it.
//
// If botUsername is empty the input is returned unchanged.
func StripMention(text, botUsername string) string {
	if botUsername == "" {
		return text
	}
	out := strings.ReplaceAll(text, "@"+botUsername, " ")
	out = leadingPunctRegex.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

// leadingPunctRegex matches leading colon/comma/dash sequences that
// follow a removed mention ("@bot: hello" → ":  hello" → "hello").
var leadingPunctRegex = regexp.MustCompile(`^\s*[:,\-]+\s*`)
