package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServicePolicyWritebackPreservesEnvAndAgentSettings(t *testing.T) {
	t.Setenv("SERVICE_SECRET", "do-not-persist")
	data := []byte(`{"mcpServers":{"svc":{"type":"http","url":"${SERVICE_SECRET}"}},"agents":{"custom":{"model":"provider.custom","maxTokens":12345,"tools":{"existing":true}}}}`)
	_, document, err := editConfigFile(data, func(c *Config) {
		a := c.Agents["custom"]
		a.Tools["svc_*"] = false
		a.DeferredTools = map[string]bool{"svc_*": true}
		c.Agents["custom"] = a
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-persist") || !strings.Contains(string(encoded), "${SERVICE_SECRET}") {
		t.Fatal("environment placeholder changed")
	}
	var round Config
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	a := round.Agents["custom"]
	if a.Model != "provider.custom" || a.MaxTokens != 12345 || a.Tools["svc_*"] || !a.Tools["existing"] || !a.DeferredTools["svc_*"] {
		t.Fatalf("bad round trip: %+v", a)
	}
}
