package memory

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	IndexMaxLines  = 200
	IndexMaxBytes  = 25 * 1024
	NoteMaxBytes   = 8 * 1024
	headerMaxBytes = 4096
)

type Index struct {
	Text      string
	Truncated bool
}

// ReadIndex reads only MEMORY.md. canonical is the resolved index path whose
// permissions were checked. Check the opened file against that path as well as
// confining it beneath root, since root itself may have been replaced meanwhile.
func ReadIndex(root, relative, canonical string) (Index, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return Index{}, err
	}
	defer r.Close()
	if err := noLinks(r, relative); err != nil {
		return Index{}, err
	}
	f, err := r.OpenFile(relative, os.O_RDONLY|readFlags, 0)
	if err != nil {
		return Index{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Index{}, err
	}
	if !info.Mode().IsRegular() {
		return Index{}, fmt.Errorf("memory index is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(canonical)
	if err != nil || resolved != canonical {
		return Index{}, fmt.Errorf("memory index path changed after permission check")
	}
	authorized, err := os.Lstat(canonical)
	if err != nil || !os.SameFile(info, authorized) {
		return Index{}, fmt.Errorf("memory index file changed after permission check")
	}
	data, err := io.ReadAll(io.LimitReader(f, IndexMaxBytes+1))
	if err != nil {
		return Index{}, err
	}
	return BoundIndex(data), nil
}

func BoundIndex(data []byte) Index {
	truncated := len(data) > IndexMaxBytes
	if truncated {
		data = data[:IndexMaxBytes]
	}
	data = bytes.ToValidUTF8(data, []byte("\uFFFD"))
	if len(data) > IndexMaxBytes {
		truncated = true
		data = data[:IndexMaxBytes]
	}
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) > IndexMaxLines && len(bytes.Join(lines[IndexMaxLines:], nil)) > 0 {
		data = bytes.Join(lines[:IndexMaxLines], nil)
		truncated = true
	}
	return Index{Text: string(data), Truncated: truncated}
}

func noLinks(root *os.Root, relative string) error {
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("memory path escapes store")
	}
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory symlinks are not loaded automatically")
		}
	}
	return nil
}

// CheckFile returns authoring feedback, never content. Ordinary tools retain
// their own write permissions and semantics; this is not a storage quota.
func CheckFile(root, filename string) string {
	rel, err := filepath.Rel(root, filename)
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 4 || parts[0] != "projects" || parts[2] != "memory" || filepath.Ext(filename) != ".md" {
		return ""
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return ""
	}
	defer r.Close()
	if noLinks(r, rel) != nil {
		return ""
	}
	f, err := r.OpenFile(rel, os.O_RDONLY|readFlags, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	var issues []string
	if filepath.Base(filename) == "MEMORY.md" {
		data, err := io.ReadAll(io.LimitReader(f, IndexMaxBytes+1))
		if err != nil {
			return ""
		}
		if BoundIndex(data).Truncated {
			issues = append(issues, "index exceeds 200 lines or 25 KiB; shorten to one retrieval cue/link per topic, with updatedAt")
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "](") && !strings.Contains(line, "updatedAt:") {
				issues = append(issues, "index links need a retrieval cue and updatedAt freshness date")
				break
			}
		}
	} else {
		if info.Size() > NoteMaxBytes {
			issues = append(issues, "note exceeds 8 KiB; summarize facts or split focused topics and update MEMORY.md")
		}
		data, err := io.ReadAll(io.LimitReader(f, headerMaxBytes))
		if err != nil {
			return ""
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		var meta struct {
			Description string `yaml:"description"`
			UpdatedAt   string `yaml:"updatedAt"`
		}
		end := bytes.Index(data, []byte("\n---\n"))
		valid := bytes.HasPrefix(data, []byte("---\n")) && end >= 4 && yaml.Unmarshal(data[4:end], &meta) == nil
		if !valid || strings.TrimSpace(meta.Description) == "" {
			issues = append(issues, "add a short description in YAML frontmatter")
		}
		if _, err := time.Parse(time.RFC3339, meta.UpdatedAt); err != nil {
			issues = append(issues, "set updatedAt in frontmatter to the last factual update (RFC3339 UTC), not the last read/import")
		}
	}
	if len(issues) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: %s.", filepath.Base(filename), strings.Join(issues, "; "))
}
