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

type MultiEditItem struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type MultiEditParams struct {
	FilePath string          `json:"file_path"`
	Edits    []MultiEditItem `json:"edits"`
}

type MultiEditPermissionEdit struct {
	Diff       string `json:"diff"`
	LineNumber int    `json:"line_number"`
}

type MultiEditPermissionsParams struct {
	FilePath string                    `json:"file_path"`
	Edits    []MultiEditPermissionEdit `json:"edits"`
}

type MultiEditResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

type multiEditTool struct {
	lsp         lsp.LspService
	permissions permission.Service
	files       history.Service
	registry    agentregistry.Registry
}

const (
	MultiEditToolName    = "multiedit"
	multiEditDescription = `Applies multiple exact string replacements to a single file in one atomic operation. Prefer this over several edit calls on the same file.

- Same read-first and uniqueness contract as the edit tool: the file must have been read first, and each old_string must match exactly and be unique (or set replace_all). Unlike edit, empty old_string is rejected — multiedit cannot create files; use write or edit for that.
- Edits apply in order, each operating on the result of the previous one — make sure earlier edits don't change text later edits need to find.
- Atomic: if any edit fails, none are applied.`
)

func NewMultiEditTool(lspService lsp.LspService, permissions permission.Service, files history.Service, reg agentregistry.Registry) BaseTool {
	return &multiEditTool{
		lsp:         lspService,
		permissions: permissions,
		files:       files,
		registry:    reg,
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
		return NewTextErrorResponse("you must read the file before editing it. Use the Read tool first"), nil
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

	oldContent := strings.ReplaceAll(string(content), "\r\n", "\n")
	currentContent := oldContent
	perEditDiffs := make([]MultiEditPermissionEdit, 0, len(params.Edits))

	for i, edit := range params.Edits {
		if edit.OldString == "" {
			return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string cannot be empty in multiedit", i+1)), nil
		}

		if edit.OldString == edit.NewString {
			return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string and new_string must be different", i+1)), nil
		}

		normalizedOldString := strings.ReplaceAll(edit.OldString, "\r\n", "\n")
		normalizedNewString := strings.ReplaceAll(edit.NewString, "\r\n", "\n")

		index := strings.Index(currentContent, normalizedOldString)
		if index == -1 {
			return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string not found in file. Make sure it matches exactly, including whitespace and line breaks", i+1)), nil
		}

		lineNumber := strings.Count(currentContent[:index], "\n") + 1
		beforeEdit := currentContent

		if edit.ReplaceAll {
			currentContent = strings.ReplaceAll(currentContent, normalizedOldString, normalizedNewString)
		} else {
			lastIndex := strings.LastIndex(currentContent, normalizedOldString)
			if index != lastIndex {
				count := strings.Count(currentContent, normalizedOldString)
				return NewTextErrorResponse(fmt.Sprintf("edit %d: old_string appears %d times in the file. Please provide more surrounding context lines in old_string to make the match unique, or use replace_all=true to replace all occurrences", i+1, count)), nil
			}
			currentContent = currentContent[:index] + normalizedNewString + currentContent[index+len(normalizedOldString):]
		}

		editDiff, _, _ := diff.GenerateDiff(beforeEdit, currentContent, params.FilePath)
		perEditDiffs = append(perEditDiffs, MultiEditPermissionEdit{
			Diff:       editDiff,
			LineNumber: lineNumber,
		})
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
	action := m.registry.EvaluatePermission(string(GetAgentID(ctx)), MultiEditToolName, params.FilePath)
	switch action {
	case permission.ActionAllow:
		// Allowed by config
	case permission.ActionDeny:
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		p := m.permissions.Request(ctx,
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        permissionPath,
				ToolName:    MultiEditToolName,
				Action:      "write",
				Description: fmt.Sprintf("Apply %d edits to file %s", len(params.Edits), params.FilePath),
				Params: MultiEditPermissionsParams{
					FilePath: params.FilePath,
					Edits:    perEditDiffs,
				},
			},
		)
		if !p {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
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

	m.lsp.WaitForDiagnostics(ctx, params.FilePath)
	text := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)
	text += m.lsp.FormatDiagnostics(params.FilePath)
	response.Content = text
	return response, nil
}

func (m *multiEditTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	var params MultiEditParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return false
	}
	return !hasFileConflict(call, []string{params.FilePath}, allCalls)
}

func (m *multiEditTool) IsBaseline() bool { return true }
