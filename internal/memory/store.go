package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/fileutil"
)

const MaxFileBytes = 5 * 1024 * 1024
const MaxViewChars = 16000

type Params struct {
	Command    string  `json:"command"`
	Path       string  `json:"path,omitempty"`
	ViewRange  []int   `json:"view_range,omitempty"`
	FileText   *string `json:"file_text,omitempty"`
	OldStr     *string `json:"old_str,omitempty"`
	NewStr     string  `json:"new_str,omitempty"`
	InsertLine *int    `json:"insert_line,omitempty"`
	InsertText *string `json:"insert_text,omitempty"`
	OldPath    string  `json:"old_path,omitempty"`
	NewPath    string  `json:"new_path,omitempty"`
}

type Result struct {
	Text  string
	Image []byte
	MIME  string
}

func Relative(virtual string) (string, error) {
	if virtual == "/memories" || virtual == "/memories/" {
		return ".", nil
	}
	if !strings.HasPrefix(virtual, "/memories/") || strings.ContainsAny(virtual, "\\\x00") {
		return "", fmt.Errorf("path must be under /memories")
	}
	rel := strings.TrimSuffix(strings.TrimPrefix(virtual, "/memories/"), "/")
	for _, part := range strings.Split(rel, "/") {
		if part == "" || strings.HasPrefix(part, ".") || part == "node_modules" {
			return "", fmt.Errorf("hidden, empty and traversal path components are not allowed")
		}
	}
	return rel, nil
}

func Validate(p Params) ([]string, error) {
	paths := []string{p.Path}
	switch p.Command {
	case "view":
		if len(p.ViewRange) != 0 && (len(p.ViewRange) != 2 || p.ViewRange[0] < 1 || (p.ViewRange[1] != -1 && p.ViewRange[1] < p.ViewRange[0])) {
			return nil, fmt.Errorf("view_range must be [start, end], 1-indexed, with -1 for EOF")
		}
	case "create":
		if p.FileText == nil || len(*p.FileText) > MaxFileBytes {
			return nil, fmt.Errorf("file_text is required and must be at most %d bytes", MaxFileBytes)
		}
	case "str_replace":
		if p.OldStr == nil || *p.OldStr == "" {
			return nil, fmt.Errorf("nonempty old_str is required")
		}
	case "insert":
		if p.InsertLine == nil || *p.InsertLine < 0 || p.InsertText == nil {
			return nil, fmt.Errorf("insert_line and insert_text are required")
		}
	case "delete":
	case "rename":
		paths = []string{p.OldPath, p.NewPath}
	default:
		return nil, fmt.Errorf("unknown memory command %q", p.Command)
	}
	for _, virtual := range paths {
		rel, err := Relative(virtual)
		if err != nil {
			return nil, err
		}
		if p.Command != "view" && (rel == "." || rel == "store.json" || rel == "aliases.json") {
			return nil, fmt.Errorf("cannot modify the memory root or store configuration")
		}
	}
	return paths, nil
}

func checkPath(root *os.Root, rel string) error {
	current := ""
	for _, part := range strings.Split(rel, "/") {
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in memory paths")
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("only regular files and directories are supported")
		}
	}
	return nil
}

func readFile(root *os.Root, rel string) ([]byte, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|readFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return nil, fmt.Errorf("memory file must be regular and at most %d bytes", MaxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("memory file exceeds size limit")
	}
	return data, err
}

