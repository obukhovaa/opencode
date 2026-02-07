package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetContextFromPaths(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	_, err := config.Load(tmpDir, false)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg := config.Get()
	cfg.WorkingDir = tmpDir
	cfg.ContextPaths = []string{
		"file.txt",
		"directory/",
	}
	testFiles := []string{
		"file.txt",
		"directory/file_a.txt",
		"directory/file_b.txt",
		"directory/file_c.txt",
	}

	createTestFiles(t, tmpDir, testFiles)

	context := processContextPaths(tmpDir, cfg.ContextPaths)
	assert.Contains(t, context, "file.txt: test content")
	assert.Contains(t, context, "directory/file_a.txt: test content")
	assert.Contains(t, context, "directory/file_b.txt: test content")
	assert.Contains(t, context, "directory/file_c.txt: test content")
}

func TestProcessContextPaths(t *testing.T) {
	t.Parallel()

	t.Run("single file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"a.txt"})

		result := processContextPaths(tmpDir, []string{"a.txt"})
		assert.Contains(t, result, "a.txt: test content")
	})

	t.Run("directory with trailing slash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"docs/one.txt", "docs/two.txt"})

		result := processContextPaths(tmpDir, []string{"docs/"})
		assert.Contains(t, result, "one.txt: test content")
		assert.Contains(t, result, "two.txt: test content")
	})

	t.Run("symlink to file is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"real.txt"})

		err := os.Symlink(filepath.Join(tmpDir, "real.txt"), filepath.Join(tmpDir, "link.txt"))
		require.NoError(t, err)

		result := processContextPaths(tmpDir, []string{"real.txt", "link.txt"})
		count := countOccurrences(result, "real.txt: test content")
		assert.Equal(t, 1, count, "symlinked file should only appear once")
	})

	t.Run("symlink to directory is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"realdir/file.txt"})

		err := os.Symlink(filepath.Join(tmpDir, "realdir"), filepath.Join(tmpDir, "linkdir"))
		require.NoError(t, err)

		result := processContextPaths(tmpDir, []string{"realdir/", "linkdir/"})
		count := countOccurrences(result, "file.txt: test content")
		assert.Equal(t, 1, count, "file in symlinked directory should only appear once")
	})

	t.Run("same file listed twice is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"dup.txt"})

		result := processContextPaths(tmpDir, []string{"dup.txt", "dup.txt"})
		count := countOccurrences(result, "dup.txt: test content")
		assert.Equal(t, 1, count, "duplicate path should only appear once")
	})

	t.Run("file in directory and explicit path is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"ctx/notes.txt"})

		result := processContextPaths(tmpDir, []string{"ctx/", "ctx/notes.txt"})
		count := countOccurrences(result, "notes.txt: test content")
		assert.Equal(t, 1, count, "file listed both via directory and explicit path should only appear once")
	})

	t.Run("nonexistent path produces no output", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		result := processContextPaths(tmpDir, []string{"does-not-exist.txt"})
		assert.Empty(t, result)
	})

	t.Run("empty paths produces no output", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		result := processContextPaths(tmpDir, []string{})
		assert.Empty(t, result)
	})

	t.Run("symlink in walked directory is deduplicated with explicit path", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"source.txt"})

		err := os.MkdirAll(filepath.Join(tmpDir, "dir"), 0755)
		require.NoError(t, err)
		err = os.Symlink(filepath.Join(tmpDir, "source.txt"), filepath.Join(tmpDir, "dir", "link.txt"))
		require.NoError(t, err)

		result := processContextPaths(tmpDir, []string{"source.txt", "dir/"})
		count := countOccurrences(result, "source.txt: test content")
		assert.Equal(t, 1, count, "symlink inside directory should be deduplicated against explicit path")
	})
}

func countOccurrences(s, substr string) int {
	count := 0
	idx := 0
	for {
		i := indexAt(s, substr, idx)
		if i == -1 {
			break
		}
		count++
		idx = i + len(substr)
	}
	return count
}

func indexAt(s, substr string, start int) int {
	if start >= len(s) {
		return -1
	}
	i := indexOf(s[start:], substr)
	if i == -1 {
		return -1
	}
	return start + i
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func createTestFiles(t *testing.T, tmpDir string, testFiles []string) {
	t.Helper()
	for _, path := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		if path[len(path)-1] == '/' {
			err := os.MkdirAll(fullPath, 0755)
			require.NoError(t, err)
		} else {
			dir := filepath.Dir(fullPath)
			err := os.MkdirAll(dir, 0755)
			require.NoError(t, err)
			err = os.WriteFile(fullPath, []byte(path+": test content"), 0644)
			require.NoError(t, err)
		}
	}
}
