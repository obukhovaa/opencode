package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/permission"
)

type GrepParams struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Include         string `json:"include"`
	Glob            string `json:"glob"`
	LiteralText     bool   `json:"literal_text"`
	OutputMode      string `json:"output_mode"`
	Context         *int   `json:"context"`
	BeforeContext   *int   `json:"before_context"`
	AfterContext    *int   `json:"after_context"`
	CaseInsensitive bool   `json:"case_insensitive"`
	FileType        string `json:"file_type"`
	HeadLimit       *int   `json:"head_limit"`
	Offset          int    `json:"offset"`
	Multiline       bool   `json:"multiline"`
}

// resolveGlob returns the effective glob pattern, preferring the new "glob"
// parameter but falling back to the legacy "include" alias.
func (p *GrepParams) resolveGlob() string {
	if p.Glob != "" {
		return p.Glob
	}
	return p.Include
}

// resolveHeadLimit returns the effective head limit, defaulting to 250.
func (p *GrepParams) resolveHeadLimit() int {
	if p.HeadLimit != nil {
		return *p.HeadLimit
	}
	return 250
}

// resolveOutputMode returns the effective output mode, defaulting to "files_with_matches".
func (p *GrepParams) resolveOutputMode() string {
	switch p.OutputMode {
	case "content", "files_with_matches", "count":
		return p.OutputMode
	default:
		return "files_with_matches"
	}
}

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	lineText string
}

type GrepResponseMetadata struct {
	Mode            string `json:"mode"`
	NumberOfFiles   int    `json:"number_of_files"`
	NumberOfMatches int    `json:"number_of_matches"`
	Truncated       bool   `json:"truncated"`
	Offset          int    `json:"offset,omitempty"`
	Limit           int    `json:"limit"`
}

type grepTool struct {
	registry    agentregistry.Registry
	permissions permission.Service
}

const (
	GrepToolName    = "grep"
	grepDescription = `A powerful search tool built on ripgrep

Usage:
- ALWAYS use grep for search tasks. NEVER invoke ` + "`grep`" + ` or ` + "`rg`" + ` as a bash command. The grep tool has been optimized for correct permissions and access.
- Supports full regex syntax (e.g., "log.*Error", "function\\s+\\w+")
- Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or file_type parameter (e.g., "js", "py", "go")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
- Use Task tool for open-ended searches requiring multiple rounds
- Pattern syntax: Uses ripgrep (not grep) — literal braces need escaping (use ` + "`interface\\{\\}`" + ` to find ` + "`interface{}`" + ` in Go code)
- Multiline matching: By default patterns match within single lines only. For cross-line patterns like ` + "`struct \\{[\\s\\S]*?field`" + `, use multiline=true
`
)

func NewGrepTool(reg agentregistry.Registry, permissions permission.Service) BaseTool {
	return &grepTool{registry: reg, permissions: permissions}
}

func (g *grepTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (g *grepTool) IsBaseline() bool { return true }

func (g *grepTool) Info() ToolInfo {
	return ToolInfo{
		Name:        GrepToolName,
		Description: grepDescription,
		Parameters: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The regex pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to the current working directory.",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Glob pattern to filter files (e.g. \"*.js\", \"*.{ts,tsx}\") — maps to rg --glob",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "Deprecated alias for glob. Use glob instead.",
			},
			"literal_text": map[string]any{
				"type":        "boolean",
				"description": "If true, the pattern will be treated as literal text with special regex characters escaped. Default is false.",
			},
			"output_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"content", "files_with_matches", "count"},
				"description": "Output mode: \"content\" shows matching lines (supports context flags, head_limit), \"files_with_matches\" shows file paths (supports head_limit), \"count\" shows match counts (supports head_limit). Defaults to \"files_with_matches\".",
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Number of lines to show before and after each match (rg -C). Requires output_mode: \"content\", ignored otherwise.",
			},
			"before_context": map[string]any{
				"type":        "integer",
				"description": "Number of lines to show before each match (rg -B). Requires output_mode: \"content\", ignored otherwise.",
			},
			"after_context": map[string]any{
				"type":        "integer",
				"description": "Number of lines to show after each match (rg -A). Requires output_mode: \"content\", ignored otherwise.",
			},
			"case_insensitive": map[string]any{
				"type":        "boolean",
				"description": "Case insensitive search (rg -i). Default is false.",
			},
			"file_type": map[string]any{
				"type":        "string",
				"description": "File type to search (rg --type). Common types: js, py, go, java, rust, etc. More efficient than glob for standard file types.",
			},
			"head_limit": map[string]any{
				"type":        "integer",
				"description": "Limit output to first N entries. Defaults to 250. Pass 0 for unlimited (use sparingly).",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Skip first N entries before applying head_limit. Defaults to 0.",
			},
			"multiline": map[string]any{
				"type":        "boolean",
				"description": "Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false.",
			},
		},
		Required: []string{"pattern"},
	}
}

