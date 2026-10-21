package tools

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/memory"
)

type memoryFeedback struct {
	BaseTool
	root, cwd string
}

func WithMemoryFeedback(tool BaseTool, root, cwd string) BaseTool {
	switch tool.Info().Name {
	case WriteToolName, EditToolName, MultiEditToolName, PatchToolName:
		return &memoryFeedback{BaseTool: tool, root: root, cwd: cwd}
	}
	return tool
}

func (m *memoryFeedback) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	result, err := m.BaseTool.Run(ctx, call)
	if err != nil || result.IsError {
		return result, err
	}
	var warnings []string
	seen := map[string]bool{}
	for _, path := range ExtractPathsFromCall(call) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.cwd, path)
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if warning := memory.CheckFile(m.root, path); warning != "" {
			warnings = append(warnings, warning)
		}
		if len(warnings) == 5 {
			break
		}
	}
	if len(warnings) > 0 {
		result.Content += "\n<system-reminder>\nMemory write succeeded. Authoring feedback:\n" + strings.Join(warnings, "\n") + "\nUse concise facts and source links; do not discard facts merely to satisfy a size budget.\n</system-reminder>"
	}
	return result, nil
}
