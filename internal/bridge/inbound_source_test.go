package bridge

import "testing"

// TestAnswerWasAcknowledgedByTransport pins the ack-suppression truth table
// that the question router relies on (GENAI-151): only sources that leave the
// reviewer with NO visible confirmation get an explicit acknowledgment; a
// button (self-renders "✓ Answered") and any unknown/empty source are treated
// as already-acked so an unstamped inbound from an older orchestrator is never
// double-acknowledged.
func TestAnswerWasAcknowledgedByTransport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		source      string
		alreadyAckd bool
	}{
		{InboundSourceButton, true},      // widget already shows "✓ Answered"
		{"", true},                       // unknown / unstamped → suppress (never double-ack a button)
		{"someFutureSource", true},       // unknown → suppress
		{InboundSourceMessage, false},    // typed DM/channel answer → ack
		{InboundSourceAppMention, false}, // @mention answer → ack
		{InboundSourceModal, false},      // custom-answer modal submit → ack
	}
	for _, c := range cases {
		got := Inbound{Source: c.source}.AnswerWasAcknowledgedByTransport()
		if got != c.alreadyAckd {
			t.Errorf("source=%q AnswerWasAcknowledgedByTransport()=%v, want %v", c.source, got, c.alreadyAckd)
		}
	}
}
