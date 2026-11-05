package config

// DynamicFlow is opt-in and is shared by JSON settings and agent frontmatter.
type DynamicFlow struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	FlowComplexity string `json:"flowComplexity,omitempty" yaml:"flowComplexity,omitempty"`
}

type FlowLimits struct{ MaxSteps, MaxParallel, MaxTurns, MaxIterations int }

// DynamicFlowLimits deliberately accepts exact spellings, matching JSON Schema.
func DynamicFlowLimits(size string) (FlowLimits, bool) {
	switch size {
	case "XS":
		return FlowLimits{3, 1, 20, 1}, true
	case "S":
		return FlowLimits{5, 2, 40, 1}, true
	case "M":
		return FlowLimits{8, 3, 80, 2}, true
	case "L":
		return FlowLimits{12, 4, 120, 3}, true
	case "XL":
		return FlowLimits{18, 6, 180, 4}, true
	case "XXL":
		return FlowLimits{24, 8, 240, 6}, true
	}
	return FlowLimits{}, false
}

func ValidDynamicFlow(df *DynamicFlow) bool {
	if df == nil || df.FlowComplexity == "" {
		return true
	}
	_, ok := DynamicFlowLimits(df.FlowComplexity)
	return ok
}
