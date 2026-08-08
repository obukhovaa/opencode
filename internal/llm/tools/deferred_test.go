package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTool struct {
	name     string
	desc     string
	baseline bool
}

func (f fakeTool) Info() ToolInfo {
	return ToolInfo{
		Name:        f.name,
		Description: f.desc,
		Parameters: map[string]any{
			"arg": map[string]any{"type": "string", "description": "an argument"},
		},
		Required: []string{"arg"},
	}
}

func (f fakeTool) Run(ctx context.Context, params ToolCall) (ToolResponse, error) {
	return NewTextResponse("ok"), nil
}
func (f fakeTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool { return true }
func (f fakeTool) IsBaseline() bool                                         { return f.baseline }

func TestDeferredWrapperSessionIsolation(t *testing.T) {
	seq := &atomic.Int64{}
	w := WrapDeferred(fakeTool{name: "jira_comment"}, seq)

	_, on := w.ActivatedAt("s1")
	assert.False(t, on, "fresh wrapper must not be activated")

	w.Activate("s1")
	s1First, on := w.ActivatedAt("s1")
	require.True(t, on)

	_, on = w.ActivatedAt("s2")
	assert.False(t, on, "session 2 must not observe session 1's activation")

	w.Activate("s1") // idempotent
	s1Again, _ := w.ActivatedAt("s1")
	assert.Equal(t, s1First, s1Again, "re-activation must keep the original sequence")

	w.Activate("")
	_, on = w.ActivatedAt("")
	assert.False(t, on, "empty session id must not activate")
}

func TestSerializableForOrdering(t *testing.T) {
	seq := &atomic.Int64{}
	regular := fakeTool{name: "read", baseline: true}
	a := WrapDeferred(fakeTool{name: "a_tool"}, seq)
	b := WrapDeferred(fakeTool{name: "b_tool"}, seq)
	all := []BaseTool{regular, a, b}

	// Nothing activated: only the regular tool serializes.
	got := SerializableFor("s1", all)
	require.Len(t, got, 1)
	assert.Equal(t, "read", got[0].Info().Name)

	// Activate b first, then a: append order must be activation order,
	// not toolset order — previously serialized positions stay stable.
	b.Activate("s1")
	a.Activate("s1")
	got = SerializableFor("s1", all)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"read", "b_tool", "a_tool"},
		[]string{got[0].Info().Name, got[1].Info().Name, got[2].Info().Name})

	// Identity for toolsets without wrappers.
	plain := []BaseTool{regular, fakeTool{name: "write", baseline: true}}
	got = SerializableFor("s1", plain)
	require.Len(t, got, 2)
}

func toolSearchCall(t *testing.T, query string) ToolCall {
	t.Helper()
	input, err := json.Marshal(toolSearchParams{Query: query})
	require.NoError(t, err)
	return ToolCall{Name: ToolSearchToolName, Input: string(input)}
}

func TestToolSearchMatchingAndActivation(t *testing.T) {
	seq := &atomic.Int64{}
	slack := WrapDeferred(fakeTool{name: "mcp_slack_send_message", desc: "Send a message to a Slack channel"}, seq)
	jira := WrapDeferred(fakeTool{name: "jira_add_comment", desc: "Add a comment to a Jira issue"}, seq)
	ts := NewToolSearchTool()
	ts.BindToolset([]BaseTool{fakeTool{name: "read", baseline: true}, slack, jira, ts})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")

	t.Run("keyword search activates", func(t *testing.T) {
		resp, err := ts.Run(ctx, toolSearchCall(t, "send slack message"))
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "mcp_slack_send_message")
		assert.Contains(t, resp.Content, "Parameters:")
		_, on := slack.ActivatedAt("s1")
		assert.True(t, on, "matched tool must be activated for the calling session")
		_, on = jira.ActivatedAt("s1")
		assert.False(t, on)
	})

	t.Run("already-activated is disambiguated", func(t *testing.T) {
		resp, err := ts.Run(ctx, toolSearchCall(t, "mcp_slack_send_message"))
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "Already loaded")
	})

	t.Run("select multi-select", func(t *testing.T) {
		resp, err := ts.Run(ctx, toolSearchCall(t, "select:jira_add_comment"))
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "jira_add_comment")
		_, on := jira.ActivatedAt("s1")
		assert.True(t, on)
	})

	t.Run("no match lists deferred names", func(t *testing.T) {
		ctx2 := context.WithValue(context.Background(), SessionIDContextKey, "s2")
		resp, err := ts.Run(ctx2, toolSearchCall(t, "nonexistent_xyz"))
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "No deferred tools matched")
		assert.Contains(t, resp.Content, "jira_add_comment")
		assert.Contains(t, resp.Content, "mcp_slack_send_message")
	})

	t.Run("required term filters", func(t *testing.T) {
		ctx3 := context.WithValue(context.Background(), SessionIDContextKey, "s3")
		resp, err := ts.Run(ctx3, toolSearchCall(t, "+jira comment message"))
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "jira_add_comment")
		assert.False(t, strings.Contains(resp.Content, "## mcp_slack_send_message"),
			"+jira must exclude the slack tool")
	})

	t.Run("sessions are isolated in search state", func(t *testing.T) {
		ctx4 := context.WithValue(context.Background(), SessionIDContextKey, "s4")
		resp, err := ts.Run(ctx4, toolSearchCall(t, "select:mcp_slack_send_message"))
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "now loaded", "s4 must be able to activate independently")
	})
}
