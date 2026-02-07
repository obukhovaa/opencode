package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
)

type MultiEditItem struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type MultiEditParams struct {
	FilePath string          `json:"file_path"`
	Edits    []MultiEditItem `json:"edits"`
}

type MultiEditResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

type multiEditTool struct {
	lspClients  map[string]*lsp.Client
	permissions permission.Service
	files       history.Service
}

const (
	MultiEditToolName    = "multiedit"
	multiEditDescription = `This is a tool for making multiple edits to a single file in one operation. It is built on top of the Edit tool and allows you to perform multiple find-and-replace operations efficiently. Prefer this tool over the Edit tool when you need to make multiple edits to the same file.

Before using this tool:

1. Use the Read tool to understand the file's contents and context
2. Verify the directory path is correct

To make multiple file edits, provide the following:
1. file_path: The absolute path to the file to modify (must be absolute, not relative)
2. edits: An array of edit operations to perform sequentially on the file, where each edit contains:
   - old_string: The text to replace (must match the file contents exactly, including all whitespace and indentation)
   - new_string: The edited text to replace the old_string
   - replace_all: Replace all occurrences of old_string. This parameter is optional and defaults to false.

IMPORTANT:
- All edits are applied in sequence, in the order they are provided
- Each edit operates on the result of the previous edit
- All edits must be valid for the operation to succeed - if any edit fails, none will be applied
- This tool is ideal when you need to make several changes to different parts of the same file

CRITICAL REQUIREMENTS:
1. All edits follow the same requirements as the single Edit tool
2. The edits are atomic - either all succeed or none are applied
3. Plan your edits carefully to avoid conflicts between sequential operations

WARNING:
- The tool will fail if edits.old_string doesn't match the file contents exactly (including whitespace)
- The tool will fail if edits.old_string and edits.new_string are the same
- Since edits are applied in sequence, ensure that earlier edits don't affect the text that later edits are trying to find

When making edits:
- Ensure all edits result in idiomatic, correct code
- Do not leave the code in a broken state
- Always use absolute file paths (starting with /)
- Use replace_all for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.`
)

func NewMultiEditTool(lspClients map[string]*lsp.Client, permissions permission.Service, files history.Service) BaseTool {
	return &multiEditTool{
		lspClients:  lspClients,
		permissions: permissions,
		files:       files,
	}
}

func (m *multiEditTool) Info() ToolInfo {
	return ToolInfo{
		Name:        MultiEditToolName,
		Description: multiEditDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to modify",
			},
			"edits": map[string]any{
				"type":        "array",
				"description": "Array of edit operations to perform sequentially on the file",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_string": map[string]any{
							"type":        "string",
							"description": "The text to replace",
						},
						"new_string": map[string]any{
							"type":        "string",
							"description": "The text to replace it with (must be different from old_string)",
						},
						"replace_all": map[string]any{
							"type":        "boolean",
							"description": "Replace all occurrences of old_string (default false)",
						},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		Required: []string{"file_path", "edits"},
	}
}

func (m *multiEditTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params MultiEditParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	if len(params.Edits) == 0 {
		return NewTextErrorResponse("edits array must not be empty"), nil
	}

	if !filepath.IsAbs(params.FilePath) {
		wd := config.WorkingDirectory()
		params.FilePath = filepath.Join(wd, params.FilePath)
	}

	fileInfo, err := os.Stat(params.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", params.FilePath)), nil
		}
		return NewEmptyResponse(), fmt.Errorf("failed to access file: %w", err)
	}

	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", params.FilePath)), nil
	}

	if getLastReadTime(params.FilePath).IsZero() {
		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
	}

	modTime := fileInfo.ModTime()
	lastRead := getLastReadTime(params.FilePath)
	if modTime.After(lastRead) {
		return NewTextErrorResponse(
			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
				params.FilePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
			)), nil
	}

	content, err := os.ReadFile(params.FilePath)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := string(content)
	currentContent := oldContent

	for i, edit := range params.Edits {
		if edit.OldString == "" {
			return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string cannot be empty in multiedit", i+1)), nil
		}

		if edit.OldString == edit.NewString {
			return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string and new_string must be different", i+1)), nil
		}

		index := strings.Index(currentContent, edit.OldString)
		if index == -1 {
			return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string not found in file. Make sure it matches exactly, including whitespace and line breaks", i+1)), nil
		}

		if edit.ReplaceAll {
			currentContent = strings.ReplaceAll(currentContent, edit.OldString, edit.NewString)
		} else {
			lastIndex := strings.LastIndex(currentContent, edit.OldString)
			if index != lastIndex {
				return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string appears multiple times in the file. Please provide more context to ensure a unique match, or use replace_all to change every instance", i+1)), nil
			}
			currentContent = currentContent[:index] + edit.NewString + currentContent[index+len(edit.OldString):]
		}
	}

	if oldContent == currentContent {
		return NewTextErrorResponse("no changes were made. All edits resulted in the same content."), nil
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session ID and message ID are required")
	}

	combinedDiff, additions, removals := diff.GenerateDiff(
		oldContent,
		currentContent,
		params.FilePath,
	)

	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(params.FilePath)
	if strings.HasPrefix(params.FilePath, rootDir) {
		permissionPath = rootDir
	}
	p := m.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ToolName:    MultiEditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Apply %d edits to file %s", len(params.Edits), params.FilePath),
			Params: EditPermissionsParams{
				FilePath: params.FilePath,
				Diff:     combinedDiff,
			},
		},
	)
	if !p {
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	}

	err = os.WriteFile(params.FilePath, []byte(currentContent), 0o644)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to write file: %w", err)
	}

	file, err := m.files.GetByPathAndSession(ctx, params.FilePath, sessionID)
	if err != nil {
		_, err = m.files.Create(ctx, sessionID, params.FilePath, oldContent)
		if err != nil {
			return NewEmptyResponse(), fmt.Errorf("error creating file history: %w", err)
		}
	}
	if file.Content != oldContent {
		_, err = m.files.CreateVersion(ctx, sessionID, params.FilePath, oldContent)
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}
	}
	_, err = m.files.CreateVersion(ctx, sessionID, params.FilePath, currentContent)
	if err != nil {
		logging.Debug("Error creating file history version", "error", err)
	}

	recordFileWrite(params.FilePath)
	recordFileRead(params.FilePath)

	response := WithResponseMetadata(
		NewTextResponse(fmt.Sprintf("%d edits applied to file: %s", len(params.Edits), params.FilePath)),
		MultiEditResponseMetadata{
			Diff:      combinedDiff,
			Additions: additions,
			Removals:  removals,
		},
	)

	waitForLspDiagnostics(ctx, params.FilePath, m.lspClients)
	text := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)
	text += getDiagnostics(params.FilePath, m.lspClients)
	response.Content = text
	return response, nil
}
