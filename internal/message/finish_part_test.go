package message

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A call cancelled during its tools was reported before it was cancelled:
// the cancel's finish must keep the mark, and the mark must survive storage.
func TestFinishReportedSurvivesRefinishAndStorage(t *testing.T) {
	m := &Message{Role: Assistant, Parts: []ContentPart{TextContent{Text: "calling a tool"}}}
	m.AddFinish(FinishReasonToolUse)
	m.MarkReported()
	m.AddFinish(FinishReasonCanceled)

	f := m.FinishPart()
	require.NotNil(t, f)
	assert.Equal(t, FinishReasonCanceled, f.Reason)
	assert.True(t, f.Reported, "a later finish must keep the report mark")

	data, err := marshallParts(m.Parts)
	require.NoError(t, err)
	parts, err := unmarshallParts(data)
	require.NoError(t, err)
	got := (&Message{Parts: parts}).FinishPart()
	require.NotNil(t, got)
	assert.True(t, got.Reported, "the report mark must round-trip")

	unreported := &Message{Role: Assistant}
	unreported.AddFinish(FinishReasonError)
	assert.False(t, unreported.FinishPart().Reported)
}
