package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

func toolsConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(ProfileEnv, "")
	t.Cleanup(func() { toolenv.SetConfigured(nil) })
	return home
}

func writeToolsConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadToolsScopeAndPrecedence(t *testing.T) {
	home := toolsConfigHome(t)
	t.Setenv("TOOLS_TEST_PATH", "./team/tools")
	writeToolsConfig(t, home, `{
		"providers":{"local":{"apiKey":"${TOOLS_UNUSED_CREDENTIAL_492A}"}},
		"agents":{"coder":{"model":"invalid-provider.invalid-model"}},
		"sessionProvider":{"type":"mysql"},
		"telemetry":{"enabled":true},
		"data":{"directory":"${TOOLS_UNUSED_DATA_492A}"},
		"cliTools":{"paths":["${TOOLS_TEST_PATH}"],"timeout":"90s","maxOutputBytes":42},
		"toolEnv":{"deny":["TOOLS_PRIVATE_*"]}
	}`)
	ws := t.TempDir()
	writeToolsConfig(t, ws, `{
		"router":{"token":"${TOOLS_UNUSED_ROUTER_492A}"},
		"mcpServers":{"unused":{"headers":{"Authorization":"${TOOLS_UNUSED_MCP_492A}"}}},
		"cliTools":{"timeout":"30s"}
	}`)
	before := Get()
	c, err := LoadTools(ws)
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkingDir != ws || c.CLITools == nil || c.CLITools.Timeout != "30s" || c.CLITools.MaxOutputBytes != 42 || !reflect.DeepEqual(c.CLITools.Paths, []string{"./team/tools"}) {
		t.Fatalf("wrong merged tools config: %+v", c.CLITools)
	}
	if len(c.Agents) != 0 || len(c.Providers) != 0 || len(c.MCPServers) != 0 || c.Data.Directory != "" {
		t.Fatal("unrelated settings loaded")
	}
	if Get() != before {
		t.Fatal("scoped load replaced the runtime singleton")
	}
	filtered := toolenv.Filter([]string{"TOOLS_PRIVATE_KEY=secret", "PUBLIC=ok", "OPENCODE_MYSQL_DSN=secret"}, toolenv.DenyList())
	if !reflect.DeepEqual(filtered, []string{"PUBLIC=ok"}) {
		t.Fatalf("environment filtering lost: %v", filtered)
	}
}

func TestLoadToolsProfile(t *testing.T) {
	home := toolsConfigHome(t)
	writeToolsConfig(t, home, `{"cliTools":{"timeout":"11s"}}`)
	writeToolsConfig(t, filepath.Join(home, ".config", "opencode-piano"), `{"cliTools":{"timeout":"22s"}}`)
	ws := t.TempDir()
	for _, tt := range []struct{ profile, timeout string }{{"", "11s"}, {"piano", "22s"}, {"missing", "11s"}} {
		t.Run(tt.profile, func(t *testing.T) {
			t.Setenv(ProfileEnv, tt.profile)
			c, err := LoadTools(ws)
			if err != nil || c.CLITools == nil || c.CLITools.Timeout != tt.timeout {
				t.Fatalf("profile %q: config=%+v err=%v", tt.profile, c, err)
			}
		})
	}
}

func TestLoadToolsRejectsInvalidRelevantConfig(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"required cliTools env", `{"cliTools":{"paths":["secret-prefix/${TOOLS_REQUIRED_UNSET_492A}"]}}`, "TOOLS_REQUIRED_UNSET_492A"},
		{"required toolEnv env", `{"toolEnv":{"deny":["${TOOLS_REQUIRED_UNSET_492A}"]}}`, "TOOLS_REQUIRED_UNSET_492A"},
		{"invalid type", `{"toolEnv":{"deny":{"bad":"secret-prefix"}}}`, "failed to unmarshal tools config"},
		{"malformed JSON", `{"providers":`, "failed to read"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := toolsConfigHome(t)
			writeToolsConfig(t, home, tt.body)
			_, err := LoadTools(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "secret-prefix") {
				t.Fatalf("wrong or unsafe error: %v", err)
			}
		})
	}
}
