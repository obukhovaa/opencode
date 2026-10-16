package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
)

type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

type viewTool struct {
	lsp         lsp.LspService
	registry    agentregistry.Registry
	permissions permission.Service
}

type ViewResponseMetadata struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

const (
	ReadToolName     = "read"
	MaxReadSize      = 250 * 1024
	DefaultReadLimit = 2000
	MaxLineLength    = 2000
	viewDescription  = `Reads a file from the local filesystem and returns its contents with line numbers.

- Reads up to 2000 lines from the start by default; use offset/limit for other sections. Avoid tiny repeated slices (e.g. 30-line chunks) — read a larger window in a single call.
- A whole-file read (no offset or limit) is limited to files up to 250KB; larger files can be read in pages with offset/limit. One call returns at most 250KB; lines longer than 2000 characters are truncated.
- Cannot display binary files or images (use the view_image tool for images).
- Suggests similar file names when the requested file is not found.`
)

func NewReadTool(lspService lsp.LspService, reg agentregistry.Registry, permissions permission.Service) BaseTool {
	return &viewTool{
		lsp:         lspService,
		registry:    reg,
		permissions: permissions,
	}
}

func (v *viewTool) Info() ToolInfo {
	return ToolInfo{
		Name:        ReadToolName,
		Description: viewDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to read",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "The line number to start reading from (0-based)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "The number of lines to read (defaults to 2000)",
			},
		},
		Required: []string{"file_path"},
	}
}

// Run implements Tool.
func (v *viewTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params ViewParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	// Handle relative paths
	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(config.WorkingDirectory(), filePath)
	}

	if err := checkReadPermission(ctx, v.registry, v.permissions, ReadToolName, filePath); err != nil {
		if err == permission.ErrorPermissionDenied {
			return NewTextErrorResponse(fmt.Sprintf("Permission denied: reading %s", filePath)), nil
		}
		return NewEmptyResponse(), err
	}

	// Check if file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to offer suggestions for similarly named files
			dir := filepath.Dir(filePath)
			base := filepath.Base(filePath)

			dirEntries, dirErr := os.ReadDir(dir)
			if dirErr == nil {
				var suggestions []string
				for _, entry := range dirEntries {
					if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(base)) ||
						strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name())) {
						suggestions = append(suggestions, filepath.Join(dir, entry.Name()))
						if len(suggestions) >= 3 {
							break
						}
					}
				}

				if len(suggestions) > 0 {
					return NewTextErrorResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
						filePath, strings.Join(suggestions, "\n"))), nil
				}
			}

			return NewTextErrorResponse(fmt.Sprintf("File not found: %s", filePath)), nil
		}
		return NewEmptyResponse(), fmt.Errorf("error accessing file: %w", err)
	}

	// Check if it's a directory
	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
	}

	if params.Offset < 0 {
		params.Offset = 0
	}
	// The size ceiling guards a whole-file read only. A paged read (offset or
	// limit given) streams the file and is bounded per call by the line limit
	// and MaxReadSize on the returned text instead, so a large log or a tool
	// output spill stays readable in pages.
	largeFile := fileInfo.Size() > MaxReadSize
	if largeFile && params.Offset == 0 && params.Limit <= 0 {
		return NewTextErrorResponse(fmt.Sprintf("File is too large to read whole (%d bytes; the limit for a whole-file read is %d bytes). "+
			"Read it in pages with offset and limit (for example offset=0, limit=500), or search it with the grep tool.",
			fileInfo.Size(), MaxReadSize)), nil
	}

	// Set default limit if not provided
	if params.Limit <= 0 {
		params.Limit = DefaultReadLimit
	}

	// Cap the limit to the maximum allowed
	if params.Limit > DefaultReadLimit {
		params.Limit = DefaultReadLimit
	}

	// Check if it's an image file
	isImage, imageType := isImageFile(filePath)
	if isImage {
		return NewTextErrorResponse(fmt.Sprintf("This is an image file of type: %s\nUse a view_image tool to process images", imageType)), nil
	}

	// Check if it's a binary file by sampling content
	if isBinary, err := isBinaryFile(filePath); err == nil && isBinary {
		return NewTextErrorResponse("File appears to be binary. Use the appropriate tool for this file type."), nil
	}

	// Read the file content
	content, linesRead, lineCount, capped, err := readTextFile(filePath, params.Offset, params.Limit, MaxReadSize)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("error reading file: %w", err)
	}
	if linesRead == 0 && params.Offset > 0 && params.Offset >= lineCount {
		return NewTextErrorResponse(fmt.Sprintf("Offset %d is past the end of the file, which has %d lines.", params.Offset, lineCount)), nil
	}

	// The LSP client sends the whole file in didOpen; past the whole-read
	// ceiling that would load all of it, so a paged read of a large file
	// skips the LSP.
	if !largeFile {
		v.lsp.NotifyOpenFile(ctx, filePath)
	}
	output := "<file>\n"
	// Format the output with line numbers
	output += addLineNumbers(content, params.Offset+1)

	// Add a note if the content was truncated
	startLine := params.Offset + 1
	endLine := params.Offset + linesRead
	if lineCount > endLine {
		reason := ""
		if capped {
			reason = fmt.Sprintf(" This call's output limit (%d bytes) was reached.", MaxReadSize)
		}
		output += fmt.Sprintf("\n\n(Showing lines %d-%d of %d total.%s Use offset=%d to continue reading.)",
			startLine, endLine, lineCount, reason, endLine)
	}
	output += "\n</file>\n"
	if !largeFile {
		output += v.lsp.FormatDiagnostics(filePath)
	}
	recordFileRead(filePath)
	return WithResponseMetadata(
		NewTextResponse(output),
		ViewResponseMetadata{
			FilePath: filePath,
			Content:  content,
		},
	), nil
}

