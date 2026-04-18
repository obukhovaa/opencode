package fileutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/opencode-ai/opencode/internal/logging"
)

const defaultFileOpTimeout = 3 * time.Minute

var fileOpTimeout = resolveFileOpTimeout()

func resolveFileOpTimeout() time.Duration {
	if envVal := os.Getenv("OPENCODE_FILE_OP_TIMEOUT"); envVal != "" {
		if secs, err := strconv.Atoi(envVal); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		logging.Warn("Invalid OPENCODE_FILE_OP_TIMEOUT value, using default",
			"value", envVal, "default", defaultFileOpTimeout)
	}
	return defaultFileOpTimeout
}

func FileOpTimeout() time.Duration {
	return fileOpTimeout
}

var (
	rgPath  string
	fzfPath string
)

func init() {
	var err error
	rgPath, err = exec.LookPath("rg")
	if err != nil {
		logging.Warn("Ripgrep (rg) not found in $PATH. Some features might be limited or slower.")
		rgPath = ""
	}
	fzfPath, err = exec.LookPath("fzf")
	if err != nil {
		logging.Warn("FZF not found in $PATH. Some features might be limited or slower.")
		fzfPath = ""
	}
}

func GetRgCmd(ctx context.Context, globPattern string) *exec.Cmd {
	if rgPath == "" {
		return nil
	}
	rgArgs := []string{
		"--files",
		"-L",
		"--null",
	}
	if globPattern != "" {
		if !filepath.IsAbs(globPattern) && !strings.HasPrefix(globPattern, "/") {
			globPattern = "/" + globPattern
		}
		rgArgs = append(rgArgs, "--glob", globPattern)
	}
	cmd := exec.CommandContext(ctx, rgPath, rgArgs...)
	cmd.Dir = "."
	return cmd
}

func GetFzfCmd(query string) *exec.Cmd {
	if fzfPath == "" {
		return nil
	}
	fzfArgs := []string{
		"--filter",
		query,
		"--read0",
		"--print0",
	}
	cmd := exec.Command(fzfPath, fzfArgs...)
	cmd.Dir = "."
	return cmd
}

type FileInfo struct {
	Path    string
	ModTime time.Time
}

func SkipHidden(path string) bool {
	// Check for hidden files (starting with a dot)
	base := filepath.Base(path)
	if base != "." && strings.HasPrefix(base, ".") {
		return true
	}

	commonIgnoredDirs := map[string]bool{
		".opencode":        true,
		"node_modules":     true,
		"vendor":           true,
		"dist":             true,
		"build":            true,
		"target":           true,
		".git":             true,
		".idea":            true,
		".vscode":          true,
		"__pycache__":      true,
		"bin":              true,
		"obj":              true,
		"out":              true,
		"coverage":         true,
		"tmp":              true,
		"temp":             true,
		"logs":             true,
		"generated":        true,
		"bower_components": true,
		"jspm_packages":    true,
	}

	parts := strings.SplitSeq(path, string(os.PathSeparator))
	for part := range parts {
		if commonIgnoredDirs[part] {
			return true
		}
	}
	return false
}

// ctxFS wraps an fs.FS to check context cancellation on every Open call.
// This prevents walks from blocking indefinitely on problematic paths
// (e.g. /proc entries where stat() can hang on processes in D-state).
type ctxFS struct {
	inner fs.FS
	ctx   context.Context
}

func (c *ctxFS) Open(name string) (fs.File, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return c.inner.Open(name)
}

func (c *ctxFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if rd, ok := c.inner.(fs.ReadDirFS); ok {
		return rd.ReadDir(name)
	}
	return fs.ReadDir(c.inner, name)
}

func GlobWithDoublestar(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	type result struct {
		matches []FileInfo
		err     error
	}
	ch := make(chan result, 1)

	go func() {
		fsys := &ctxFS{inner: os.DirFS(searchPath), ctx: ctx}
		relPattern := strings.TrimPrefix(pattern, "/")
		var matches []FileInfo

		err := doublestar.GlobWalk(fsys, relPattern, func(path string, d fs.DirEntry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if SkipHidden(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			absPath := path
			if !strings.HasPrefix(absPath, searchPath) && searchPath != "." {
				absPath = filepath.Join(searchPath, absPath)
			} else if !strings.HasPrefix(absPath, "/") && searchPath == "." {
				absPath = filepath.Join(searchPath, absPath)
			}

			matches = append(matches, FileInfo{Path: absPath, ModTime: info.ModTime()})
			if limit > 0 && len(matches) >= limit*2 {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		ch <- result{matches, err}
	}()

	select {
	case <-ctx.Done():
		return nil, false, fmt.Errorf("glob walk timed out: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, false, fmt.Errorf("glob walk error: %w", r.err)
		}
		matches := r.matches

		sort.Slice(matches, func(i, j int) bool {
			return matches[i].ModTime.After(matches[j].ModTime)
		})

		truncated := false
		if limit > 0 && len(matches) > limit {
			matches = matches[:limit]
			truncated = true
		}

		results := make([]string, len(matches))
		for i, m := range matches {
			results[i] = m.Path
		}
		return results, truncated, nil
	}
}
