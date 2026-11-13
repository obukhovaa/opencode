package flow

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputMarkdown(t *testing.T) {
	raw := `{"findings":"All checks passed","items":[1,2]}`
	formatted := OutputMarkdown(raw, true, 0, 0)
	if !strings.HasPrefix(formatted, "```json\n{\n  ") || !strings.HasSuffix(formatted, "\n```") {
		t.Fatalf("not formatted JSON: %s", formatted)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(formatted, "```json\n"), "\n```")
	if !json.Valid([]byte(body)) {
		t.Fatal("formatted output invalid")
	}
	for _, raw := range []string{"**Summary**\n\n- done", "{not valid json}"} {
		if got := OutputMarkdown(raw, false, 0, 0); got != raw {
			t.Fatalf("prose changed: %q", got)
		}
	}
	long := `{"message":"` + strings.Repeat("日本語", 100) + `"}`
	preview := OutputMarkdown(long, true, 2, 40)
	if !utf8.ValidString(preview) || !strings.Contains(preview, "\n```\n\n… Full output: /flow") {
		t.Fatalf("bad truncated JSON: %s", preview)
	}
}
