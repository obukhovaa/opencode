package format

import (
	"context"

	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
)

// ExpandShellMarkup finds all !`command` blocks in template, executes each
// command via the persistent shell, and replaces the block with the command's
// output. On error or non-zero exit, error text is inlined rather than failing.
func ExpandShellMarkup(ctx context.Context, template string, cwd string) string {
	return shell.ExpandMarkup(ctx, template, cwd)
}
