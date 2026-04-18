package permission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	tests := []struct {
		pattern string
		want    string
	}{
		{"~/foo", filepath.Join(home, "foo")},
		{"~/.openai/*", filepath.Join(home, ".openai/*")},
		{"~", home},
		{"/absolute/path", "/absolute/path"},
		{"relative", "relative"},
		{"~other", "~other"}, // not ~/..., leave as-is
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := expandHome(tt.pattern)
			if got != tt.want {
				t.Errorf("expandHome(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchWildcard_HomeTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	// Pattern with ~ should match the expanded absolute path
	if !MatchWildcard("~/.openai/*", filepath.Join(home, ".openai/config.json")) {
		t.Error("~/.openai/* should match absolute path under home")
	}
	if MatchWildcard("~/.openai/*", "/tmp/other") {
		t.Error("~/.openai/* should not match /tmp/other")
	}
	// Pattern ending with /* should also match the directory itself (no trailing slash)
	if !MatchWildcard("~/.openai/*", filepath.Join(home, ".openai")) {
		t.Error("~/.openai/* should match the directory path itself")
	}
}

func TestMatchWildcard_DirSlashStar(t *testing.T) {
	// /foo/* should match /foo (the directory), /foo/bar (a child), but not /foobar
	if !MatchWildcard("/foo/*", "/foo") {
		t.Error("/foo/* should match /foo")
	}
	if !MatchWildcard("/foo/*", "/foo/bar") {
		t.Error("/foo/* should match /foo/bar")
	}
	if MatchWildcard("/foo/*", "/foobar") {
		t.Error("/foo/* should not match /foobar")
	}
}

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

func TestMatchPatternsDeterministic(t *testing.T) {
	// More specific pattern should always win regardless of map iteration order.
	// Run many times to catch non-determinism.
	perms := map[string]any{
		"bash": map[string]any{
			"/proc/*":        "deny",
			"/proc/cpuinfo":  "allow",
		},
	}
	for i := 0; i < 100; i++ {
		got := EvaluateToolPermission("bash", "/proc/cpuinfo", perms, nil)
		if got != ActionAllow {
			t.Fatalf("iteration %d: expected allow for exact match, got %v", i, got)
		}
	}
}

func TestEvaluateReadToolPermission(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		input       string
		agentPerms  map[string]any
		globalPerms map[string]any
		want        Action
	}{
		{
			name:  "default to allow when no perms",
			tool:  "grep",
			input: "/home/user/project",
			want:  ActionAllow,
		},
		{
			name:  "read tool default to allow",
			tool:  "read",
			input: "/home/user/file.go",
			want:  ActionAllow,
		},
		{
			name:  "read category denies path",
			tool:  "grep",
			input: "/proc/1/status",
			agentPerms: map[string]any{
				"read": map[string]any{
					"*":      "allow",
					"/proc/*": "deny",
				},
			},
			want: ActionDeny,
		},
		{
			name:  "read category applies to glob tool",
			tool:  "glob",
			input: "/sys/class",
			agentPerms: map[string]any{
				"read": map[string]any{
					"/sys/*": "deny",
				},
			},
			want: ActionDeny,
		},
		{
			name:  "read category applies to ls tool",
			tool:  "ls",
			input: "/dev",
			agentPerms: map[string]any{
				"read": map[string]any{
					"/dev": "deny",
				},
			},
			want: ActionDeny,
		},
		{
			name:  "specific tool overrides read",
			tool:  "grep",
			input: "/proc/cpuinfo",
			agentPerms: map[string]any{
				"read": map[string]any{
					"/proc/*": "deny",
				},
				"grep": map[string]any{
					"/proc/*": "allow",
				},
			},
			want: ActionAllow,
		},
		{
			name:  "specific tool deny overrides read allow",
			tool:  "grep",
			input: "/tmp/data",
			agentPerms: map[string]any{
				"read": "allow",
				"grep": map[string]any{
					"/tmp/*": "deny",
				},
			},
			want: ActionDeny,
		},
		{
			name:  "read tool uses read perms directly",
			tool:  "read",
			input: "/proc/1/maps",
			agentPerms: map[string]any{
				"read": map[string]any{
					"/proc/*": "deny",
				},
			},
			want: ActionDeny,
		},
		{
			name:  "falls back to wildcard star",
			tool:  "glob",
			input: "/some/path",
			globalPerms: map[string]any{
				"*": "ask",
			},
			want: ActionAsk,
		},
		{
			name:  "agent perms take precedence over global",
			tool:  "grep",
			input: "/proc/loadavg",
			agentPerms: map[string]any{
				"read": map[string]any{
					"/proc/*": "deny",
				},
			},
			globalPerms: map[string]any{
				"read": "allow",
			},
			want: ActionDeny,
		},
		{
			name:  "global read perms used as fallback",
			tool:  "ls",
			input: "/dev/sda",
			globalPerms: map[string]any{
				"read": map[string]any{
					"/dev/*": "ask",
				},
			},
			want: ActionAsk,
		},
		{
			name:  "no specific tool entry falls back to read",
			tool:  "grep",
			input: "/home/user/project",
			agentPerms: map[string]any{
				"read": "allow",
			},
			want: ActionAllow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateReadToolPermission(tt.tool, tt.input, tt.agentPerms, tt.globalPerms)
			if got != tt.want {
				t.Errorf("EvaluateReadToolPermission(%q, %q) = %v, want %v", tt.tool, tt.input, got, tt.want)
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
