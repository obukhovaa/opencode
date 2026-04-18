package fileutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGlobWithDoublestar_ContextCancellation(t *testing.T) {
	// Create a temp dir with some files
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("returns results with valid context", func(t *testing.T) {
		ctx := context.Background()
		files, _, err := GlobWithDoublestar(ctx, "**/*.go", dir, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 2 {
			t.Errorf("expected 2 files, got %d", len(files))
		}
	})

	t.Run("returns error when context already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := GlobWithDoublestar(ctx, "**/*.go", dir, 100)
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})

	t.Run("returns error when context times out", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()
		time.Sleep(5 * time.Millisecond) // ensure timeout fires
		_, _, err := GlobWithDoublestar(ctx, "**/*.go", dir, 100)
		if err == nil {
			t.Fatal("expected error for timed out context")
		}
	})
}

func TestCtxFS_CancelledContextReturnsError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fs := &ctxFS{inner: os.DirFS(dir), ctx: ctx}

	_, err := fs.Open("test.txt")
	if err == nil {
		t.Error("expected error from Open on cancelled context")
	}

	_, err = fs.ReadDir(".")
	if err == nil {
		t.Error("expected error from ReadDir on cancelled context")
	}
}

func TestCtxFS_ValidContextWorks(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)

	ctx := context.Background()
	fs := &ctxFS{inner: os.DirFS(dir), ctx: ctx}

	f, err := fs.Open("test.txt")
	if err != nil {
		t.Fatalf("unexpected error from Open: %v", err)
	}
	f.Close()

	entries, err := fs.ReadDir(".")
	if err != nil {
		t.Fatalf("unexpected error from ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestFileOpTimeout_DefaultValue(t *testing.T) {
	timeout := FileOpTimeout()
	if timeout != defaultFileOpTimeout {
		t.Errorf("expected default timeout %v, got %v", defaultFileOpTimeout, timeout)
	}
}
