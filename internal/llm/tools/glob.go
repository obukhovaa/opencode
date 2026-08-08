package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

const (
	GlobToolName    = "glob"
	globDescription = `Fast file pattern matching that returns paths matching a glob (e.g. "**/*.js", "src/**/*.{ts,tsx}"), sorted by modification time, newest first.

- Results are capped at 100 files; hidden files (dot-prefixed) are skipped. Refine the pattern if results are truncated.
- Matches names only — use the grep tool for file contents, and the task tool for open-ended searches needing multiple rounds.
- Batch speculative searches by issuing several calls in one response.`
)

type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

type globTool struct {
	registry    agentregistry.Registry
	permissions permission.Service
}

func NewGlobTool(reg agentregistry.Registry, permissions permission.Service) BaseTool {
	return &globTool{registry: reg, permissions: permissions}
}

func (g *globTool) Info() ToolInfo {
	return ToolInfo{
		Name:        GlobToolName,
		Description: globDescription,
		Parameters: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The glob pattern to match files against",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to the current working directory.",
			},
		},
		Required: []string{"pattern"},
	}
}

func (g *globTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GlobParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = config.WorkingDirectory()
	}

	if err := checkReadPermission(ctx, g.registry, g.permissions, GlobToolName, searchPath); err != nil {
		if err == permission.ErrorPermissionDenied {
			return NewTextErrorResponse(fmt.Sprintf("Permission denied: globbing %s", searchPath)), nil
		}
		return NewEmptyResponse(), err
	}

	info, err := os.Stat(searchPath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("path does not exist: %s", searchPath)), nil
		}
		return NewEmptyResponse(), fmt.Errorf("error accessing path: %w", err)
	}
	if !info.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("path is a file, not a directory: %s. Provide a directory path instead.", searchPath)), nil
	}

	files, truncated, err := globFiles(ctx, params.Pattern, searchPath, 100)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("error finding files: %w", err)
	}

	var output string
	if len(files) == 0 {
		output = "No files found"
	} else {
		output = strings.Join(files, "\n")
		if truncated {
			output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return WithResponseMetadata(
		NewTextResponse(output),
		GlobResponseMetadata{
			NumberOfFiles: len(files),
			Truncated:     truncated,
		},
	), nil
}

func (g *globTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (g *globTool) IsBaseline() bool { return true }

func globFiles(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	timeout := fileutil.FileOpTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdRg := fileutil.GetRgCmd(ctx, pattern)
	if cmdRg != nil {
		cmdRg.Dir = searchPath
		matches, err := runRipgrep(cmdRg, searchPath, limit)
		if err == nil {
			return matches, len(matches) >= limit && limit > 0, nil
		}
		logging.Warn(fmt.Sprintf("Ripgrep execution failed: %v. Falling back to doublestar.", err))
	}

	return fileutil.GlobWithDoublestar(ctx, pattern, searchPath, limit)
}

func runRipgrep(cmd *exec.Cmd, searchRoot string, limit int) ([]string, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("ripgrep: %w\n%s", err, out)
	}

	var matches []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		absPath := string(p)
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(searchRoot, absPath)
		}
		if fileutil.SkipHidden(absPath) {
			continue
		}
		matches = append(matches, absPath)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		iInfo, iErr := os.Stat(matches[i])
		jInfo, jErr := os.Stat(matches[j])
		if iErr != nil || jErr != nil {
			return len(matches[i]) < len(matches[j])
		}
		return iInfo.ModTime().After(jInfo.ModTime())
	})

	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}