// escapeRegexPattern escapes special regex characters so they're treated as literal characters
func escapeRegexPattern(pattern string) string {
	specialChars := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	escaped := pattern

	for _, char := range specialChars {
		escaped = strings.ReplaceAll(escaped, char, "\\"+char)
	}

	return escaped
}

func (g *grepTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GrepParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}

	searchPattern := params.Pattern
	if params.LiteralText {
		searchPattern = escapeRegexPattern(params.Pattern)
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = config.WorkingDirectory()
	}

	if err := checkReadPermission(ctx, g.registry, g.permissions, GrepToolName, searchPath); err != nil {
		if err == permission.ErrorPermissionDenied {
			return NewTextErrorResponse(fmt.Sprintf("Permission denied: searching %s", searchPath)), nil
		}
		return NewEmptyResponse(), err
	}

	mode := params.resolveOutputMode()
	headLimit := params.resolveHeadLimit()
	glob := params.resolveGlob()

	// Extract read deny patterns from the permission system and convert to
	// ripgrep --glob exclusions, mirroring Claude Code's getFileReadIgnorePatterns.
	var denyPatterns []string
	if g.registry != nil {
		agentID := string(GetAgentID(ctx))
		denyPatterns = g.registry.ReadDenyPatterns(agentID, GrepToolName)
	}

	output, metadata, err := runGrepSearch(ctx, searchPattern, searchPath, &params, glob, mode, headLimit, denyPatterns)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("error searching files: %w", err)
	}

	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

func runGrepSearch(ctx context.Context, pattern, rootPath string, params *GrepParams, glob, mode string, headLimit int, denyPatterns []string) (string, GrepResponseMetadata, error) {
	timeout := fileutil.FileOpTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Try ripgrep first; fall back to Go regex search
	rgAvailable := isRipgrepAvailable()

	if rgAvailable {
		return searchWithRipgrepModes(ctx, pattern, rootPath, params, glob, mode, headLimit, denyPatterns)
	}

	// Fallback: Go regex search only supports basic files_with_matches-like output.
	// Pass headLimit so the fallback collection cap aligns with the requested limit.
	return searchWithRegexFallback(ctx, pattern, rootPath, glob, mode, headLimit, params.Offset, denyPatterns)
}

var (
	ripgrepOnce      sync.Once
	ripgrepAvailable bool
)

func isRipgrepAvailable() bool {
	ripgrepOnce.Do(func() {
		_, err := exec.LookPath("rg")
		ripgrepAvailable = err == nil
	})
	return ripgrepAvailable
}

func searchWithRipgrepModes(ctx context.Context, pattern, path string, params *GrepParams, glob, mode string, headLimit int, denyPatterns []string) (string, GrepResponseMetadata, error) {
	args := buildRipgrepArgs(pattern, params, glob, mode, denyPatterns)
	args = append(args, path)

	cmd := exec.CommandContext(ctx, "rg", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			switch exitErr.ExitCode() {
			case 1:
				// No matches
				return "No matches found", GrepResponseMetadata{Mode: mode, Limit: headLimit}, nil
			case 2:
				// Partial results — continue processing
			default:
				return "", GrepResponseMetadata{}, err
			}
		} else {
			return "", GrepResponseMetadata{}, err
		}
	}

	raw := strings.TrimSuffix(string(output), "\n")
	if raw == "" {
		return "No matches found", GrepResponseMetadata{Mode: mode, Limit: headLimit}, nil
	}

	rootPath := path
	switch mode {
	case "content":
		return formatContentMode(raw, rootPath, params.Offset, headLimit)
	case "count":
		return formatCountMode(raw, rootPath, params.Offset, headLimit)
	default:
		return formatFilesMode(raw, rootPath, params.Offset, headLimit)
	}
}

