package message

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolSearchPartsMarshalRoundTrip(t *testing.T) {
	parts := []ContentPart{
		ReasoningContent{Thinking: "let me search", Signature: "sig"},
		ToolSearchContent{
			ToolUseID:  "srvtoolu_1",
			Name:       "tool_search_tool_regex",
			Input:      `{"query":"slack"}`,
			References: []string{"mcp_slack_send_message", "mcp_slack_list_channels"},
		},
		ToolSearchContent{ToolUseID: "srvtoolu_2", Name: "tool_search_tool_regex", Input: `{}`, ErrorCode: "too_many_requests"},
		TextContent{Text: "found it"},
	}

	data, err := marshallParts(parts)
	require.NoError(t, err)
	got, err := unmarshallParts(data)
	require.NoError(t, err)
	require.Len(t, got, 4)

	ts, ok := got[1].(ToolSearchContent)
	require.True(t, ok, "tool search part must round-trip as ToolSearchContent")
	assert.Equal(t, "srvtoolu_1", ts.ToolUseID)
	assert.Equal(t, []string{"mcp_slack_send_message", "mcp_slack_list_channels"}, ts.References)

	tsErr, ok := got[2].(ToolSearchContent)
	require.True(t, ok)
	assert.Equal(t, "too_many_requests", tsErr.ErrorCode)
}

func TestSetToolSearchPartsPositioning(t *testing.T) {
	m := &Message{Parts: []ContentPart{
		ReasoningContent{Thinking: "hmm"},
		TextContent{Text: "answer"},
	}}
	m.SetToolSearchParts([]ToolSearchContent{{ToolUseID: "x", Name: "tool_search_tool_regex"}})

	require.Len(t, m.Parts, 3)
	_, isReasoning := m.Parts[0].(ReasoningContent)
	_, isSearch := m.Parts[1].(ToolSearchContent)
	_, isText := m.Parts[2].(TextContent)
	assert.True(t, isReasoning && isSearch && isText,
		"tool search parts must sit after reasoning, before text (emission order)")

	// Replacement, not accumulation.
	m.SetToolSearchParts([]ToolSearchContent{{ToolUseID: "y", Name: "tool_search_tool_regex"}})
	require.Len(t, m.Parts, 3)
	ts := m.ToolSearchParts()
	require.Len(t, ts, 1)
	assert.Equal(t, "y", ts[0].ToolUseID)
}
