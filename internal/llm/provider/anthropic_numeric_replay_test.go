package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/message"
)

func TestAnthropicToolInputPreservesNumericLexemes(t *testing.T) {
	client := &anthropicClient{}
	input := `{"id":9007199254740993,"limit":10,"nested":[{"id":18446744073709551615,"ratio":1.0000000000000001}]}`
	messages, _ := client.convertMessages([]message.Message{{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "call", Name: "fixture_detail", Input: input, Finished: true},
	}}}, nil)
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"9007199254740993", "18446744073709551615", "1.0000000000000001"} {
		if !strings.Contains(string(raw), number) {
			t.Fatalf("number %s changed in tool history: %s", number, raw)
		}
	}
	for path, want := range map[string]string{
		"0.content.0.input.id": "9007199254740993", "0.content.0.input.limit": "10",
		"0.content.0.input.nested.0.id": "18446744073709551615", "0.content.0.input.nested.0.ratio": "1.0000000000000001",
	} {
		got := gjson.GetBytes(raw, path)
		if got.Type != gjson.Number || got.Raw != want {
			t.Fatalf("wrong numeric type/value at %s: %s", path, raw)
		}
	}
	for _, bad := range []string{`{} {}`, `{"id":1} false`, `[]`, `"value"`} {
		if _, err := decodeAnthropicToolInput(bad); err == nil {
			t.Fatalf("invalid object accepted: %s", bad)
		}
	}
}
