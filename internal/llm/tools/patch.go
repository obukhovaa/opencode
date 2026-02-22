package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
)

type PatchParams struct {
	PatchText string `json:"patch_text"`
}

type PatchResponseMetadata struct {
	FilesChanged []string `json:"files_changed"`
	Additions    int      `json:"additions"`
	Removals     int      `json:"removals"`
}

type patchTool struct {
	lsp         lsp.LspService
	permissions permission.Service
	files       history.Service
	registry    agentregistry.Registry
}

const (
	PatchToolName    = "patch"
	patchDescription = `Applies a patch to multiple files in one operation. This tool is useful for making coordinated changes across multiple files.

The patch text must follow this format:

` + "```" + `
*** Begin Patch
[ one or more file sections ]
*** End Patch
` + "```" + `

Each section starts with one of three headers:

*** Add File: <path> - create a new file. Every following line must be a + line (the initial contents).
*** Delete File: <path> - remove an existing file. Nothing follows.
*** Update File: <path> - patch an existing file in place, optionally followed by *** Move to: <new_path> to rename.

Example patch:

` + "```" + `
*** Begin Patch
*** Add File: hello.txt
+Hello world
*** Update File: src/app.py
*** Move to: src/main.py
@@ def greet():
-print("Hi")
+print("Hello, world!")
*** Delete File: obsolete.txt
*** End Patch
` + "```" + `

Before using this tool:
1. Use the View tool to understand the files' content and context
2. Verify all file paths are correct (use the LS tool)

Important:
- You must include a header with your intended action (Add/Delete/Update)
- You must prefix new lines with ` + "`+`" + ` even when creating a new file
- Context lines (@@) must uniquely identify the section you want to change
- All whitespace, indentation, and surrounding code must match exactly
- Always use absolute file paths (starting with /)`
)

func NewPatchTool(lspService lsp.LspService, permissions permission.Service, files history.Service, reg agentregistry.Registry) BaseTool {
	return &patchTool{
		lsp:         lspService,
		permissions: permissions,
		files:       files,
		registry:    reg,
	}
}

func (p *patchTool) Info() ToolInfo {
	return ToolInfo{
		Name:        PatchToolName,
		Description: patchDescription,
		Parameters: map[string]any{
			"patch_text": map[string]any{
				"type":        "string",
				"description": "The full patch text that describes all changes to be made",
			},
		},
		Required: []string{"patch_text"},
	}
}