// commonIgnoredDirs lists directories that should be skipped during search.
// VCS dirs are always excluded; heavy dependency/build dirs are excluded because
// ripgrep only respects .gitignore inside git repos — outside git (or if the
// entry is missing from .gitignore) these would be walked in full.
var commonIgnoredDirs = []string{
	// VCS
	".git", ".svn", ".hg",
	// Dependencies
	"node_modules", "bower_components", "jspm_packages",
	// Build output / caches
	"__pycache__", ".opencode",
}

func buildRipgrepArgs(pattern string, params *GrepParams, glob, mode string, denyPatterns []string) []string {
	args := []string{
		"--hidden",
		"--max-columns", "500",
		"--max-columns-preview",
		"--no-messages",
	}

	for _, dir := range commonIgnoredDirs {
		args = append(args, "--glob", "!"+dir)
	}

	// Apply read deny patterns from the permission system as --glob exclusions.
	// Patterns may be absolute paths (e.g., "/etc/secrets/*") or relative globs
	// (e.g., "*.env", ".env.*"). Non-absolute patterns get a **/ prefix so
	// ripgrep matches them at any depth.
	for _, dp := range denyPatterns {
		if strings.HasPrefix(dp, "/") {
			args = append(args, "--glob", "!"+dp)
		} else {
			args = append(args, "--glob", "!**/"+dp)
		}
	}

	switch mode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	case "content":
		args = append(args, "-H", "-n", "--field-match-separator=\x1f")
		if params.Context != nil && *params.Context > 0 {
			args = append(args, "-C", strconv.Itoa(*params.Context))
		}
		if params.BeforeContext != nil && *params.BeforeContext > 0 {
			args = append(args, "-B", strconv.Itoa(*params.BeforeContext))
		}
		if params.AfterContext != nil && *params.AfterContext > 0 {
			args = append(args, "-A", strconv.Itoa(*params.AfterContext))
		}
	}

	if params.CaseInsensitive {
		args = append(args, "-i")
	}

	if params.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}

	if params.FileType != "" {
		args = append(args, "--type", params.FileType)
	}

	if glob != "" {
		args = append(args, "--glob", glob)
	}

	// Use -e when pattern starts with dash to avoid flag ambiguity
	if strings.HasPrefix(pattern, "-") {
		args = append(args, "-e", pattern)
	} else {
		args = append(args, pattern)
	}

	return args
}

// paginationResult holds the output of paginating a slice.
type paginationResult struct {
	start     int
	end       int
	total     int
	truncated bool
}

// paginate applies offset and head_limit to a total count, returning
// the slice bounds and whether the result was truncated.
func paginate(total, offset, headLimit int) paginationResult {
	start := min(offset, total)
	remaining := total - start

	end := total
	truncated := false
	if headLimit > 0 && remaining > headLimit {
		end = start + headLimit
		truncated = true
	}

	return paginationResult{
		start:     start,
		end:       end,
		total:     total,
		truncated: truncated,
	}
}

// formatFilesMode processes rg -l output: one file path per line.
// Sorts by mtime (newest first), applies offset/limit pagination.
func formatFilesMode(raw, rootPath string, offset, headLimit int) (string, GrepResponseMetadata, error) {
	lines := strings.Split(raw, "\n")

	type fileEntry struct {
		path    string
		modTime time.Time
	}
	entries := make([]fileEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		info, err := os.Stat(line)
		if err != nil {
			entries = append(entries, fileEntry{path: line})
			continue
		}
		entries = append(entries, fileEntry{path: line, modTime: info.ModTime()})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.After(entries[j].modTime)
	})

	pg := paginate(len(entries), offset, headLimit)
	page := entries[pg.start:pg.end]

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d files", pg.total)
	if pg.truncated {
		fmt.Fprintf(&sb, " (showing %d, limit: %d)", len(page), headLimit)
	}
	sb.WriteByte('\n')

	for _, e := range page {
		sb.WriteString(toRelativePath(e.path, rootPath))
		sb.WriteByte('\n')
	}

	return sb.String(), GrepResponseMetadata{
		Mode:          "files_with_matches",
		NumberOfFiles: pg.total,
		Truncated:     pg.truncated,
		Offset:        offset,
		Limit:         headLimit,
	}, nil
}