func writeFile(root *os.Root, rel string, data []byte) error {
	if len(data) > MaxFileBytes {
		return fmt.Errorf("memory file exceeds size limit")
	}
	if err := root.MkdirAll(path.Dir(rel), 0o700); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(rel), ".memory-"+hex.EncodeToString(random[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(tmp, rel)
}

func Run(ctx context.Context, directory string, p Params) (Result, error) {
	paths, err := Validate(p)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Result{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Result{}, err
	}
	defer root.Close()
	if err := checkPath(root, ".sync/lock"); err != nil {
		return Result{}, err
	}
	if err := root.MkdirAll(".sync", 0o700); err != nil {
		return Result{}, err
	}
	lock, err := fileutil.Acquire(filepath.Join(directory, ".sync", "lock"))
	if err != nil {
		return Result{}, fmt.Errorf("memory store busy or unavailable; retry: %w", err)
	}
	defer lock.Release()
	if data, err := readFile(root, "store.json"); os.IsNotExist(err) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return Result{}, err
		}
		manifest := fmt.Sprintf("{\"version\":1,\"store_id\":%q}\n", hex.EncodeToString(id[:]))
		if err := writeFile(root, "store.json", []byte(manifest)); err != nil {
			return Result{}, err
		}
	} else if err != nil {
		return Result{}, err
	} else {
		var manifest struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 {
			return Result{}, fmt.Errorf("unsupported memory store version")
		}
	}
	for _, virtual := range paths {
		rel, _ := Relative(virtual)
		if err := checkPath(root, rel); err != nil {
			return Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	rel, _ := Relative(paths[0])
	switch p.Command {
	case "view":
		return view(root, rel, paths[0], p.ViewRange)
	case "create":
		if _, err := root.Lstat(rel); !os.IsNotExist(err) {
			if err != nil {
				return Result{}, err
			}
			return Result{}, fmt.Errorf("File %s already exists", p.Path)
		}
		if err := writeFile(root, rel, []byte(*p.FileText)); err != nil {
			return Result{}, err
		}
		return Result{Text: "File created successfully at: " + p.Path}, nil
	case "delete":
		if _, err := root.Lstat(rel); err != nil {
			return Result{}, err
		}
		if err := root.RemoveAll(rel); err != nil {
			return Result{}, err
		}
		return Result{Text: "Successfully deleted " + p.Path}, nil
	case "rename":
		to, _ := Relative(p.NewPath)
		if _, err := root.Lstat(to); !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("destination exists or is unavailable")
		}
		if err := root.MkdirAll(path.Dir(to), 0o700); err != nil {
			return Result{}, err
		}
		if err := root.Rename(rel, to); err != nil {
			return Result{}, err
		}
		return Result{Text: "Successfully renamed " + p.OldPath + " to " + p.NewPath}, nil
	}
	data, err := readFile(root, rel)
	if err != nil {
		return Result{}, err
	}
	if !utf8.Valid(data) {
		return Result{}, fmt.Errorf("memory text must be UTF-8")
	}
	content := string(data)
	if p.Command == "str_replace" {
		if count := strings.Count(content, *p.OldStr); count != 1 {
			return Result{}, fmt.Errorf("No replacement was performed: old_str occurs %d times; ensure it is unique", count)
		}
		content = strings.Replace(content, *p.OldStr, p.NewStr, 1)
	} else {
		lines := strings.SplitAfter(content, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if *p.InsertLine > len(lines) {
			return Result{}, fmt.Errorf("insert_line must be in [0, %d]", len(lines))
		}
		prefix := strings.Join(lines[:*p.InsertLine], "")
		if prefix != "" && !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		suffix := strings.Join(lines[*p.InsertLine:], "")
		inserted := *p.InsertText
		if suffix != "" && !strings.HasSuffix(inserted, "\n") {
			inserted += "\n"
		}
		content = prefix + inserted + suffix
	}
	if err := writeFile(root, rel, []byte(content)); err != nil {
		return Result{}, err
	}
	return Result{Text: "The memory file has been edited."}, nil
}

func view(root *os.Root, rel, virtual string, viewRange []int) (Result, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		var lines []string
		truncated := false
		err := fs.WalkDir(root.FS(), rel, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if name != rel && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules") {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			suffix := strings.TrimPrefix(strings.TrimPrefix(name, rel), "/")
			if rel == "." {
				suffix = name
			}
			if name != rel && strings.Count(suffix, "/") >= 2 {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if len(lines) >= 200 {
				truncated = true
				return fs.SkipAll
			}
			stat, err := entry.Info()
			if err != nil {
				return err
			}
			if !stat.IsDir() && !stat.Mode().IsRegular() {
				return nil
			}
			label := "/memories"
			if name != "." {
				label += "/" + name
			}
			lines = append(lines, fmt.Sprintf("%dB\t%s", stat.Size(), label))
			return nil
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return Result{}, err
		}
		sort.Strings(lines)
		text := fmt.Sprintf("Here are files and directories up to 2 levels deep in %s, excluding hidden items and node_modules:\n%s", virtual, strings.Join(lines, "\n"))
		if truncated {
			text += "\nListing truncated; view a subdirectory."
		}
		return Result{Text: text}, nil
	}
	data, err := readFile(root, rel)
	if err != nil {
		return Result{}, err
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".png":
		return Result{Image: data, MIME: "image/png"}, nil
	case ".jpg", ".jpeg":
		return Result{Image: data, MIME: "image/jpeg"}, nil
	}
	if !utf8.Valid(data) {
		return Result{}, fmt.Errorf("memory text must be UTF-8")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) > 999999 {
		return Result{}, fmt.Errorf("file exceeds maximum line limit of 999,999 lines")
	}
	start, end := 1, len(lines)
	if len(viewRange) == 2 {
		start = viewRange[0]
		if viewRange[1] != -1 {
			end = min(viewRange[1], len(lines))
		}
	}
	if start > len(lines) {
		return Result{}, fmt.Errorf("view_range starts beyond EOF")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Here's the content of %s with line numbers:\n", virtual)
	count := 0
	for i := start - 1; i < end; i++ {
		line := fmt.Sprintf("%6d\t%s\n", i+1, lines[i])
		runes := []rune(line)
		if count+len(runes) > MaxViewChars {
			out.WriteString(string(runes[:MaxViewChars-count]))
			out.WriteString("\nView truncated; use view_range to read further lines.")
			break
		}
		out.WriteString(line)
		count += len(runes)
	}
	return Result{Text: out.String()}, nil
}
