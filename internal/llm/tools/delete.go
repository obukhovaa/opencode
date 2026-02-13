package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

type DeleteParams struct {
	Path string `json:"path"`
}

type DeletePermissionsParams struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

type DeleteResponseMetadata struct {
	Diff         string `json:"diff"`
	Removals     int    `json:"removals"`
	FilesDeleted int    `json:"files_deleted"`
}

type deleteTool struct {
	permissions permission.Service
	files       history.Service
	registry    agentregistry.Registry
}

const (
	DeleteToolName    = "delete"
	deleteDescription = `File and directory deletion tool that removes files or directories from the filesystem while tracking changes in file history.

WHEN TO USE THIS TOOL:
- Use when you need to remove files or directories
- Helpful for cleaning up unused code or resources
- Perfect for removing temporary or generated files

HOW TO USE:
- Provide the path to the file or directory to delete
- The tool will handle both files and directories automatically
- All deletions are tracked in file history for visibility

FEATURES:
- Supports both files and directories (directories are deleted recursively)
- Tracks all deletions in file history for visibility in the sidebar
- Generates diffs showing what was removed
- Requires permission for deletions

LIMITATIONS:
- Cannot delete files outside the working directory
- Directory deletions are limited to 500 files for safety
- Cannot undo deletions (files are permanently removed)

TIPS:
- Prefer this over 'rm' in bash for proper file tracking
- Use the View tool first to verify you're deleting the correct file
- Use the LS tool to verify directory contents before deleting
- For large directory deletions (>500 files), use bash rm -rf or delete subdirectories individually`
)

func NewDeleteTool(permissions permission.Service, files history.Service, reg agentregistry.Registry) BaseTool {
	return &deleteTool{
		permissions: permissions,
		files:       files,
		registry:    reg,
	}
}

func (d *deleteTool) Info() ToolInfo {
	return ToolInfo{
		Name:        DeleteToolName,
		Description: deleteDescription,
		Parameters: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The path to the file or directory to delete",
			},
		},
		Required: []string{"path"},
	}
}

func (d *deleteTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params DeleteParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Path == "" {
		return NewTextErrorResponse("path is required"), nil
	}

	absPath := params.Path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(config.WorkingDirectory(), absPath)
	}

	fileInfo, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("file or directory does not exist: %s", absPath)), nil
		}
		return NewEmptyResponse(), fmt.Errorf("error checking path: %w", err)
	}

	rootDir := config.WorkingDirectory()
	if !strings.HasPrefix(absPath, rootDir) {
		return NewTextErrorResponse("cannot delete files outside the working directory"), nil
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session_id and message_id are required")
	}

	if !fileInfo.IsDir() {
		content, err := os.ReadFile(absPath)
		if err != nil {
			return NewEmptyResponse(), fmt.Errorf("error reading file: %w", err)
		}

		diffStr, _, removals := diff.GenerateDiff(string(content), "", absPath)

		action := d.registry.EvaluatePermission(string(GetAgentID(ctx)), DeleteToolName, absPath)
		switch action {
		case permission.ActionAllow:
		case permission.ActionDeny:
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		default:
			p := d.permissions.Request(
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        filepath.Dir(absPath),
					ToolName:    DeleteToolName,
					Action:      "delete",
					Description: fmt.Sprintf("Delete file %s", absPath),
					Params: DeletePermissionsParams{
						Path: absPath,
						Diff: diffStr,
					},
				},
			)
			if !p {
				return NewEmptyResponse(), permission.ErrorPermissionDenied
			}
		}

		err = os.Remove(absPath)
		if err != nil {
			return NewEmptyResponse(), fmt.Errorf("error deleting file: %w", err)
		}

		file, err := d.files.GetByPathAndSession(ctx, absPath, sessionID)
		if err != nil {
			_, err = d.files.Create(ctx, sessionID, absPath, string(content))
			if err != nil {
				logging.Debug("Error creating file history", "error", err)
			}
		}
		if err == nil && file.Content != string(content) {
			_, err = d.files.CreateVersion(ctx, sessionID, absPath, string(content))
			if err != nil {
				logging.Debug("Error creating file history version", "error", err)
			}
		}
		_, err = d.files.CreateVersion(ctx, sessionID, absPath, "")
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}

		recordFileWrite(absPath)
		recordFileRead(absPath)

		result := fmt.Sprintf("<result>\nFile successfully deleted: %s\n</result>", absPath)
		return WithResponseMetadata(NewTextResponse(result),
			DeleteResponseMetadata{
				Diff:         diffStr,
				Removals:     removals,
				FilesDeleted: 1,
			},
		), nil
	}

	type fileEntry struct {
		path    string
		content string
	}
	var files []fileEntry
	totalRemovals := 0

	err = filepath.WalkDir(absPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		if len(files) >= 500 {
			return fmt.Errorf("directory contains more than 500 files")
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		files = append(files, fileEntry{
			path:    path,
			content: string(content),
		})

		_, _, removals := diff.GenerateDiff(string(content), "", path)
		totalRemovals += removals

		return nil
	})
	if err != nil {
		return NewTextErrorResponse("directory contains more than 500 files. Use bash rm -rf for large directory deletions, or delete subdirectories individually"), nil
	}

	action := d.registry.EvaluatePermission(string(GetAgentID(ctx)), DeleteToolName, absPath)
	switch action {
	case permission.ActionAllow:
	case permission.ActionDeny:
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		p := d.permissions.Request(
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        filepath.Dir(absPath),
				ToolName:    DeleteToolName,
				Action:      "delete",
				Description: fmt.Sprintf("Delete directory %s (%d files)", absPath, len(files)),
				Params: DeletePermissionsParams{
					Path: absPath,
					Diff: fmt.Sprintf("Deleting %d files", len(files)),
				},
			},
		)
		if !p {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	err = os.RemoveAll(absPath)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("error deleting directory: %w", err)
	}

	for _, f := range files {
		file, err := d.files.GetByPathAndSession(ctx, f.path, sessionID)
		if err != nil {
			_, err = d.files.Create(ctx, sessionID, f.path, f.content)
			if err != nil {
				logging.Debug("Error creating file history", "error", err)
			}
		}
		if err == nil && file.Content != f.content {
			_, err = d.files.CreateVersion(ctx, sessionID, f.path, f.content)
			if err != nil {
				logging.Debug("Error creating file history version", "error", err)
			}
		}
		_, err = d.files.CreateVersion(ctx, sessionID, f.path, "")
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}

		recordFileWrite(f.path)
		recordFileRead(f.path)
	}

	result := fmt.Sprintf("<result>\nDirectory successfully deleted: %s (%d files removed)\n</result>", absPath, len(files))
	return WithResponseMetadata(NewTextResponse(result),
		DeleteResponseMetadata{
			Diff:         fmt.Sprintf("Deleted %d files", len(files)),
			Removals:     totalRemovals,
			FilesDeleted: len(files),
		},
	), nil
}
