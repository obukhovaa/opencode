package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	mock_permission "github.com/opencode-ai/opencode/internal/permission/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func setupDeleteTest(t *testing.T) (context.Context, BaseTool, *gomock.Controller) {
	t.Helper()
	ctrl := gomock.NewController(t)

	mockPerms := mock_permission.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true).AnyTimes()

	files := &stubHistoryService{}
	tool := NewDeleteTool(mockPerms, files)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-message")

	return ctx, tool, ctrl
}

func runDelete(t *testing.T, tool BaseTool, ctx context.Context, params DeleteParams) ToolResponse {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, ToolCall{Name: DeleteToolName, Input: string(paramsJSON)})
	require.NoError(t, err)
	return resp
}

func createTempFileInWorkingDir(t *testing.T, pattern string) string {
	t.Helper()
	workingDir := config.WorkingDirectory()
	tmpFile, err := os.CreateTemp(workingDir, pattern)
	require.NoError(t, err)
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	t.Cleanup(func() {
		os.Remove(tmpPath)
	})
	return tmpPath
}

func createTempDirInWorkingDir(t *testing.T, pattern string) string {
	t.Helper()
	workingDir := config.WorkingDirectory()
	tmpDir, err := os.MkdirTemp(workingDir, pattern)
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})
	return tmpDir
}

func TestDeleteTool_Info(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPerms := mock_permission.NewMockService(ctrl)
	tool := NewDeleteTool(mockPerms, &stubHistoryService{})
	info := tool.Info()

	assert.Equal(t, DeleteToolName, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.Contains(t, info.Parameters, "path")
	assert.Contains(t, info.Required, "path")
}

func TestDeleteTool_DeleteFile(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpPath := createTempFileInWorkingDir(t, "delete_test_*.txt")
	content := "test file content"
	require.NoError(t, os.WriteFile(tmpPath, []byte(content), 0644))

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: tmpPath,
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)
	assert.Contains(t, resp.Content, "successfully deleted")
	assert.Contains(t, resp.Content, tmpPath)

	_, err := os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(err), "File should be deleted")

	assert.NotEmpty(t, resp.Metadata)
	var metadata DeleteResponseMetadata
	err = json.Unmarshal([]byte(resp.Metadata), &metadata)
	require.NoError(t, err)
	assert.Equal(t, 1, metadata.FilesDeleted)
	assert.Greater(t, metadata.Removals, 0)
	assert.NotEmpty(t, metadata.Diff)
}

func TestDeleteTool_DeleteDirectory(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpDir := createTempDirInWorkingDir(t, "delete_test_dir_*")

	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	subDir := filepath.Join(tmpDir, "subdir")
	file3 := filepath.Join(subDir, "file3.txt")

	require.NoError(t, os.WriteFile(file1, []byte("content1"), 0644))
	require.NoError(t, os.WriteFile(file2, []byte("content2"), 0644))
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(file3, []byte("content3"), 0644))

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: tmpDir,
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)
	assert.Contains(t, resp.Content, "successfully deleted")
	assert.Contains(t, resp.Content, tmpDir)
	assert.Contains(t, resp.Content, "3 files")

	_, err := os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err), "Directory should be deleted")

	assert.NotEmpty(t, resp.Metadata)
	var metadata DeleteResponseMetadata
	err = json.Unmarshal([]byte(resp.Metadata), &metadata)
	require.NoError(t, err)
	assert.Equal(t, 3, metadata.FilesDeleted)
	assert.Greater(t, metadata.Removals, 0)
}

func TestDeleteTool_EmptyPath(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: "",
	})

	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "path is required")
}

func TestDeleteTool_NonExistentPath(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	workingDir := config.WorkingDirectory()
	nonExistentPath := filepath.Join(workingDir, "nonexistent_file_12345.txt")

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: nonExistentPath,
	})

	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "does not exist")
}

