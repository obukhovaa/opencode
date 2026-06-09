// Package telegram is the bridge's Telegram adapter. It uses
// github.com/go-telegram/bot (a self-contained Go SDK with zero
// transitive deps) for the long-polling and REST API surface.
package telegram

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Telegram chat_ids are signed integers. Negative IDs are groups /
// supergroups / channels; positive IDs are users / private chats.
var chatIDPattern = regexp.MustCompile(`^-?\d+$`)

// IsPeerID reports whether s is a valid Telegram peerId (numeric chat_id
// form). Username-style @handles are NOT valid bridge peerIds — the bridge
// addresses chats by chat_id only.
func IsPeerID(s string) bool {
	return chatIDPattern.MatchString(strings.TrimSpace(s))
}

// ErrInvalidPeerID is returned for non-numeric peerIds. The TS bridge
// distinguishes this case so the agent's error message points at the
// chat_id requirement.
var ErrInvalidPeerID = errors.New("telegram: peerId must be a numeric chat_id; usernames like @name are not valid")

// ParsePeerID parses a Telegram peerId into its int64 chat_id form.
// Returns ErrInvalidPeerID on non-numeric inputs.
func ParsePeerID(s string) (int64, error) {
	t := strings.TrimSpace(s)
	if !chatIDPattern.MatchString(t) {
		return 0, ErrInvalidPeerID
	}
	v, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, ErrInvalidPeerID
	}
	return v, nil
}

// StripMention removes "@<botUsername>" tokens from text in a
// case-insensitive way, trims the result. The Telegram bot's @-handle is
// case-insensitive per the platform — match accordingly.
//
// Returns the input unchanged when botUsername is empty.
func StripMention(text, botUsername string) string {
	if botUsername == "" {
		return text
	}
	lower := strings.ToLower(botUsername)
	var out strings.Builder
	i := 0
	for i < len(text) {
		if text[i] == '@' && i+1+len(lower) <= len(text) &&
			strings.EqualFold(text[i+1:i+1+len(lower)], lower) {
			// Skip the @bot token; insert a space if neither side
			// already has whitespace so adjacent words don't fuse.
			i += 1 + len(lower)
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return strings.TrimSpace(out.String())
}

// MentionsBot reports whether text contains an @-mention of botUsername.
// Used to gate inbound dispatch in group chats (per the chat-bridge spec:
// group messages MUST require an @mention to be forwarded to the agent).
func MentionsBot(text, botUsername string) bool {
	if botUsername == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), "@"+strings.ToLower(botUsername))
}
