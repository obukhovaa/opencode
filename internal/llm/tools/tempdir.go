package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

	filename := fmt.Sprintf("%s-%d.txt", prefix, time.Now().UnixNano())
	filePath := filepath.Join(dir, filename)

	data := content
	if len(data) > MaxPersistBytes {
		data = data[:MaxPersistBytes]
	}

	if err := os.WriteFile(filePath, []byte(data), 0o600); err != nil {
		return ""
	}

	return filePath
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
		sb.WriteString("Use the view tool with offset/limit to read specific sections.\n")
	}
	sb.WriteString("\n")
	return sb.String()
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
