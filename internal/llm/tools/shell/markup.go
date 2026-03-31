package shell

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	// MarkupDefaultTimeout is the timeout for shell markup command execution (2 minutes).
	MarkupDefaultTimeout = 2 * 60 * 1000
	// MarkupMaxOutputBytes is the max output size for shell markup commands.
	MarkupMaxOutputBytes = 50 * 1024
	// MarkupMaxOutputLines is the max line count for shell markup commands.
	MarkupMaxOutputLines = 2000
)

var shellMarkupRegex = regexp.MustCompile("!`([^`]+)`")

// ExpandMarkup finds all !`command` blocks in template, executes each
// command via the persistent shell, and replaces the block with the command's
// output. On error or non-zero exit, error text is inlined rather than failing.
func ExpandMarkup(ctx context.Context, template string, cwd string) string {
	if !strings.Contains(template, "!`") {
		return template
	}

	return shellMarkupRegex.ReplaceAllStringFunc(template, func(match string) string {
		submatches := shellMarkupRegex.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		command := submatches[1]

		sh := GetPersistentShell(cwd)
		if sh == nil {
			logging.Warn("ExpandMarkup: failed to get shell", "command", command)
			return fmt.Sprintf("[shell unavailable: %s]", command)
		}

		stdout, stderr, exitCode, _, err := sh.Exec(ctx, command, MarkupDefaultTimeout)
		if err != nil {
			logging.Warn("ExpandMarkup: exec error", "command", command, "error", err)
			return fmt.Sprintf("[command error: %s]\n%s", command, err.Error())
		}

		stdout = truncateMarkupOutput(stdout)
		stderr = truncateMarkupOutput(stderr)

		if exitCode != 0 {
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

func truncateMarkupOutput(content string) string {
	if content == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	totalBytes := len(content)

	if totalBytes <= MarkupMaxOutputBytes && len(lines) <= MarkupMaxOutputLines {
		return content
	}

	headLines := 500
	tailLines := 500
	if len(lines) > headLines+tailLines {
		head := strings.Join(lines[:headLines], "\n")
		tail := strings.Join(lines[len(lines)-tailLines:], "\n")
		return fmt.Sprintf("%s\n\n... (%d lines truncated) ...\n\n%s", head, len(lines)-headLines-tailLines, tail)
	}

	if totalBytes > MarkupMaxOutputBytes {
		return content[:MarkupMaxOutputBytes] + "\n... (output truncated)"
	}

	return content
}
