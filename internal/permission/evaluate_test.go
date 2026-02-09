package permission

import "testing"

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		pattern string
		str     string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"git *", "git status", true},
		{"git *", "git", false},
		{"git *", "npm install", false},
		{"*.env", "production.env", true},
		{"*.env", "production.env.bak", false},
		{"*.env.*", "app.env.local", true},
		{"internal-*", "internal-docs", true},
		{"internal-*", "external-docs", false},
		{"exact", "exact", true},
		{"exact", "nope", false},
		{"src/**/*.go", "src/foo/bar.go", true},
		{"rm *", "rm -rf /", true},
		{"rm *", "rmdir foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.str, func(t *testing.T) {
			got := MatchWildcard(tt.pattern, tt.str)
			if got != tt.want {
				t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tt.pattern, tt.str, got, tt.want)
			}
		})
	}
}

func TestEvaluateToolPermission(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		input       string
		agentPerms  map[string]any
		globalPerms map[string]any
		want        Action
	}{
		{
			name:  "simple allow",
			tool:  "bash",
			input: "git status",
			agentPerms: map[string]any{
				"bash": "allow",
			},
			want: ActionAllow,
		},
		{
			name:  "granular bash patterns",
			tool:  "bash",
			input: "git status",
			agentPerms: map[string]any{
				"bash": map[string]any{
					"*":     "ask",
					"git *": "allow",
				},
			},
			want: ActionAllow,
		},
		{
			name:  "granular bash deny rm",
			tool:  "bash",
			input: "rm -rf /",
			agentPerms: map[string]any{
				"bash": map[string]any{
					"*":    "allow",
					"rm *": "deny",
				},
			},
			want: ActionDeny,
		},
		{
			name:  "agent overrides global",
			tool:  "skill",
			input: "my-skill",
			agentPerms: map[string]any{
				"skill": "allow",
			},
			globalPerms: map[string]any{
				"skill": "deny",
			},
			want: ActionAllow,
		},
		{
			name:  "fallback to global",
			tool:  "edit",
			input: "src/main.go",
			globalPerms: map[string]any{
				"edit": "deny",
			},
			want: ActionDeny,
		},
		{
			name:  "default to ask",
			tool:  "bash",
			input: "make build",
			want:  ActionAsk,
		},
		{
			name:  "global wildcard",
			tool:  "bash",
			input: "anything",
			globalPerms: map[string]any{
				"*": "allow",
			},
			want: ActionAllow,
		},
		{
			name:  "granular skill patterns",
			tool:  "skill",
			input: "internal-docs",
			agentPerms: map[string]any{
				"skill": map[string]any{
					"*":          "deny",
					"internal-*": "allow",
				},
			},
			want: ActionAllow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateToolPermission(tt.tool, tt.input, tt.agentPerms, tt.globalPerms)
			if got != tt.want {
				t.Errorf("EvaluateToolPermission(%q, %q) = %v, want %v", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestIsToolEnabled(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		config map[string]bool
		want   bool
	}{
		{"nil config", "bash", nil, true},
		{"explicit true", "bash", map[string]bool{"bash": true}, true},
		{"explicit false", "bash", map[string]bool{"bash": false}, false},
		{"wildcard match", "mymcp_list", map[string]bool{"mymcp_*": false}, false},
		{"not in config", "edit", map[string]bool{"bash": false}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsToolEnabled(tt.tool, tt.config)
			if got != tt.want {
				t.Errorf("IsToolEnabled(%q) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}