// formatContentMode processes rg -H -n output (with optional context).
// Converts absolute paths to relative, applies offset/limit pagination on output lines.
func formatContentMode(raw, rootPath string, offset, headLimit int) (string, GrepResponseMetadata, error) {
	lines := strings.Split(raw, "\n")

	// Count match lines and unique files using the unit separator (\x1f).
	// buildRipgrepArgs uses --field-match-separator=\x1f for content mode, so
	// match lines contain \x1f (e.g. "file\x1fline\x1fcontent") while context
	// lines use dashes ("file-line-content") and separators are "--".
	matchCount := 0
	fileSet := make(map[string]struct{})
	for i, line := range lines {
		if strings.ContainsRune(line, '\x1f') {
			matchCount++
			if idx := strings.IndexByte(line, '\x1f'); idx > 0 {
				fileSet[line[:idx]] = struct{}{}
			}
			lines[i] = strings.ReplaceAll(line, "\x1f", ":")
		}
	}

	// Convert absolute paths to relative in each line
	for i, line := range lines {
		lines[i] = relativizeLine(line, rootPath)
	}

	pg := paginate(len(lines), offset, headLimit)
	page := lines[pg.start:pg.end]

	var sb strings.Builder
	for _, line := range page {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	if pg.truncated || offset > 0 {
		fmt.Fprintf(&sb, "[Showing lines %d-%d of %d total]\n", pg.start+1, pg.end, pg.total)
	}

	return sb.String(), GrepResponseMetadata{
		Mode:            "content",
		NumberOfFiles:   len(fileSet),
		NumberOfMatches: matchCount,
		Truncated:       pg.truncated,
		Offset:          offset,
		Limit:           headLimit,
	}, nil
}

// formatCountMode processes rg -c output (file:count per line).
// Sums total matches, applies offset/limit pagination on file entries.
func formatCountMode(raw, rootPath string, offset, headLimit int) (string, GrepResponseMetadata, error) {
	lines := strings.Split(raw, "\n")

	type countEntry struct {
		path  string
		count int
	}
	entries := make([]countEntry, 0, len(lines))
	totalMatches := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: file:count (last colon separates)
		idx := strings.LastIndex(line, ":")
		if idx < 0 {
			continue
		}
		filePath := line[:idx]
		count, err := strconv.Atoi(line[idx+1:])
		if err != nil {
			continue
		}
		totalMatches += count
		entries = append(entries, countEntry{
			path:  filePath,
			count: count,
		})
	}

	pg := paginate(len(entries), offset, headLimit)
	page := entries[pg.start:pg.end]

	var sb strings.Builder
	for _, e := range page {
		fmt.Fprintf(&sb, "%s:%d\n", toRelativePath(e.path, rootPath), e.count)
	}
	fmt.Fprintf(&sb, "Found %d total matches across %d files.\n", totalMatches, pg.total)

	return sb.String(), GrepResponseMetadata{
		Mode:            "count",
		NumberOfFiles:   pg.total,
		NumberOfMatches: totalMatches,
		Truncated:       pg.truncated,
		Offset:          offset,
		Limit:           headLimit,
	}, nil
}

