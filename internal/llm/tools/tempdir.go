package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxPersistBytes    = 100 * 1024 * 1024 // 100MB
	TruncatedHeadLines = 500
	TruncatedTailLines = 500
)

var (
	processTempDir   string
	processTempDirMu sync.Mutex
)

func ensureTempDir() (string, error) {
	processTempDirMu.Lock()
	defer processTempDirMu.Unlock()

	if processTempDir != "" {
		return processTempDir, nil
	}

	dir := filepath.Join(os.TempDir(), fmt.Sprintf("opencode-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	processTempDir = dir
	return dir, nil
}

func persistToTempFile(content, prefix string) string {
	dir, err := ensureTempDir()
	if err != nil {
		return ""
	}

	data := content
	if len(data) > MaxPersistBytes {
		data = data[:MaxPersistBytes]
	}

	// os.CreateTemp fills the '*' with a unique suffix atomically (0600), so
	// concurrent spills with the same prefix can never overwrite each other.
	// The prefix is sanitized because it may embed caller-supplied names (e.g.
	// an MCP server's tool name) that must not influence the file's directory.
	pattern := fmt.Sprintf("%s-%d-*.txt", sanitizeFilePrefix(prefix), time.Now().UnixNano())
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return ""
	}
	defer f.Close()

	if _, err := f.WriteString(data); err != nil {
		os.Remove(f.Name())
		return ""
	}

	return f.Name()
}

// sanitizeFilePrefix reduces a caller-supplied temp-file prefix to a single safe
// filename component: any byte outside [a-zA-Z0-9._-] (e.g. path separators in
// an MCP-server-supplied tool name) becomes '_', and the result is capped to a
// filesystem-friendly length. An all-unsafe or empty prefix yields "output".
func sanitizeFilePrefix(prefix string) string {
	const maxPrefixLen = 80
	var sb strings.Builder
	for _, r := range prefix {
		if sb.Len() >= maxPrefixLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	if sb.Len() == 0 {
		return "output"
	}
	return sb.String()
}

// CleanupTempDir removes the process-scoped temp directory and all its contents.
func CleanupTempDir() {
	processTempDirMu.Lock()
	defer processTempDirMu.Unlock()

	if processTempDir != "" {
		os.RemoveAll(processTempDir)
		processTempDir = ""
	}
}

// buildPreview returns a line-aligned head+tail preview of content.
// If the content has fewer lines than headN+tailN, it is returned unchanged.
func buildPreview(content string, headN, tailN int) (string, int) {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	if totalLines <= headN+tailN {
		return content, totalLines
	}

	head := strings.Join(lines[:headN], "\n")
	tail := strings.Join(lines[totalLines-tailN:], "\n")
	truncatedCount := totalLines - headN - tailN

	return fmt.Sprintf("--- First %d lines ---\n%s\n\n... [%d lines truncated] ...\n\n--- Last %d lines ---\n%s",
		headN, head, truncatedCount, tailN, tail), totalLines
}

func buildTruncationHeader(label string, totalLines int, filePath string, originalSize int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<%s truncated: %d lines total>\n", label, totalLines)
	if filePath != "" {
		if originalSize > MaxPersistBytes {
			fmt.Fprintf(&sb, "Full output saved to: %s (truncated at 100MB)\n", filePath)
		} else {
			fmt.Fprintf(&sb, "Full output saved to: %s\n", filePath)
		}
		sb.WriteString("Use the read tool with offset/limit to read specific sections.\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// PersistLargeOutput caps a tool call's output at maxBytes for the model's
// context while keeping the full payload available on disk. When content is
// within the cap — or maxBytes <= 0, meaning "unlimited" — it is returned
// unchanged with an empty path. Otherwise the full content is spilled to a temp
// file (same per-process scratch dir as the bash tool) and a compact,
// byte-aligned head+tail preview (~maxBytes total) is returned along with the
// file path, so the caller can point the agent at the file to explore with the
// grep/read/bash tools rather than re-running the tool.
//
// source/label form the temp-file prefix ("<source>-<label>-<nanos>.txt") and
// label names the output in the overflow header.
func PersistLargeOutput(content, label, source string, maxBytes int) (preview string, filePath string) {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content, ""
	}
	filePath = persistToTempFile(content, source+"-"+label)
	head := maxBytes / 2
	tail := maxBytes - head
	return buildOutputOverflowHeader(label, len(content), filePath) + buildBytePreview(content, head, tail), filePath
}

// buildBytePreview returns a byte-aligned head+tail preview of content, keeping
// roughly headBytes from the start and tailBytes from the end with an elision
// marker between them. Unlike buildPreview it is byte- (not line-) based, so it
// bounds single-line payloads such as minified JSON. Cut points are snapped to
// UTF-8 rune boundaries (and to a nearby newline when one exists) so the preview
// never splits a rune or a line mid-way.
func buildBytePreview(content string, headBytes, tailBytes int) string {
	if len(content) <= headBytes+tailBytes {
		return content
	}
	headEnd := toRuneBoundaryBackward(content, headBytes)
	if nl := strings.LastIndexByte(content[:headEnd], '\n'); nl > headBytes/2 {
		headEnd = nl
	}
	tailStart := toRuneBoundaryForward(content, len(content)-tailBytes)
	if nl := strings.IndexByte(content[tailStart:], '\n'); nl >= 0 && nl < tailBytes/2 {
		tailStart += nl + 1
	}
	elided := tailStart - headEnd
	return fmt.Sprintf("%s\n\n... [%d bytes elided — full output in the saved file] ...\n\n%s",
		content[:headEnd], elided, content[tailStart:])
}

func buildOutputOverflowHeader(label string, totalBytes int, filePath string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<%s output truncated: %d bytes total>\n", label, totalBytes)
	if filePath != "" {
		if totalBytes > MaxPersistBytes {
			fmt.Fprintf(&sb, "Full output saved to: %s (saved copy truncated at 100MB)\n", filePath)
		} else {
			fmt.Fprintf(&sb, "Full output saved to: %s\n", filePath)
		}
		sb.WriteString("Explore it with the grep tool, read specific ranges with the read tool (offset/limit), or use sed in bash. Do not re-run the tool just to get the full output.\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// toRuneBoundaryBackward returns the largest index <= i that starts a UTF-8 rune.
func toRuneBoundaryBackward(s string, i int) int {
	if i >= len(s) {
		i = len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// toRuneBoundaryForward returns the smallest index >= i that starts a UTF-8 rune.
func toRuneBoundaryForward(s string, i int) int {
	if i < 0 {
		i = 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// truncateToMaxChars truncates content to fit within maxChars,
// preferring to cut at line boundaries.
func truncateToMaxChars(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	cutPoint := maxChars
	if idx := strings.LastIndex(content[:cutPoint], "\n"); idx > 0 {
		cutPoint = idx
	}
	return content[:cutPoint]
}
