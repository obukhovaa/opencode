package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

func TestExpandConfigString(t *testing.T) {
	t.Setenv("Config_Test", "value")
	t.Setenv("CONFIG_EMPTY", "")
	t.Setenv("CONFIG_NESTED", "${Config_Test}")
	t.Setenv("CONFIG_SPECIAL", "quote\"\\\n${literal} $(touch ignored)")
	t.Setenv("agent", "must-not-replace-context")
	os.Unsetenv("OPENCODE_TEST_UNSET_3E8F")
	tests := []struct {
		name, input, want string
		wantErr           bool
	}{
		{"multiple", "https://${Config_Test}/${Config_Test}", "https://value/value", false},
		{"case sensitive", "${CONFIG_TEST:-default}", "default", false},
		{"unset fallback", "${OPENCODE_TEST_UNSET_3E8F:-https://example.test/a:b}", "https://example.test/a:b", false},
		{"empty fallback", "${CONFIG_EMPTY:-default}", "default", false},
		{"empty is set", "before${CONFIG_EMPTY}after", "beforeafter", false},
		{"empty default", "${OPENCODE_TEST_UNSET_3E8F:-}", "", false},
		{"required", "secret-prefix/${OPENCODE_TEST_UNSET_3E8F}", "", true},
		{"escape", "$${Config_Test} $${OPENCODE_TEST_UNSET_3E8F}", "${Config_Test} ${OPENCODE_TEST_UNSET_3E8F}", false},
		{"one pass", "${CONFIG_NESTED}", "${Config_Test}", false},
		{"special data", "${CONFIG_SPECIAL}", "quote\"\\\n${literal} $(touch ignored)", false},
		{"shell and context", "$Config_Test ${agent} ${flow.step} ${env.Config_Test} ${args.x}", "$Config_Test ${agent} ${flow.step} ${env.Config_Test} ${args.x}", false},
		{"unsupported", "${1BAD} ${BAD-NAME} ${} ${Config_Test:-${CONFIG_EMPTY}}", "${1BAD} ${BAD-NAME} ${} ${Config_Test:-${CONFIG_EMPTY}}", false},
		{"unterminated", "a${Config_Test", "a${Config_Test", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandConfigString(tt.input)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "OPENCODE_TEST_UNSET_3E8F") || strings.Contains(err.Error(), "secret-prefix") {
					t.Fatalf("unsafe or missing error: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestConfigEnvViperRoundTrip(t *testing.T) {
	t.Setenv("CONFIG_KEY", "quoted\"\\\nsecret")
	t.Setenv("CONFIG_MODEL", "kimi.kimi-k3.1-pro-highspeed")
	t.Setenv("CONFIG_TOKENS", "2048")
	t.Setenv("CONFIG_BOOL", "false")
	v := viper.NewWithOptions(configDecoderOption())
	v.SetConfigType("json")
	body := `{"providers":{"kimi":{"apiKey":"${CONFIG_KEY}","baseURL":"https://${CONFIG_HOST:-example.test}/anthropic"}},"agents":{"coder":{"model":"${CONFIG_MODEL}","maxTokens":"${CONFIG_TOKENS}"}},"mcpServers":{"My_Server":{"url":"https://example.test","headers":{"X-Token":"${CONFIG_KEY}"},"env":["KEY=${CONFIG_KEY}"]}},"autoCompact":"${CONFIG_BOOL}","permission":{"skill":{"${CONFIG_KEY}":"allow"}}}`
	if err := v.ReadConfig(strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatal(err)
	}
	if c.Providers[models.ProviderKimi].APIKey != os.Getenv("CONFIG_KEY") || c.Agents[AgentCoder].Model != models.KimiK31ProHighspeed || c.Agents[AgentCoder].MaxTokens != 2048 || c.AutoCompact {
		t.Fatal("expanded fields did not survive Viper")
	}
	if c.MCPServers["my_server"].Headers["x-token"] != os.Getenv("CONFIG_KEY") || c.MCPServers["my_server"].Env[0] != "KEY="+os.Getenv("CONFIG_KEY") {
		t.Fatal("nested maps or arrays did not expand")
	}
	if c.Permission.Skill["${config_key}"] != "allow" {
		t.Fatal("a map key was expanded")
	}
}

func TestConfigEnvLeavesHookCommandsToTheHookShell(t *testing.T) {
	os.Unsetenv("OPENCODE_PROJECT_DIR")
	t.Setenv("CONFIG_KEY", "secret-marker")
	v := viper.NewWithOptions(configDecoderOption())
	v.SetConfigType("json")
	body := `{"hooks":{"PreToolUse":[{"matcher":"bash","hooks":[{"type":"command","command":"${OPENCODE_PROJECT_DIR}/.opencode/hooks/check.sh ${CONFIG_KEY}"}]}]},"providers":{"kimi":{"apiKey":"${CONFIG_KEY}"}}}`
	if err := v.ReadConfig(strings.NewReader(body)); err != nil {
		t.Fatalf("a hook-only variable must not fail the load: %v", err)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatal(err)
	}
	var command string
	for _, groups := range c.Hooks {
		command = groups[0].Hooks[0].Command
	}
	if command != "${OPENCODE_PROJECT_DIR}/.opencode/hooks/check.sh ${CONFIG_KEY}" {
		t.Fatalf("hook command was expanded at load: %q", command)
	}
	if c.Providers[models.ProviderKimi].APIKey != "secret-marker" {
		t.Fatal("expansion outside hooks stopped")
	}
}

func TestConfigEnvLoad(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("CONFIG_KEY", "private-config-key")
	t.Setenv("CONFIG_HOST", "global.test")
	t.Setenv("CONFIG_LIMIT", "7")
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	write := func(dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, `{"providers":{"kimi":{"apiKey":"${CONFIG_KEY}","baseURL":"https://${CONFIG_HOST}/anthropic"}},"maxTurns":"${CONFIG_LIMIT}","agents":{"coder":{"model":"kimi.kimi-k3.1-pro"}}}`)
	write(project, `{"providers":{"kimi":{"baseURL":"https://${PROJECT_HOST:-local.test}/anthropic"}},"autoCompact":"${COMPACT_ENABLED:-false}"}`)
	viper.Reset()
	Reset()
	t.Cleanup(func() { viper.Reset(); Reset() })
	c, err := Load(project, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers[models.ProviderKimi].APIKey != "private-config-key" || c.Providers[models.ProviderKimi].BaseURL != "https://local.test/anthropic" || c.MaxTurns != 7 || c.AutoCompact {
		t.Fatal("file merge or weak scalar typing changed")
	}
	if c.Agents[AgentCoder].Model != models.KimiK31Pro || c.Agents[AgentCoder].ReasoningEffort != "max" {
		t.Fatal("model/effort was not resolved after expansion")
	}
	// Provider defaults must see the expanded credential before Unmarshal.
	if c.Agents[AgentExplorer].Model != models.KimiK3 {
		t.Fatal("expanded credential did not enable Kimi defaults")
	}
	if err := UpdateTheme("test-theme"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-config-key") || !strings.Contains(string(data), "${CONFIG_KEY}") || !strings.Contains(string(data), "${CONFIG_LIMIT}") {
		t.Fatal("writeback materialized environment values")
	}
	// A missing project reference must fail rather than silently use global config.
	write(project, `{"providers":{"kimi":{"apiKey":"${OPENCODE_TEST_UNSET_3E8F}"}}}`)
	Reset()
	viper.Reset()
	if _, err := Load(project, false); err == nil || !strings.Contains(err.Error(), "OPENCODE_TEST_UNSET_3E8F") {
		t.Fatalf("missing project reference: %v", err)
	}
	// A failed load must not cache partially initialized configuration.
	write(project, `{}`)
	if _, err := Load(project, false); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCfgFilePreservesReferences(t *testing.T) {
	t.Setenv("CONFIG_KEY", "secret-marker")
	t.Setenv("CONFIG_LIMIT", "9")
	t.Setenv("CONFIG_BOOL", "true")
	body := `{"providers":{"kimi":{"apiKey":"${CONFIG_KEY}","baseURL":"${CONFIG_URL:-https://example.test}"}},"maxTurns":"${CONFIG_LIMIT}","autoCompact":"${CONFIG_BOOL}","future":{"big":9007199254740993,"secret":"${CONFIG_KEY}"},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo ${CONFIG_KEY}"}]}]},"router":{"channels":{"telegram":{"bots":[{"id":"one","token":"${CONFIG_KEY}","future":true},{"id":"two","token":"${CONFIG_KEY}"}]}}}}`
	file, _ := withTestConfigFile(t, body)
	if err := UpdateCfgFile(func(c *Config) {
		c.Router.Channels.Telegram.Bots[0].GroupsEnabled = true
		c.Router.Channels.Telegram.Bots = append(c.Router.Channels.Telegram.Bots, bridge.TelegramIdentity{ID: "three", Token: "new-token"})
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-marker") || !strings.Contains(string(data), "9007199254740993") || !strings.Contains(string(data), "PreToolUse") {
		t.Fatal("writeback lost raw config or exposed a secret")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	bots := doc["router"].(map[string]any)["channels"].(map[string]any)["telegram"].(map[string]any)["bots"].([]any)
	first := bots[0].(map[string]any)
	if first["token"] != "${CONFIG_KEY}" || first["groupsEnabled"] != true || first["future"] != true || bots[1].(map[string]any)["token"] != "${CONFIG_KEY}" {
		t.Fatal("nested edited array lost placeholders")
	}
	if err := UpdateCfgFile(func(c *Config) {
		c.AutoCompact = false
		c.MaxTurns = 0
		c.Providers[models.ProviderKimi] = Provider{APIKey: "replacement"}
		c.Router.Channels.Telegram.Bots = c.Router.Channels.Telegram.Bots[1:]
	}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["autoCompact"] != false || doc["maxTurns"] != float64(0) {
		t.Fatal("zero-valued updates lost")
	}
	bots = doc["router"].(map[string]any)["channels"].(map[string]any)["telegram"].(map[string]any)["bots"].([]any)
	if bots[0].(map[string]any)["token"] != "${CONFIG_KEY}" {
		t.Fatal("removing an identity materialized another identity's token")
	}
}

func TestConfigWritebackMovedIdentityAndCase(t *testing.T) {
	t.Setenv("CONFIG_FIRST_TOKEN", "first-secret")
	t.Setenv("CONFIG_SECOND_TOKEN", "second-secret")
	body := `{"TUI":{"Theme":"old"},"router":{"channels":{"telegram":{"bots":[{"id":"first","token":"${CONFIG_FIRST_TOKEN}"},{"id":"second","token":"${CONFIG_SECOND_TOKEN}","unknown":true}]}}}}`
	file, _ := withTestConfigFile(t, body)
	if err := UpdateCfgFile(func(c *Config) {
		c.TUI.Theme = "new"
		c.Router.Channels.Telegram.Bots = c.Router.Channels.Telegram.Bots[1:]
		c.Router.Channels.Telegram.Bots[0].GroupsEnabled = true
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "second-secret") {
		t.Fatal("moving and editing an identity exposed a token")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, duplicate := doc["tui"]; duplicate || doc["TUI"].(map[string]any)["Theme"] != "new" {
		t.Fatal("case-insensitive struct field edit did not preserve spelling")
	}
	bots := doc["router"].(map[string]any)["channels"].(map[string]any)["telegram"].(map[string]any)["bots"].([]any)
	if bots[0].(map[string]any)["token"] != "${CONFIG_SECOND_TOKEN}" || bots[0].(map[string]any)["unknown"] != true {
		t.Fatal("moving and editing an identity lost raw fields")
	}
}

func TestConfigEnvExplicitOverrideAndTypeError(t *testing.T) {
	clearProviderEnv(t)
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("MOONSHOT_API_KEY", "test-key")
	t.Setenv("CONFIG_LIMIT", "3")
	t.Setenv("OPENCODE_MAXTURNS", "8")
	file := filepath.Join(project, ".opencode.json")
	if err := os.WriteFile(file, []byte(`{"maxTurns":"${CONFIG_LIMIT}"}`), 0600); err != nil {
		t.Fatal(err)
	}
	Reset()
	viper.Reset()
	t.Cleanup(func() { Reset(); viper.Reset() })
	c, err := Load(project, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxTurns != 8 {
		t.Fatalf("explicit environment override lost: %d", c.MaxTurns)
	}
	t.Setenv("OPENCODE_MAXTURNS", "")
	t.Setenv("CONFIG_LIMIT", "secret-type-error-marker")
	Reset()
	viper.Reset()
	if _, err := Load(project, false); err == nil || strings.Contains(err.Error(), "secret-type-error-marker") {
		t.Fatalf("unsafe type diagnostic: %v", err)
	}
	t.Setenv("CONFIG_LIMIT", "5")
	if c, err := Load(project, false); err != nil || c.MaxTurns != 5 {
		t.Fatal("failed load cached partially expanded config")
	}
}
