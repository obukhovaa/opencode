// Command config-env-e2e exercises the real config/Viper/provider/writeback
// pipeline in the hermetic sandboxes prepared by scripts/test/config_env.sh.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/message"
)

func run() error {
	received := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Thinking  struct {
				Type string `json:"type"`
			} `json:"thinking"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		valid := json.NewDecoder(r.Body).Decode(&body) == nil && r.URL.Path == "/anthropic/v1/messages" && r.Header.Get("Authorization") == "Bearer "+os.Getenv("E2E_KEY") && body.Model == strings.TrimPrefix(os.Getenv("E2E_MODEL"), "kimi.") && body.MaxTokens == 2048 && body.Thinking.Type == "adaptive" && body.OutputConfig.Effort == "max"
		select {
		case received <- valid:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"e2e","type":"message","role":"assistant","model":"kimi-k3.1-pro","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer server.Close()
	if err := os.Setenv("E2E_KIMI_BASE", server.URL+"/anthropic"); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	c, err := config.Load(cwd, false)
	if err != nil {
		return err
	}
	p := c.Providers[models.ProviderKimi]
	agent := c.Agents[config.AgentCoder]
	if p.APIKey != os.Getenv("E2E_KEY") || p.BaseURL != server.URL+"/anthropic" || c.MaxTurns != 12 || c.AutoCompact || agent.Model != models.ModelID(os.Getenv("E2E_MODEL")) || agent.MaxTokens != 2048 || agent.ReasoningEffort != "max" {
		return fmt.Errorf("configuration expansion, precedence or scalar conversion failed")
	}
	if c.MCPServers["example"].Headers["x-secret"] != os.Getenv("E2E_DATA") || c.MCPServers["example"].Env[0] != "TOKEN="+os.Getenv("E2E_DATA") || c.ContextPaths[0] != "AGENTS.${agent}.${flow.step}.md" || c.Shell.Args[0] != "${E2E_LITERAL}" {
		return fmt.Errorf("nested strings or literal context syntax changed")
	}
	client, err := provider.NewProvider(models.ProviderKimi, provider.WithModel(models.SupportedModels[agent.Model]), provider.WithAPIKey(p.APIKey), provider.WithBaseURL(p.BaseURL), provider.WithMaxTokens(agent.MaxTokens), provider.WithSystemMessage("You are a concise assistant."), provider.WithAnthropicOptions(provider.WithAnthropicReasoningEffort(agent.ReasoningEffort)))
	if err != nil {
		return fmt.Errorf("native provider construction failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := client.SendMessages(ctx, []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}, nil)
	if err != nil || response == nil || response.Content != "OK" {
		return fmt.Errorf("native provider request failed")
	}
	select {
	case valid := <-received:
		if !valid {
			return fmt.Errorf("native provider sent incorrect model, auth, thinking or limits")
		}
	default:
		return fmt.Errorf("native request was not received")
	}
	if err := config.UpdateTheme("e2e-theme"); err != nil {
		return err
	}
	if err := config.UpdateCfgFile(func(c *config.Config) { c.AutoCompact = false; c.MaxTurns = 0 }); err != nil {
		return err
	}
	fmt.Println("PASS config/Viper/native Kimi/writeback")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
