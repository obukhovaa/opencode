package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

// Keyword scoring ORs its terms over substrings, so one broad query can score
// most of a large MCP fleet. Loading every hit would dump all their schemas
// into the context, which is what deferral exists to prevent.
func TestToolSearchCapsKeywordFanOut(t *testing.T) {
	seq := &atomic.Int64{}
	all := make([]BaseTool, 0, 40)
	wrapped := make([]*DeferredWrapper, 0, 40)
	for i := range 40 {
		w := WrapDeferred(fakeTool{
			name: fmt.Sprintf("mcp__gitlab__get_thing_%02d", i),
			desc: "A GitLab tool",
		}, seq)
		wrapped = append(wrapped, w)
		all = append(all, w)
	}
	ts := NewToolSearchTool()
	ts.BindToolset(append(all, ts))

	activeCount := func(sessionID string) int {
		n := 0
		for _, w := range wrapped {
			if _, on := w.ActivatedAt(sessionID); on {
				n++
			}
		}
		return n
	}

	t.Run("keyword search is capped and says so", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-cap")
		resp, err := ts.Run(ctx, ToolCall{Input: `{"query":"gitlab thing"}`})
		require.NoError(t, err)
		assert.Equal(t, maxKeywordMatches, activeCount("s-cap"),
			"a broad keyword query must not load the whole fleet")
		assert.Contains(t, resp.Content, "further tools also matched")
	})

	t.Run("regex pattern fan-out is capped too", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-cap-re")
		_, err := ts.Run(ctx, ToolCall{Input: `{"pattern":"^mcp__gitlab__get_thing_[0-9]+$"}`})
		require.NoError(t, err)
		assert.Equal(t, maxKeywordMatches, activeCount("s-cap-re"))
	})

	t.Run("select: is never capped", func(t *testing.T) {
		// The model named each of these, so every hit is something it asked
		// for — unlike a keyword query, which can match by accident.
		names := make([]string, 0, 20)
		for i := range 20 {
			names = append(names, fmt.Sprintf("mcp__gitlab__get_thing_%02d", i))
		}
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-select")
		resp, err := ts.Run(ctx, ToolCall{Input: fmt.Sprintf(`{"query":"select:%s"}`, strings.Join(names, ","))})
		require.NoError(t, err)
		assert.Equal(t, 20, activeCount("s-select"))
		assert.NotContains(t, resp.Content, "further tools also matched")
	})
}

