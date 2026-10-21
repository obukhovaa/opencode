package provider

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestMemoryNativeSerializationAndFallback(t *testing.T) {
	tool := tools.NewMemoryTool(&config.Config{}, nil, nil)
	for _, model := range []string{"claude-opus-4-6", "kimi-k2"} {
		client := &anthropicClient{providerOptions: providerClientOptions{model: models.Model{APIModel: model}}}
		result := client.convertTools(sessionCtx("memory-session"), []tools.BaseTool{tool})
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if model == "claude-opus-4-6" {
			if !strings.Contains(string(data), `"type":"memory_20250818"`) || strings.Contains(string(data), "input_schema") {
				t.Fatalf("%s", data)
			}
			if result[0].OfMemoryTool20250818.CacheControl.Type == "" {
				t.Fatal("missing cache breakpoint")
			}
			if _, ok := declaredToolNames(result)["memory"]; !ok {
				t.Fatal("missing declared name")
			}
		} else if result[0].OfTool == nil || result[0].OfTool.Name != "memory" {
			t.Fatalf("%s", data)
		}
	}
}

func TestNativeMemoryDeferral(t *testing.T) {
	client := &anthropicClient{providerOptions: providerClientOptions{model: models.Model{APIModel: "claude-sonnet-4-6", SupportsToolSearch: true}}}
	tool := tools.WrapDeferred(tools.NewMemoryTool(&config.Config{}, nil, nil), &atomic.Int64{})
	result := client.convertTools(sessionCtx("memory-session"), []tools.BaseTool{tool})
	if len(result) != 2 || result[0].OfMemoryTool20250818 == nil || !result[0].OfMemoryTool20250818.DeferLoading.Value || result[1].OfToolSearchToolRegex20251119 == nil {
		t.Fatalf("%+v", result)
	}
}