func (v *viewTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (v *viewTool) IsBaseline() bool { return true }

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

// readTextFile returns up to limit lines of the file starting after the first
// offset lines, each cut to MaxLineLength, and stops adding lines once the
// returned text would pass maxBytes (capped). It streams the file, so memory
// is bounded by the returned window rather than by the file or its longest
// line. linesRead is the number of lines returned, lineCount the file's total.
func readTextFile(filePath string, offset, limit, maxBytes int) (content string, linesRead, lineCount int, capped bool, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, 0, false, err
	}
	defer file.Close()

	r := bufio.NewReaderSize(file, 64*1024)
	var sb strings.Builder
	for {
		keep := lineCount >= offset && linesRead < limit && !capped
		line, ok, err := readLine(r, keep)
		if err != nil {
			return "", 0, 0, false, err
		}
		if !ok {
			break
		}
		lineCount++
		if !keep {
			continue
		}
		size := len(line)
		if linesRead > 0 {
			size++ // the joining newline
		}
		// The first line always fits: it is at most MaxLineLength bytes.
		if linesRead > 0 && sb.Len()+size > maxBytes {
			capped = true
			continue
		}
		if linesRead > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(line)
		linesRead++
	}
	return sb.String(), linesRead, lineCount, capped, nil
}

// readLine reads one line without its line ending. With keep it returns the
// line cut to MaxLineLength ("..." marks a cut); without, it only consumes
// it. ok is false at end of file. A line longer than the reader's buffer is
// consumed in chunks and never held whole.
func readLine(r *bufio.Reader, keep bool) (line string, ok bool, err error) {
	var buf []byte
	cut := false
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err == io.EOF {
			if ok {
				break
			}
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		ok = true
		if keep {
			room := MaxLineLength - len(buf)
			if len(chunk) > room {
				buf = append(buf, chunk[:room]...)
				cut = true
			} else {
				buf = append(buf, chunk...)
			}
		}
		if !isPrefix {
			break
		}
	}
	if cut {
		return string(buf) + "...", true, nil
	}
	return string(buf), true, nil
}

func isImageFile(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "JPEG"
	case ".png":
		return true, "PNG"
	case ".gif":
		return true, "GIF"
	case ".bmp":
		return true, "BMP"
	case ".svg":
		return true, "SVG"
	case ".webp":
		return true, "WebP"
	default:
		return false, ""
	}
}

func isBinaryFile(filePath string) (bool, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	buf = buf[:n]

	nonPrintable := 0
	for _, b := range buf {
		if b == 0 {
			return true, nil
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\x1b' {
			nonPrintable++
		}
	}

	return float64(nonPrintable)/float64(n) > 0.3, nil
}