func (p *patchTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params PatchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.PatchText == "" {
		return NewTextErrorResponse("patch_text is required"), nil
	}

	normalized := strings.ReplaceAll(strings.ReplaceAll(params.PatchText, "\r\n", "\n"), "\r", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "*** Begin Patch\n*** End Patch" || normalized == "*** Begin Patch\r\n*** End Patch" {
		return NewTextErrorResponse("patch rejected: empty patch"), nil
	}

	// Identify all files needed for the patch and verify they've been read
	filesToRead := diff.IdentifyFilesNeeded(params.PatchText)
	for _, filePath := range filesToRead {
		absPath := filePath
		if !filepath.IsAbs(absPath) {
			wd := config.WorkingDirectory()
			absPath = filepath.Join(wd, absPath)
		}

		if getLastReadTime(absPath).IsZero() {
			return NewTextErrorResponse(fmt.Sprintf("you must read the file %s before patching it. Use the FileRead tool first", filePath)), nil
		}

		fileInfo, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return NewTextErrorResponse(fmt.Sprintf("file not found: %s", absPath)), nil
			}
			return NewEmptyResponse(), fmt.Errorf("failed to access file: %w", err)
		}

		if fileInfo.IsDir() {
			return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", absPath)), nil
		}

		modTime := fileInfo.ModTime()
		lastRead := getLastReadTime(absPath)
		if modTime.After(lastRead) {
			return NewTextErrorResponse(
				fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
					absPath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
				)), nil
		}
	}

	// Check for new files to ensure they don't already exist
	filesToAdd := diff.IdentifyFilesAdded(params.PatchText)
	for _, filePath := range filesToAdd {
		absPath := filePath
		if !filepath.IsAbs(absPath) {
			wd := config.WorkingDirectory()
			absPath = filepath.Join(wd, absPath)
		}

		_, err := os.Stat(absPath)
		if err == nil {
			return NewTextErrorResponse(fmt.Sprintf("file already exists and cannot be added: %s", absPath)), nil
		} else if !os.IsNotExist(err) {
			return NewEmptyResponse(), fmt.Errorf("failed to check file: %w", err)
		}
	}

	// Load all required files
	currentFiles := make(map[string]string)
	for _, filePath := range filesToRead {
		absPath := filePath
		if !filepath.IsAbs(absPath) {
			wd := config.WorkingDirectory()
			absPath = filepath.Join(wd, absPath)
		}

		content, err := os.ReadFile(absPath)
		if err != nil {
			return NewEmptyResponse(), fmt.Errorf("failed to read file %s: %w", absPath, err)
		}
		currentFiles[filePath] = string(content)
	}

	// Process the patch
	patch, fuzz, err := diff.TextToPatch(params.PatchText, currentFiles)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to parse patch: %s", err)), nil
	}

	if fuzz > 3 {
		return NewTextErrorResponse(fmt.Sprintf("patch contains fuzzy matches (fuzz level: %d). Please make your context lines more precise", fuzz)), nil
	}

	// Convert patch to commit
	commit, err := diff.PatchToCommit(patch, currentFiles)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to create commit from patch: %s", err)), nil
	}

	// Get session ID and message ID
	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session ID and message ID are required for creating a patch")
	}

	// Request permission for all changes
	var combinedDiff string
	needsPermission := false
	for filePath, change := range commit.Changes {
		fileAction := p.registry.EvaluatePermission(string(GetAgentID(ctx)), PatchToolName, filePath)
		if fileAction == permission.ActionDeny {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
		if fileAction == permission.ActionAllow {
			continue
		}
		needsPermission = true

		oldContent := ""
		if change.OldContent != nil {
			oldContent = *change.OldContent
		}
		newContent := ""
		if change.NewContent != nil {
			newContent = *change.NewContent
		}
		fileDiff, _, _ := diff.GenerateDiff(oldContent, newContent, filePath)
		combinedDiff += fileDiff + "\n"
	}

	if needsPermission {
		filePaths := make([]string, 0, len(commit.Changes))
		for filePath := range commit.Changes {
			filePaths = append(filePaths, filePath)
		}
		rootDir := config.WorkingDirectory()
		permissionPath := rootDir

		allowed := p.permissions.Request(
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        permissionPath,
				ToolName:    PatchToolName,
				Action:      "write",
				Description: fmt.Sprintf("Apply patch to %d files: %s", len(filePaths), strings.Join(filePaths, ", ")),
				Params: EditPermissionsParams{
					FilePath: strings.Join(filePaths, ", "),
					Diff:     combinedDiff,
				},
			},
		)
		if !allowed {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	// Apply the changes to the filesystem
	err = diff.ApplyCommit(commit, func(path string, content string) error {
		absPath := path
		if !filepath.IsAbs(absPath) {
			wd := config.WorkingDirectory()
			absPath = filepath.Join(wd, absPath)
		}

		// Create parent directories if needed
		dir := filepath.Dir(absPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create parent directories for %s: %w", absPath, err)
		}

		return os.WriteFile(absPath, []byte(content), 0o644)
	}, func(path string) error {
		absPath := path
		if !filepath.IsAbs(absPath) {
			wd := config.WorkingDirectory()
			absPath = filepath.Join(wd, absPath)
		}
		return os.Remove(absPath)
	})
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to apply patch: %s", err)), nil
	}

	// Update file history for all modified files
	changedFiles := []string{}
	totalAdditions := 0
	totalRemovals := 0

	for path, change := range commit.Changes {
		absPath := path
		if !filepath.IsAbs(absPath) {
			wd := config.WorkingDirectory()
			absPath = filepath.Join(wd, absPath)
		}
		changedFiles = append(changedFiles, absPath)

		oldContent := ""
		if change.OldContent != nil {
			oldContent = *change.OldContent
		}

		newContent := ""
		if change.NewContent != nil {
			newContent = *change.NewContent
		}

		// Calculate diff statistics
		_, additions, removals := diff.GenerateDiff(oldContent, newContent, path)
		totalAdditions += additions
		totalRemovals += removals

		// Update history
		file, err := p.files.GetByPathAndSession(ctx, absPath, sessionID)
		if err != nil && change.Type != diff.ActionAdd {
			// If not adding a file, create history entry for existing file
			_, err = p.files.Create(ctx, sessionID, absPath, oldContent)
			if err != nil {
				logging.Debug("Error creating file history", "error", err)
			}
		}

		if err == nil && change.Type != diff.ActionAdd && file.Content != oldContent {
			// User manually changed content, store intermediate version
			_, err = p.files.CreateVersion(ctx, sessionID, absPath, oldContent)
			if err != nil {
				logging.Debug("Error creating file history version", "error", err)
			}
		}

		// Store new version
		if change.Type == diff.ActionDelete {
			_, err = p.files.CreateVersion(ctx, sessionID, absPath, "")
		} else {
			_, err = p.files.CreateVersion(ctx, sessionID, absPath, newContent)
		}
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}

		// Record file operations
		recordFileWrite(absPath)
		recordFileRead(absPath)
	}

	// Run LSP diagnostics on all changed files
	for _, filePath := range changedFiles {
		p.lsp.WaitForDiagnostics(ctx, filePath)
	}

	result := fmt.Sprintf("Patch applied successfully. %d files changed, %d additions, %d removals",
		len(changedFiles), totalAdditions, totalRemovals)

	diagnosticsText := ""
	for _, filePath := range changedFiles {
		diagnosticsText += p.lsp.FormatDiagnostics(filePath)
	}

	if diagnosticsText != "" {
		result += "\n\nDiagnostics:\n" + diagnosticsText
	}

	return WithResponseMetadata(
		NewTextResponse(result),
		PatchResponseMetadata{
			FilesChanged: changedFiles,
			Additions:    totalAdditions,
			Removals:     totalRemovals,
		}), nil
}
