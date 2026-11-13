package config

import (
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
