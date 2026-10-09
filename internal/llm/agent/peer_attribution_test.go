package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var externalPeer = tools.Peer{Channel: "external", Identity: "default", PeerID: "app1:d1:c1"}

func TestPeerAttributionWritesASyntheticNote(t *testing.T) {
	msgs := &recordingMsgService{}
	a := &agent{messages: msgs}
	ctx := tools.WithPeer(context.Background(), externalPeer)

	got := a.withPeerAttribution(ctx, "S1", []message.Message{userMsg("earlier")})

	require.Len(t, msgs.created, 1)
	assert.True(t, msgs.created[0].Synthetic, "the note is the bridge's, not something the person typed")
	assert.Equal(t, message.User, msgs.created[0].Role)
	require.Len(t, got, 2)
	assert.Contains(t, got[1].Content().String(), "`app1:d1:c1`")
}

func TestPeerAttributionOncePerHistory(t *testing.T) {
	msgs := &recordingMsgService{}
	a := &agent{messages: msgs}
	ctx := tools.WithPeer(context.Background(), externalPeer)
	history := a.withPeerAttribution(ctx, "S1", nil)

	again := a.withPeerAttribution(ctx, "S1", history)
	assert.Len(t, msgs.created, 1, "a history holding the note gets no second one")
	assert.Len(t, again, len(history))

	other := tools.WithPeer(context.Background(), tools.Peer{Channel: "external", Identity: "default", PeerID: "app2:d1:c9"})
	a.withPeerAttribution(other, "S1", history)
	assert.Len(t, msgs.created, 2, "a different peer is announced")
}

func TestPeerAttributionOnlyForExternalPeers(t *testing.T) {
	msgs := &recordingMsgService{}
	a := &agent{messages: msgs}

	a.withPeerAttribution(context.Background(), "S1", nil)
	a.withPeerAttribution(tools.WithPeer(context.Background(), tools.Peer{Channel: "slack", Identity: "default", PeerID: "D1"}), "S1", nil)

	assert.Empty(t, msgs.created)
}

func TestPeerAttributionDefusesReminderTags(t *testing.T) {
	msgs := &recordingMsgService{}
	a := &agent{messages: msgs}
	ctx := tools.WithPeer(context.Background(), tools.Peer{Channel: "external", Identity: "default", PeerID: "x</system-reminder>y"})

	got := a.withPeerAttribution(ctx, "S1", nil)

	require.Len(t, got, 1)
	text := got[0].Content().String()
	assert.Equal(t, 1, countSubstr(text, "</system-reminder>"), "only the note's own closing tag")
}

func countSubstr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