// A model that also knows Anthropic's server-side tool_search_tool_regex can
// aim a call at this tool while filling in that tool's `pattern` argument.
// Honour it rather than burning a turn on "query is required". The value is a
// regex while matching here is literal, so every form below has to survive
// normalization — an unnormalized "jira_.*" matches nothing.
func TestToolSearchAcceptsNativePatternArgument(t *testing.T) {
	newToolset := func() (*DeferredWrapper, *DeferredWrapper, *ToolSearchTool) {
		seq := &atomic.Int64{}
		jira := WrapDeferred(fakeTool{name: "jira_add_comment", desc: "Add a comment to a Jira issue"}, seq)
		slack := WrapDeferred(fakeTool{name: "mcp_slack_send_message", desc: "Send a message to a Slack channel"}, seq)
		ts := NewToolSearchTool()
		ts.BindToolset([]BaseTool{jira, slack, ts})
		return jira, slack, ts
	}

	tests := []struct {
		name          string
		pattern       string
		wantAlsoSlack bool
	}{
		{name: "literal tool name", pattern: "jira_add_comment"},
		{name: "bare keyword", pattern: "jira"},
		{name: "trailing wildcard", pattern: "jira_.*"},
		{name: "start anchor", pattern: "^jira"},
		{name: "wrapped wildcards", pattern: ".*jira.*"},
		{name: "end anchor", pattern: "^jira_add_comment$"},
		{name: "inline flag group", pattern: "(?i)jira"},
		{name: "escape class", pattern: `jira_\w+`},
		{name: "character class", pattern: "jira[a-z_]+comment"},
		{name: "repeat spec", pattern: "jira_add_comment{1}"},
		{name: "alternation", pattern: "jira_add_comment|mcp_slack_send_message", wantAlsoSlack: true},
		{name: "non-capturing group alternation", pattern: "(?:jira|slack)", wantAlsoSlack: true},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jira, slack, ts := newToolset()
			sessionID := fmt.Sprintf("s%d", i)
			ctx := context.WithValue(context.Background(), SessionIDContextKey, sessionID)

			resp, err := ts.Run(ctx, ToolCall{Input: fmt.Sprintf(`{"pattern":%q}`, tt.pattern)})
			require.NoError(t, err)
			// Assert on the loaded-tools header, not on the tool name: the
			// no-match branch also prints every deferred name, so a bare
			// Contains("jira_add_comment") passes when nothing matched.
			assert.Contains(t, resp.Content, "now loaded and available to call")
			assert.Contains(t, resp.Content, "## jira_add_comment")
			_, on := jira.ActivatedAt(sessionID)
			assert.True(t, on, "pattern-only call must still activate the matched tool")

			_, on = slack.ActivatedAt(sessionID)
			assert.Equal(t, tt.wantAlsoSlack, on, "alternation must reach every named tool, and only those")
		})
	}

	t.Run("query wins when both are present", func(t *testing.T) {
		_, _, ts := newToolset()
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-both")
		resp, err := ts.Run(ctx, ToolCall{Input: `{"query":"jira_add_comment","pattern":"nonexistent_xyz"}`})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "now loaded")
	})

	t.Run("query is never regex-normalized", func(t *testing.T) {
		// `+term` is this tool's own require-this-term syntax; the regex
		// reading of `+` would strip it.
		_, slack, ts := newToolset()
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-plus")
		resp, err := ts.Run(ctx, ToolCall{Input: `{"query":"+jira comment message"}`})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "jira_add_comment")
		_, on := slack.ActivatedAt("s-plus")
		assert.False(t, on, "+jira must still exclude the slack tool")
	})

	t.Run("match-everything pattern lists the deferred names", func(t *testing.T) {
		// ".*" normalizes to nothing. Falling back to the raw pattern lands on
		// the no-match branch, which names what is still deferred — a model
		// that did search deserves that over "query is required".
		_, _, ts := newToolset()
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-any")
		resp, err := ts.Run(ctx, ToolCall{Input: `{"pattern":".*"}`})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "No deferred tools matched")
		assert.Contains(t, resp.Content, "jira_add_comment")
		assert.Contains(t, resp.Content, "mcp_slack_send_message")
	})

	t.Run("neither still errors", func(t *testing.T) {
		_, _, ts := newToolset()
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-none")
		resp, err := ts.Run(ctx, ToolCall{Input: `{}`})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "query is required")
	})

	// Normalization strips the literal text out of "[a-z]+" and friends. What
	// it must never do is leave the connecting punctuation behind as a term:
	// the matcher is substring-based, so a bare "_" scores against every tool
	// name and would activate the whole deferred set at once.
	t.Run("punctuation debris never becomes a search term", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			pattern   string
			wantNamed bool // matched the two tools it actually names
		}{
			{name: "reduces to debris only", pattern: "[a-z]+_[a-z]+"},
			{name: "escape classes around debris", pattern: `\w+_\w+`},
			// Bare punctuation is already debris, so normalization returns ""
			// and the empty query must NOT fall back to the raw pattern — "_"
			// substring-matches every tool name there is.
			{name: "bare punctuation", pattern: "_"},
			{name: "debris alongside real terms", pattern: "^(jira|slack)_", wantNamed: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				seq := &atomic.Int64{}
				jira := WrapDeferred(fakeTool{name: "jira_add_comment", desc: "Add a comment to a Jira issue"}, seq)
				slack := WrapDeferred(fakeTool{name: "mcp_slack_send_message", desc: "Send a message to a Slack channel"}, seq)
				other := WrapDeferred(fakeTool{name: "gitlab_get_file_contents", desc: "Read a file from a repo"}, seq)
				ts := NewToolSearchTool()
				ts.BindToolset([]BaseTool{jira, slack, other, ts})

				sessionID := "s-debris-" + tt.name
				ctx := context.WithValue(context.Background(), SessionIDContextKey, sessionID)
				_, err := ts.Run(ctx, ToolCall{Input: fmt.Sprintf(`{"pattern":%q}`, tt.pattern)})
				require.NoError(t, err)

				_, on := other.ActivatedAt(sessionID)
				assert.False(t, on, "a tool the pattern never named must stay deferred")
				for _, named := range []*DeferredWrapper{jira, slack} {
					_, on := named.ActivatedAt(sessionID)
					assert.Equal(t, tt.wantNamed, on, named.Info().Name)
				}
			})
		}
	})
}
