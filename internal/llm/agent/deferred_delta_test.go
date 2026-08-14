package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The rendered message must name no specific tool-search tool: which one the
// model holds a schema for is decided per request by the provider (native
// server-side tool_search_tool_regex vs the client-side `toolsearch`), while
// this message is persisted once and outlives mid-session model switches.
// Naming `toolsearch` here is what made native-path models emit a `toolsearch`
// call carrying the server tool's `pattern` argument.
func TestBuildDeferredDeltaNamesNoToolSearchTool(t *testing.T) {
	msg := buildDeferredDelta([]string{"gitlab_get_file_contents", "teamcity_trigger_build"})

	assert.NotContains(t, msg, "toolsearch",
		"naming a concrete tool-search tool makes native-path models call the wrong one")
	assert.Contains(t, msg, "- gitlab_get_file_contents\n")
	assert.Contains(t, msg, "- teamcity_trigger_build\n")
	assert.True(t, strings.HasPrefix(msg, "<system-reminder>\n"+deferredDeltaMarker))
	assert.True(t, strings.HasSuffix(msg, "</system-reminder>"))
}

// The marker doubles as the history-scan key that stops a restarted process
// from re-announcing every MCP tool, so it must keep matching deltas written
// by builds that used the old wording. The scan then harvests names from
// "- <name>" lines, which both formats share.
func TestDeferredDeltaMarkerMatchesLegacyDeltas(t *testing.T) {
	legacy := "<system-reminder>\nThe following deferred tools are now available via toolsearch." +
		" Their schemas are NOT loaded — call toolsearch to load a tool before using it:\n" +
		"- jira_add_comment\n</system-reminder>"

	assert.Contains(t, legacy, deferredDeltaMarker,
		"legacy delta messages must still dedup against the current marker")
	assert.Contains(t, buildDeferredDelta([]string{"jira_add_comment"}), deferredDeltaMarker,
		"current delta messages must dedup against their own marker")
}