// toRelativePath converts an absolute path to relative if it's under rootPath.
func toRelativePath(absPath, rootPath string) string {
	rel, err := filepath.Rel(rootPath, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

// relativizeLine converts the leading file path in a ripgrep content line to relative.
// Lines may be "file:line:content", "file-line-content" (context), or "--" (separator).
func relativizeLine(line, rootPath string) string {
	if line == "--" {
		return line
	}
	if strings.HasPrefix(line, rootPath+"/") {
		return line[len(rootPath)+1:]
	}
	return line
}

// searchWithRegexFallback is the Go regex fallback for environments without ripgrep.
func searchWithRegexFallback(ctx context.Context, pattern, rootPath, glob, mode string, headLimit, offset int, denyPatterns []string) (string, GrepResponseMetadata, error) {
	// Collect more than headLimit so pagination can report accurate totals,
	// but still cap to avoid unbounded memory use.
	collectLimit := max(headLimit+offset, 500)
	matches, capped, err := searchFilesWithRegex(ctx, pattern, rootPath, glob, collectLimit)
	if err != nil {
		return "", GrepResponseMetadata{}, err
	}

	// Filter out files matching read deny patterns.
	// Absolute patterns match the full path; relative patterns match against
	// the base name and relative path (mirroring ripgrep's !**/pattern behavior).
	if len(denyPatterns) > 0 {
		filtered := matches[:0]
		for _, m := range matches {
			denied := false
			for _, dp := range denyPatterns {
				if strings.HasPrefix(dp, "/") {
					if permission.MatchWildcard(dp, m.path) {
						denied = true
						break
					}
				} else {
					base := filepath.Base(m.path)
					rel, _ := filepath.Rel(rootPath, m.path)
					if permission.MatchWildcard(dp, base) || (rel != "" && permission.MatchWildcard(dp, rel)) {
						denied = true
						break
					}
				}
			}
			if !denied {
				filtered = append(filtered, m)
			}
		}
		matches = filtered
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	if len(matches) == 0 {
		return "No matches found", GrepResponseMetadata{Mode: mode, Limit: headLimit}, nil
	}

	pg := paginate(len(matches), offset, headLimit)
	// If the walk was capped, the total we have may be incomplete.
	truncated := pg.truncated || capped
	page := matches[pg.start:pg.end]

	var sb strings.Builder
	note := ""
	if mode == "content" || mode == "count" {
		note = "\n(Advanced search features require ripgrep. Install it for full functionality.)\n"
	}

	if capped {
		fmt.Fprintf(&sb, "Found %d+ files\n", pg.total)
	} else {
		fmt.Fprintf(&sb, "Found %d files\n", pg.total)
	}

	currentFile := ""
	for _, match := range page {
		relPath := toRelativePath(match.path, rootPath)
		if currentFile != relPath {
			if currentFile != "" {
				sb.WriteByte('\n')
			}
			currentFile = relPath
			fmt.Fprintf(&sb, "%s:\n", relPath)
		}
		if match.lineNum > 0 {
			fmt.Fprintf(&sb, "  Line %d: %s\n", match.lineNum, match.lineText)
		}
	}

	if truncated {
		fmt.Fprintf(&sb, "\n(Results truncated: showing %d files. Consider a more specific path or pattern.)", len(page))
	}

	sb.WriteString(note)

	return sb.String(), GrepResponseMetadata{
		Mode:          mode,
		NumberOfFiles: pg.total,
		Truncated:     truncated,
		Offset:        offset,
		Limit:         headLimit,
	}, nil
}

// searchFilesWithRegex walks rootPath and returns files matching the regex.
// collectLimit caps how many matches are collected (0 = no limit, uses a
// sensible default of 500). The boolean return indicates whether the walk
// was stopped early because the cap was reached.
func searchFilesWithRegex(ctx context.Context, pattern, rootPath, include string, collectLimit int) ([]grepMatch, bool, error) {
	if collectLimit <= 0 {
		collectLimit = 500
	}

	matches := []grepMatch{}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includePattern *regexp.Regexp
	if include != "" {
		regexPattern := globToRegex(include)
		includePattern, err = regexp.Compile(regexPattern)
		if err != nil {
			return nil, false, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	capped := false
	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil // Skip errors
		}

		if info.IsDir() {
			// Skip common ignored directories (matching ripgrep's --glob exclusions).
			// Hidden files/dirs are allowed (matching ripgrep's --hidden flag).
			// Never skip the root search directory itself.
			if path != rootPath && slices.Contains(commonIgnoredDirs, filepath.Base(path)) {
				return filepath.SkipDir
			}
			return nil
		}

		if includePattern != nil && !includePattern.MatchString(path) {
			return nil
		}

		match, lineNum, lineText, err := fileContainsPattern(path, regex)
		if err != nil {
			return nil // Skip files we can't read
		}

		if match {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  info.ModTime(),
				lineNum:  lineNum,
				lineText: lineText,
			})

			if len(matches) >= collectLimit {
				capped = true
				return filepath.SkipAll
			}
		}

		return nil
	})
	if err != nil {
		return nil, false, err
	}

	return matches, capped, nil
}

func fileContainsPattern(filePath string, pattern *regexp.Regexp) (bool, int, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, 0, "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if pattern.MatchString(line) {
			return true, lineNum, line, nil
		}
	}

	return false, 0, "", scanner.Err()
}

func globToRegex(glob string) string {
	regexPattern := strings.ReplaceAll(glob, ".", "\\.")
	regexPattern = strings.ReplaceAll(regexPattern, "*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "?", ".")

	re := regexp.MustCompile(`\{([^}]+)\}`)
	regexPattern = re.ReplaceAllStringFunc(regexPattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})

	return regexPattern
}
