package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/stretchr/testify/require"
)

type patchPermissionRegistry struct {
	agentregistry.Registry
	evaluate func(string) permission.Action
}

func (r patchPermissionRegistry) EvaluatePermission(_, _ string, path string) permission.Action {
	return r.evaluate(path)
}

type patchPermissionService struct {
	permission.Service
	request func(permission.CreatePermissionRequest) bool
}

func (p patchPermissionService) Request(_ context.Context, request permission.CreatePermissionRequest) bool {
	return p.request(request)
}

func TestPatchMoveDestinationPermission(t *testing.T) {
	for _, verdict := range []permission.Action{permission.ActionDeny, permission.ActionAsk, permission.ActionAllow} {
		t.Run(string(verdict), func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.txt")
			destination := filepath.Join(dir, "destination.txt")
			writeAndTrack(t, source, "old\n")
			require.NoError(t, os.WriteFile(destination, []byte("existing\n"), 0o644))
			var checked []string
			reg := patchPermissionRegistry{evaluate: func(path string) permission.Action {
				checked = append(checked, path)
				if path == destination {
					return verdict
				}
				return permission.ActionAllow
			}}
			prompted := false
			perms := patchPermissionService{request: func(request permission.CreatePermissionRequest) bool {
				prompted = true
				require.Contains(t, request.Description, source)
				require.Contains(t, request.Description, destination)
				params := request.Params.(EditPermissionsParams)
				require.Contains(t, params.FilePath, destination)
				require.Contains(t, params.Diff, destination)
				return false
			}}
			tool := NewPatchTool(&noopLspService{}, perms, &stubHistoryService{}, reg)
			ctx := context.WithValue(context.Background(), SessionIDContextKey, "session")
			ctx = context.WithValue(ctx, MessageIDContextKey, "message")
			input, err := json.Marshal(PatchParams{PatchText: "*** Begin Patch\n*** Update File: " + source + "\n*** Move to: " + destination + "\n@@\n-old\n+new\n*** End Patch"})
			require.NoError(t, err)
			response, err := tool.Run(ctx, ToolCall{Name: PatchToolName, Input: string(input)})
			require.ElementsMatch(t, []string{source, destination}, checked)
			require.Equal(t, verdict == permission.ActionAsk, prompted)
			if verdict == permission.ActionAllow {
				require.NoError(t, err)
				require.False(t, response.IsError)
				_, err = os.Stat(source)
				require.True(t, os.IsNotExist(err))
				content, err := os.ReadFile(destination)
				require.NoError(t, err)
				require.Equal(t, "new\n", string(content))
			} else {
				require.Error(t, err)
				content, err := os.ReadFile(source)
				require.NoError(t, err)
				require.Equal(t, "old\n", string(content))
				content, err = os.ReadFile(destination)
				require.NoError(t, err)
				require.Equal(t, "existing\n", string(content))
			}
		})
	}
}

func TestPatchMoveDestinationConflict(t *testing.T) {
	input, err := json.Marshal(PatchParams{PatchText: "*** Begin Patch\n*** Update File: source.txt\n*** Move to: destination.txt\n@@\n-old\n+new\n*** End Patch"})
	require.NoError(t, err)
	patchCall := ToolCall{ID: "patch", Name: PatchToolName, Input: string(input)}
	require.ElementsMatch(t, []string{"source.txt", "destination.txt"}, ExtractPathsFromCall(patchCall))
	for _, path := range []string{"source.txt", "destination.txt", filepath.Join(config.WorkingDirectory(), "destination.txt")} {
		t.Run(path, func(t *testing.T) {
			input, err := json.Marshal(EditParams{FilePath: path, OldString: "old", NewString: "new"})
			require.NoError(t, err)
			editCall := ToolCall{ID: "edit", Name: EditToolName, Input: string(input)}
			calls := []ToolCall{patchCall, editCall}
			require.False(t, (&patchTool{}).AllowParallelism(patchCall, calls))
			require.False(t, (&editTool{}).AllowParallelism(editCall, calls))
		})
	}
	for _, tc := range []struct {
		name        string
		destination string
		parallel    bool
	}{
		{name: "shared move destination", destination: "destination.txt"},
		{name: "independent move", destination: "other-destination.txt", parallel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(PatchParams{PatchText: "*** Begin Patch\n*** Update File: other-source.txt\n*** Move to: " + tc.destination + "\n@@\n-old\n+new\n*** End Patch"})
			require.NoError(t, err)
			other := ToolCall{ID: "other", Name: PatchToolName, Input: string(input)}
			calls := []ToolCall{patchCall, other}
			require.Equal(t, tc.parallel, (&patchTool{}).AllowParallelism(patchCall, calls))
			require.Equal(t, tc.parallel, (&patchTool{}).AllowParallelism(other, calls))
		})
	}
}
