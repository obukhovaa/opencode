package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractPathsFromCall_TriggerRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call ToolCall
		want []string
	}{
		{
			name: "glob pattern directory prefix without path",
			call: ToolCall{Name: GlobToolName, Input: `{"pattern":"services/auth/**/*.go"}`},
			want: []string{"services/auth"},
		},
		{
			name: "glob bare wildcard pattern yields nothing",
			call: ToolCall{Name: GlobToolName, Input: `{"pattern":"*.go"}`},
			want: nil,
		},
		{
			name: "glob combines path and pattern prefix",
			call: ToolCall{Name: GlobToolName, Input: `{"pattern":"auth/**/*.go","path":"services"}`},
			want: []string{"services", "services/auth"},
		},
		{
			name: "glob literal pattern uses its dirname",
			call: ToolCall{Name: GlobToolName, Input: `{"pattern":"services/auth/main.go"}`},
			want: []string{"services/auth"},
		},
		{
			name: "glob brace pattern stops at the metacharacter",
			call: ToolCall{Name: GlobToolName, Input: `{"pattern":"services/{a,b}/*.go"}`},
			want: []string{"services"},
		},
		{
			name: "grep without path returns nothing",
			call: ToolCall{Name: GrepToolName, Input: `{"pattern":"TODO"}`},
			want: nil,
		},
		{
			name: "grep with path returns it",
			call: ToolCall{Name: GrepToolName, Input: `{"pattern":"TODO","path":"services/auth"}`},
			want: []string{"services/auth"},
		},
		{
			name: "read returns file_path",
			call: ToolCall{Name: ReadToolName, Input: `{"file_path":"services/auth/handler.go"}`},
			want: []string{"services/auth/handler.go"},
		},
		{
			name: "patch returns paths from section headers",
			call: ToolCall{Name: PatchToolName, Input: `{"patch_text":"*** Begin Patch\n*** Update File: services/auth/handler.go\n*** Add File: services/billing/new.go\n*** End Patch"}`},
			want: []string{"services/auth/handler.go", "services/billing/new.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ExtractPathsFromCall(tt.call))
		})
	}
}

func TestExtractTargetDirsFromCall(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	authDir := filepath.Join(workDir, "services", "auth")
	require.NoError(t, os.MkdirAll(authDir, 0o755))
	authFile := filepath.Join(authDir, "handler.go")
	require.NoError(t, os.WriteFile(authFile, []byte("package auth"), 0o644))

	tests := []struct {
		name string
		call ToolCall
		want []string
	}{
		{
			name: "read derives the parent directory",
			call: ToolCall{Name: ReadToolName, Input: `{"file_path":"services/auth/handler.go"}`},
			want: []string{authDir},
		},
		{
			name: "write derives the parent of a not-yet-existing file",
			call: ToolCall{Name: WriteToolName, Input: `{"file_path":"services/auth/new.go"}`},
			want: []string{authDir},
		},
		{
			name: "multiedit derives the parent directory of file_path",
			call: ToolCall{Name: MultiEditToolName, Input: `{"file_path":"services/auth/handler.go","edits":[{"old_string":"a","new_string":"b"}]}`},
			want: []string{authDir},
		},
		{
			name: "absolute file_path is used as-is",
			call: ToolCall{Name: ReadToolName, Input: `{"file_path":"` + authFile + `"}`},
			want: []string{authDir},
		},
		{
			name: "ls takes its path verbatim",
			call: ToolCall{Name: LSToolName, Input: `{"path":"services/auth"}`},
			want: []string{authDir},
		},
		{
			name: "grep directory path is itself",
			call: ToolCall{Name: GrepToolName, Input: `{"pattern":"x","path":"services/auth"}`},
			want: []string{authDir},
		},
		{
			name: "grep file path resolves to its parent",
			call: ToolCall{Name: GrepToolName, Input: `{"pattern":"x","path":"services/auth/handler.go"}`},
			want: []string{authDir},
		},
		{
			name: "grep without path activates nothing",
			call: ToolCall{Name: GrepToolName, Input: `{"pattern":"x"}`},
			want: nil,
		},
		{
			name: "glob dedupes the search path against the combined prefix dir",
			call: ToolCall{Name: GlobToolName, Input: `{"pattern":"auth/**/*.go","path":"services"}`},
			want: []string{filepath.Join(workDir, "services"), authDir},
		},
		{
			name: "patch fans out to each touched parent directory",
			call: ToolCall{Name: PatchToolName, Input: `{"patch_text":"*** Begin Patch\n*** Update File: services/auth/handler.go\n*** Add File: services/billing/new.go\n*** End Patch"}`},
			want: []string{authDir, filepath.Join(workDir, "services", "billing")},
		},
		{
			name: "non-trigger tool is fail-closed",
			call: ToolCall{Name: BashToolName, Input: `{"path":"services/auth"}`},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ExtractTargetDirsFromCall(tt.call, workDir))
		})
	}
}
