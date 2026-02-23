package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/permission"
	mock_permission "github.com/opencode-ai/opencode/internal/permission/mocks"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func init() {
	wd, _ := os.Getwd()
	config.Load(wd, false)
}

type stubRegistry struct{}

func (s *stubRegistry) Get(id string) (agentregistry.AgentInfo, bool) {
	return agentregistry.AgentInfo{}, false
}

func (s *stubRegistry) List() []agentregistry.AgentInfo {
	return nil
}

func (s *stubRegistry) ListByMode(mode config.AgentMode) []agentregistry.AgentInfo {
	return nil
}

func (s *stubRegistry) EvaluatePermission(agentID, toolName, input string) permission.Action {
	return permission.ActionAllow
}

func (s *stubRegistry) IsToolEnabled(agentID, toolName string) bool {
	return true
}

func (s *stubRegistry) GlobalPermissions() map[string]any {
	return nil
}

type stubHistoryService struct {
	lastContent string
}

func (s *stubHistoryService) Subscribe(context.Context) <-chan pubsub.Event[history.File] {
	return make(chan pubsub.Event[history.File])
}

func (s *stubHistoryService) Create(_ context.Context, _, path, content string) (history.File, error) {
	s.lastContent = content
	return history.File{Path: path, Content: content}, nil
}

func (s *stubHistoryService) CreateVersion(_ context.Context, _, path, content string) (history.File, error) {
	s.lastContent = content
	return history.File{Path: path, Content: content}, nil
}

func (s *stubHistoryService) Get(_ context.Context, _ string) (history.File, error) {
	return history.File{}, fmt.Errorf("not found")
}

func (s *stubHistoryService) GetByPathAndSession(_ context.Context, path, _ string) (history.File, error) {
	return history.File{Path: path, Content: s.lastContent}, nil
}

func (s *stubHistoryService) ListBySession(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (s *stubHistoryService) ListLatestSessionFiles(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (s *stubHistoryService) ListBySessionTree(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (s *stubHistoryService) ListLatestSessionTreeFiles(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (s *stubHistoryService) Update(_ context.Context, f history.File) (history.File, error) {
	return f, nil
}

func (s *stubHistoryService) Delete(context.Context, string) error { return nil }

func (s *stubHistoryService) DeleteSessionFiles(context.Context, string) error { return nil }

func setupEditTest(t *testing.T) (context.Context, string, BaseTool) {
	t.Helper()
	ctrl := gomock.NewController(t)

	mockPerms := mock_permission.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true).AnyTimes()

	files := &stubHistoryService{}
	tool := NewEditTool(&noopLspService{}, mockPerms, files, &stubRegistry{})

	tmpFile, err := os.CreateTemp("", "edit_test_*.txt")
	require.NoError(t, err)
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	t.Cleanup(func() {
		os.Remove(tmpPath)
		ctrl.Finish()
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-message")

	return ctx, tmpPath, tool
}

func writeAndTrack(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	recordFileRead(path)
}

func runEdit(t *testing.T, tool BaseTool, ctx context.Context, params EditParams) ToolResponse {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, ToolCall{Name: EditToolName, Input: string(paramsJSON)})
	require.NoError(t, err)
	return resp
}

func TestEditTool_Info(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPerms := mock_permission.NewMockService(ctrl)
	tool := NewEditTool(&noopLspService{}, mockPerms, &stubHistoryService{}, &stubRegistry{})
	info := tool.Info()

	assert.Equal(t, EditToolName, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.Contains(t, info.Parameters, "file_path")
	assert.Contains(t, info.Parameters, "old_string")
	assert.Contains(t, info.Parameters, "new_string")
	assert.Contains(t, info.Parameters, "replace_all")
}

func TestEditTool_Replace(t *testing.T) {
	t.Run("single match", func(t *testing.T) {
		ctx, tmpPath, tool := setupEditTest(t)
		writeAndTrack(t, tmpPath, "hello world")

		resp := runEdit(t, tool, ctx, EditParams{
			FilePath:  tmpPath,
			OldString: "world",
			NewString: "go",
		})
		assert.False(t, resp.IsError)

		content, _ := os.ReadFile(tmpPath)
		assert.Equal(t, "hello go", string(content))
	})

	t.Run("multiple matches without replaceAll fails", func(t *testing.T) {
		ctx, tmpPath, tool := setupEditTest(t)
		writeAndTrack(t, tmpPath, "foo bar foo")

		resp := runEdit(t, tool, ctx, EditParams{
			FilePath:  tmpPath,
			OldString: "foo",
			NewString: "baz",
		})
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "2 times")
		assert.Contains(t, resp.Content, "replace_all")
	})

	t.Run("replaceAll replaces all occurrences", func(t *testing.T) {
		ctx, tmpPath, tool := setupEditTest(t)
		writeAndTrack(t, tmpPath, "foo bar foo baz foo")

		resp := runEdit(t, tool, ctx, EditParams{
			FilePath:   tmpPath,
			OldString:  "foo",
			NewString:  "qux",
			ReplaceAll: true,
		})
		assert.False(t, resp.IsError)

		content, _ := os.ReadFile(tmpPath)
		assert.Equal(t, "qux bar qux baz qux", string(content))
	})

	t.Run("old_string not found", func(t *testing.T) {
		ctx, tmpPath, tool := setupEditTest(t)
		writeAndTrack(t, tmpPath, "hello world")

		resp := runEdit(t, tool, ctx, EditParams{
			FilePath:  tmpPath,
			OldString: "nonexistent",
			NewString: "something",
		})
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "not found")
	})
}

func TestEditTool_CreateFile(t *testing.T) {
	ctx, _, tool := setupEditTest(t)

	newFile := filepath.Join(os.TempDir(), fmt.Sprintf("edit_create_%d.txt", os.Getpid()))
	t.Cleanup(func() { os.Remove(newFile) })

	resp := runEdit(t, tool, ctx, EditParams{
		FilePath:  newFile,
		OldString: "",
		NewString: "new file content",
	})
	assert.False(t, resp.IsError)

	content, err := os.ReadFile(newFile)
	require.NoError(t, err)
	assert.Equal(t, "new file content", string(content))
}

func TestEditTool_FileNotRead(t *testing.T) {
	ctx, tmpPath, tool := setupEditTest(t)
	require.NoError(t, os.WriteFile(tmpPath, []byte("content"), 0o644))

	resp := runEdit(t, tool, ctx, EditParams{
		FilePath:  tmpPath,
		OldString: "content",
		NewString: "new",
	})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "must read the file")
}

// --- MultiEdit Tests ---

func setupMultiEditTest(t *testing.T) (context.Context, string, BaseTool) {
	t.Helper()
	ctrl := gomock.NewController(t)

	mockPerms := mock_permission.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true).AnyTimes()

	files := &stubHistoryService{}
	tool := NewMultiEditTool(&noopLspService{}, mockPerms, files, &stubRegistry{})

	tmpFile, err := os.CreateTemp("", "multiedit_test_*.txt")
	require.NoError(t, err)
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	t.Cleanup(func() {
		os.Remove(tmpPath)
		ctrl.Finish()
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-message")

	return ctx, tmpPath, tool
}

func runMultiEdit(t *testing.T, tool BaseTool, ctx context.Context, params MultiEditParams) ToolResponse {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, ToolCall{Name: MultiEditToolName, Input: string(paramsJSON)})
	require.NoError(t, err)
	return resp
}

func TestMultiEditTool_Info(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPerms := mock_permission.NewMockService(ctrl)
	tool := NewMultiEditTool(&noopLspService{}, mockPerms, &stubHistoryService{}, &stubRegistry{})
	info := tool.Info()

	assert.Equal(t, MultiEditToolName, info.Name)
	assert.Contains(t, info.Parameters, "file_path")
	assert.Contains(t, info.Parameters, "edits")
}

func TestMultiEditTool_SequentialEdits(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	writeAndTrack(t, tmpPath, "aaa bbb ccc")

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "aaa", NewString: "xxx"},
			{OldString: "bbb", NewString: "yyy"},
			{OldString: "ccc", NewString: "zzz"},
		},
	})
	assert.False(t, resp.IsError)

	content, _ := os.ReadFile(tmpPath)
	assert.Equal(t, "xxx yyy zzz", string(content))
}