func TestDeleteTool_PathOutsideWorkingDirectory(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpFile, err := os.CreateTemp("/tmp", "outside_test_*.txt")
	require.NoError(t, err)
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	workingDir := config.WorkingDirectory()
	require.NotEmpty(t, workingDir)
	require.NotContains(t, tmpPath, workingDir, "Test file should be outside working directory")

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: tmpPath,
	})

	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "outside the working directory")

	_, err = os.Stat(tmpPath)
	assert.False(t, os.IsNotExist(err), "File should NOT be deleted")
}

func TestDeleteTool_DeleteSymlink(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpDir := createTempDirInWorkingDir(t, "delete_symlink_test_*")

	targetFile := filepath.Join(tmpDir, "target.txt")
	require.NoError(t, os.WriteFile(targetFile, []byte("target content"), 0644))

	symlinkPath := filepath.Join(tmpDir, "symlink.txt")
	require.NoError(t, os.Symlink(targetFile, symlinkPath))

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: symlinkPath,
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)
	assert.Contains(t, resp.Content, "successfully deleted")

	_, err := os.Lstat(symlinkPath)
	assert.True(t, os.IsNotExist(err), "Symlink should be deleted")

	_, err = os.Stat(targetFile)
	assert.False(t, os.IsNotExist(err), "Target file should still exist")

	content, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Equal(t, "target content", string(content))
}

func TestDeleteTool_RelativePath(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	workingDir := config.WorkingDirectory()
	tmpFile := filepath.Join(workingDir, "delete_relative_test.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("content"), 0644))
	t.Cleanup(func() {
		os.Remove(tmpFile)
	})

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: "delete_relative_test.txt",
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)

	_, err := os.Stat(tmpFile)
	assert.True(t, os.IsNotExist(err), "File should be deleted")
}

func TestDeleteTool_InvalidJSON(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	resp, err := tool.Run(ctx, ToolCall{
		Name:  DeleteToolName,
		Input: "invalid json",
	})
	require.NoError(t, err)

	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "error parsing parameters")
}

func TestDeleteTool_DirectoryWithManyFiles(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpDir := createTempDirInWorkingDir(t, "delete_many_files_test_*")

	for i := range 10 {
		filePath := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		require.NoError(t, os.WriteFile(filePath, []byte("content"), 0644))
	}

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: tmpDir,
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)
	assert.Contains(t, resp.Content, "10 files")

	_, err := os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err), "Directory should be deleted")
}

func TestDeleteTool_DirectoryWithSymlinks(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpDir := createTempDirInWorkingDir(t, "delete_symlinks_test_*")

	targetFile := filepath.Join(tmpDir, "target.txt")
	require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644))

	subDir := filepath.Join(tmpDir, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	symlinkPath := filepath.Join(subDir, "symlink.txt")
	require.NoError(t, os.Symlink(targetFile, symlinkPath))

	regularFile := filepath.Join(subDir, "regular.txt")
	require.NoError(t, os.WriteFile(regularFile, []byte("regular"), 0644))

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: tmpDir,
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)

	_, err := os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err), "Directory should be deleted")

	var metadata DeleteResponseMetadata
	err = json.Unmarshal([]byte(resp.Metadata), &metadata)
	require.NoError(t, err)
	assert.Equal(t, 2, metadata.FilesDeleted, "Should count regular files only, not symlinks")
}

func TestDeleteTool_EmptyDirectory(t *testing.T) {
	ctx, tool, ctrl := setupDeleteTest(t)
	defer ctrl.Finish()

	tmpDir := createTempDirInWorkingDir(t, "delete_empty_dir_test_*")

	resp := runDelete(t, tool, ctx, DeleteParams{
		Path: tmpDir,
	})

	assert.False(t, resp.IsError, "Expected no error, got: %s", resp.Content)
	assert.Contains(t, resp.Content, "0 files")

	_, err := os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err), "Directory should be deleted")

	var metadata DeleteResponseMetadata
	err = json.Unmarshal([]byte(resp.Metadata), &metadata)
	require.NoError(t, err)
	assert.Equal(t, 0, metadata.FilesDeleted)
}
