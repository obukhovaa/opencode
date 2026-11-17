package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestDynamicFlowViperRoundTrip(t *testing.T) {
	v := viper.New()
	v.SetConfigType("json")
	if err := v.ReadConfig(strings.NewReader(`{"agents":{"ReviewAgent":{"dynamicFlow":{"enabled":true,"flowComplexity":"XL"}},"coder":{"dynamicFlow":{"enabled":false}}}}`)); err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatal(err)
	}
	if got := c.Agents["reviewagent"].DynamicFlow; got == nil || !got.Enabled || got.FlowComplexity != "XL" {
		t.Fatalf("Viper lost dynamicFlow: %+v", got)
	}
	if c.Agents["coder"].DynamicFlow.Enabled {
		t.Fatal("disabled setting lost")
	}
}
func TestDynamicFlowLimits(t *testing.T) {
	for _, size := range []string{"XS", "S", "M", "L", "XL", "XXL"} {
		t.Run(size, func(t *testing.T) {
			l, ok := DynamicFlowLimits(size)
			if !ok || l.MaxSteps < 1 || l.MaxParallel < 1 || l.MaxTurns < 1 || l.MaxIterations < 1 {
				t.Fatalf("invalid limit %+v", l)
			}
		})
	}
	for _, size := range []string{"xs", "auto", " M", "XXXL"} {
		if _, ok := DynamicFlowLimits(size); ok {
			t.Fatalf("accepted %q", size)
		}
	}
}

func TestNamedFlowPolicyViperAndJSONRoundTrip(t *testing.T) {
	for _, value := range []string{"", `,"namedFlows":[]`, `,"namedFlows":["Composer/Inspect"]`} {
		t.Run(value, func(t *testing.T) {
			data := `{"agents":{"ReviewAgent":{"dynamicFlow":{"enabled":true` + value + `}}}}`
			v := viper.New()
			v.SetConfigType("json")
			if err := v.ReadConfig(strings.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			var cfg Config
			if err := v.Unmarshal(&cfg); err != nil {
				t.Fatal(err)
			}
			policy := cfg.Agents["reviewagent"].DynamicFlow
			if policy == nil || (policy.NamedFlows == nil) != (value == "") {
				t.Fatalf("lost omission/empty distinction: %+v", policy)
			}
			if policy.AllowsNamedFlow("unlisted") != (value == "") {
				t.Fatal("unexpected unlisted permission")
			}
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var round Config
			if err = json.Unmarshal(encoded, &round); err != nil {
				t.Fatal(err)
			}
			got := round.Agents["reviewagent"].DynamicFlow
			if (got.NamedFlows == nil) != (policy.NamedFlows == nil) || got.AllowsNamedFlow("Composer/Inspect") != policy.AllowsNamedFlow("Composer/Inspect") {
				t.Fatalf("policy changed on roundtrip: %s", encoded)
			}
			if value != "" && strings.Contains(value, "Composer") && got.AllowsNamedFlow("composer/inspect") {
				t.Fatal("flow IDs must remain case sensitive")
			}
		})
	}
}
func TestDynamicFlowNamedIDsValidation(t *testing.T) {
	for _, tc := range []struct {
		ids   []string
		valid bool
	}{
		{[]string{}, true}, {[]string{"composer/inspect"}, true}, {[]string{""}, false}, {[]string{"  "}, true}, {[]string{" name"}, true}, {[]string{"name "}, true}, {[]string{"name", "name"}, false},
	} {
		if ValidDynamicFlow(&DynamicFlow{Enabled: true, NamedFlows: &tc.ids}) != tc.valid {
			t.Errorf("unexpected validation for %q", tc.ids)
		}
	}
}
