package format

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/logging"
)

var shellMarkupRegex = regexp.MustCompile("!`([^`]+)`")

// ExpandShellMarkup finds all !`command` blocks in template, executes each
// command via the persistent shell, and replaces the block with the command's
// output. On error or non-zero exit, error text is inlined rather than failing.
func ExpandShellMarkup(ctx context.Context, template string, cwd string) string {
	if !strings.Contains(template, "!`") {
		return template
	}

	return shellMarkupRegex.ReplaceAllStringFunc(template, func(match string) string {
		submatches := shellMarkupRegex.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		command := submatches[1]

		sh := shell.GetPersistentShell(cwd)
		if sh == nil {
			logging.Warn("ExpandShellMarkup: failed to get shell", "command", command)
			return fmt.Sprintf("[shell unavailable: %s]", command)
		}

		stdout, stderr, exitCode, _, err := sh.Exec(ctx, command, tools.DefaultTimeout)
		if err != nil {
			logging.Warn("ExpandShellMarkup: exec error", "command", command, "error", err)
			return fmt.Sprintf("[command error: %s]\n%s", command, err.Error())
		}

		// Truncate large output
		stdout = truncateShellOutput(stdout)
		stderr = truncateShellOutput(stderr)

		if exitCode != 0 {
			// Non-zero exit: include both stdout and stderr
			var parts []string
			if stdout != "" {
				parts = append(parts, stdout)
			}
			if stderr != "" {
				parts = append(parts, stderr)
			}
			result := strings.Join(parts, "\n")
			if result == "" {
				return fmt.Sprintf("[command failed: exit %d]", exitCode)
			}
			return fmt.Sprintf("```\n$ %s\n%s\n[exit code: %d]\n```", command, result, exitCode)
		}

		return fmt.Sprintf("```\n$ %s\n%s\n```", command, stdout)
	})
}

func truncateShellOutput(content string) string {
	if content == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	totalBytes := len(content)

	if totalBytes <= tools.MaxOutputBytes && len(lines) <= tools.MaxOutputLines {
		return content
	}

	headLines := 500
	tailLines := 500
	if len(lines) > headLines+tailLines {
		head := strings.Join(lines[:headLines], "\n")
		tail := strings.Join(lines[len(lines)-tailLines:], "\n")
		return fmt.Sprintf("%s\n\n... (%d lines truncated) ...\n\n%s", head, len(lines)-headLines-tailLines, tail)
	}

	// Byte limit exceeded but line count is fine — just truncate bytes
	if totalBytes > tools.MaxOutputBytes {
		return content[:tools.MaxOutputBytes] + "\n... (output truncated)"
	}

	return content
}
