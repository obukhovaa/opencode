package flow

import (
	"bytes"
	"encoding/json"
	"strings"
)

// OutputMarkdown preserves prose and renders structured results as fenced,
// indented JSON. Optional preview limits never leave an unterminated fence.
func OutputMarkdown(output string, structured bool, maxLines, maxRunes int) string {
	output = strings.TrimSpace(output)
	jsonOutput := false
	if structured {
		var formatted bytes.Buffer
		if json.Indent(&formatted, []byte(output), "", "  ") == nil {
			output = formatted.String()
			jsonOutput = true
		}
	}
	truncated := false
	if lines := strings.Split(output, "\n"); maxLines > 0 && len(lines) > maxLines {
		output = strings.Join(lines[:maxLines], "\n")
		truncated = true
	}
	if runes := []rune(output); maxRunes > 0 && len(runes) > maxRunes {
		output = string(runes[:maxRunes])
		truncated = true
	}
	if jsonOutput {
		output = "```json\n" + output + "\n```"
	}
	if truncated {
		output += "\n\n… Full output: /flow"
	}
	return output
}