func TestMultiEditTool_EditDependsOnPrevious(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	writeAndTrack(t, tmpPath, "foo bar")

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "foo", NewString: "baz"},
			{OldString: "baz bar", NewString: "done"},
		},
	})
	assert.False(t, resp.IsError)

	content, _ := os.ReadFile(tmpPath)
	assert.Equal(t, "done", string(content))
}

func TestMultiEditTool_AtomicFailure(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	original := "hello world"
	writeAndTrack(t, tmpPath, original)

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "hello", NewString: "hi"},
			{OldString: "nonexistent", NewString: "fail"},
		},
	})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "edit 2")

	content, _ := os.ReadFile(tmpPath)
	assert.Equal(t, original, string(content))
}

func TestMultiEditTool_ReplaceAll(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	writeAndTrack(t, tmpPath, "var x = 1;\nvar y = x + x;")

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "x", NewString: "z", ReplaceAll: true},
		},
	})
	assert.False(t, resp.IsError)

	content, _ := os.ReadFile(tmpPath)
	assert.Equal(t, "var z = 1;\nvar y = z + z;", string(content))
}

func TestMultiEditTool_EmptyEdits(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	writeAndTrack(t, tmpPath, "content")

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits:    []MultiEditItem{},
	})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "must not be empty")
}

func TestMultiEditTool_EmptyOldString(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	writeAndTrack(t, tmpPath, "content")

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "", NewString: "new"},
		},
	})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "old_string cannot be empty")
}

func TestMultiEditTool_FileNotRead(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	require.NoError(t, os.WriteFile(tmpPath, []byte("content"), 0o644))

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "content", NewString: "new"},
		},
	})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "must read the file")
}

func TestMultiEditTool_MultipleMatchesWithoutReplaceAll(t *testing.T) {
	ctx, tmpPath, tool := setupMultiEditTest(t)
	writeAndTrack(t, tmpPath, "foo bar foo")

	resp := runMultiEdit(t, tool, ctx, MultiEditParams{
		FilePath: tmpPath,
		Edits: []MultiEditItem{
			{OldString: "foo", NewString: "baz"},
		},
	})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "2 times")
	assert.Contains(t, resp.Content, "replace_all")
}
