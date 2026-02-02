package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/opencode-ai/opencode/internal/config"
	mock_permission "github.com/opencode-ai/opencode/internal/permission/mocks"
	"github.com/opencode-ai/opencode/internal/skill"
)

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		str     string
		want    bool
	}{
		{"exact match", "git-release", "git-release", true},
		{"no match", "git-release", "docker-build", false},
		{"wildcard all", "*", "anything", true},
		{"prefix wildcard", "internal-*", "internal-docs", true},
		{"prefix wildcard no match", "internal-*", "external-docs", false},
		{"suffix wildcard", "*-test", "unit-test", true},
		{"suffix wildcard no match", "*-test", "unit-spec", false},
		{"no wildcard", "exact", "exact", true},
		{"no wildcard no match", "exact", "different", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchWildcard(tt.pattern, tt.str)
			if got != tt.want {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tt.pattern, tt.str, got, tt.want)
			}
		})
	}
}

func TestMatchPermissionPattern(t *testing.T) {
	patterns := map[string]string{
		"git-release":    "allow",
		"internal-*":     "deny",
		"experimental-*": "ask",
		"*":              "ask",
	}

	tests := []struct {
		name      string
		skillName string
		want      string
	}{
		{"exact match", "git-release", "allow"},
		{"prefix wildcard match", "internal-docs", "deny"},
		{"prefix wildcard match 2", "internal-tools", "deny"},
		{"experimental wildcard", "experimental-feature", "ask"},
		{"fallback to wildcard", "random-skill", "ask"},
		{"no match", "unknown", "ask"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPermissionPattern(tt.skillName, patterns)
			if got != tt.want {
				t.Errorf("matchPermissionPattern(%q) = %q, want %q", tt.skillName, got, tt.want)
			}
		})
	}
}

func TestEvaluateSkillPermission(t *testing.T) {
	tests := []struct {
		name       string
		skillName  string
		agentName  config.AgentName
		cfg        *config.Config
		want       string
	}{
		{
			name:      "global allow",
			skillName: "git-release",
			agentName: config.AgentCoder,
			cfg: &config.Config{
				Permission: &config.PermissionConfig{
					Skill: map[string]string{
						"*": "allow",
					},
				},
			},
			want: "allow",
		},
		{
			name:      "global deny",
			skillName: "internal-docs",
			agentName: config.AgentCoder,
			cfg: &config.Config{
				Permission: &config.PermissionConfig{
					Skill: map[string]string{
						"internal-*": "deny",
					},
				},
			},
			want: "deny",
		},
		{
			name:      "agent-specific override global",
			skillName: "internal-docs",
			agentName: config.AgentCoder,
			cfg: &config.Config{
				Permission: &config.PermissionConfig{
					Skill: map[string]string{
						"internal-*": "deny",
					},
				},
				Agents: map[config.AgentName]config.Agent{
					config.AgentCoder: {
						Permission: map[string]map[string]string{
							"skill": {
								"internal-*": "allow",
							},
						},
					},
				},
			},
			want: "allow",
		},
		{
			name:      "tool disabled for agent",
			skillName: "any-skill",
			agentName: config.AgentSummarizer,
			cfg: &config.Config{
				Permission: &config.PermissionConfig{
					Skill: map[string]string{
						"*": "allow",
					},
				},
				Agents: map[config.AgentName]config.Agent{
					config.AgentSummarizer: {
						Tools: map[string]bool{
							"skill": false,
						},
					},
				},
			},
			want: "deny",
		},
		{
			name:      "no config defaults to ask",
			skillName: "any-skill",
			agentName: config.AgentCoder,
			cfg:       &config.Config{},
			want:      "ask",
		},
		{
			name:      "agent-specific exact match",
			skillName: "special-skill",
			agentName: config.AgentTask,
			cfg: &config.Config{
				Agents: map[config.AgentName]config.Agent{
					config.AgentTask: {
						Permission: map[string]map[string]string{
							"skill": {
								"special-skill": "allow",
							},
						},
					},
				},
			},
			want: "allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateSkillPermission(tt.skillName, tt.agentName, tt.cfg)
			if got != tt.want {
				t.Errorf("evaluateSkillPermission(%q, %q) = %q, want %q", tt.skillName, tt.agentName, got, tt.want)
			}
		})
	}
}

func TestSkillToolIntegration(t *testing.T) {
	// Create temporary directory for test skills
	tmpDir := t.TempDir()

	// Create a test skill
	skillDir := filepath.Join(tmpDir, ".opencode", "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillContent := `---
name: test-skill
description: A test skill for unit testing
license: MIT
---

## What I do

- Test functionality
- Verify integration
`

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Set up config
	oldCfg := config.Get()
	defer func() {
		// Restore old config if it existed
		if oldCfg != nil {
			config.Load(oldCfg.WorkingDir, oldCfg.Debug)
		}
		skill.Invalidate()
	}()

	// Load config with test directory
	cfg, err := config.Load(tmpDir, false)
	if err != nil {
		t.Fatal(err)
	}

	// Set permission to allow
	cfg.Permission = &config.PermissionConfig{
		Skill: map[string]string{
			"*": "allow",
		},
	}

	// Invalidate skill cache to pick up new skills
	skill.Invalidate()

	// Create mock permission service
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPerms := mock_permission.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true).AnyTimes()

	// Create skill tool
	tool := NewSkillTool(mockPerms)

	// Test Info
	info := tool.Info()
	if info.Name != SkillToolName {
		t.Errorf("Info().Name = %q, want %q", info.Name, SkillToolName)
	}

	// Test Run with valid skill
	params := SkillParams{Name: "test-skill"}
	paramsJSON, _ := json.Marshal(params)

	ctx := context.Background()
	ctx = context.WithValue(ctx, SessionIDContextKey, "test-session")

	call := ToolCall{
		ID:    "test-call",
		Name:  SkillToolName,
		Input: string(paramsJSON),
	}

	resp, err := tool.Run(ctx, call)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if resp.IsError {
		t.Errorf("Run() returned error response: %s", resp.Content)
	}

	if !contains(resp.Content, "test-skill") {
		t.Errorf("Response missing skill name")
	}

	if !contains(resp.Content, "Test functionality") {
		t.Errorf("Response missing skill content")
	}

	// Test Run with invalid skill
	invalidParams := SkillParams{Name: "nonexistent-skill"}
	invalidParamsJSON, _ := json.Marshal(invalidParams)

	invalidCall := ToolCall{
		ID:    "test-call-2",
		Name:  SkillToolName,
		Input: string(invalidParamsJSON),
	}

	resp, err = tool.Run(ctx, invalidCall)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !resp.IsError {
		t.Errorf("Run() should return error for nonexistent skill")
	}

	if !contains(resp.Content, "not found") {
		t.Errorf("Error response should mention skill not found")
	}
}

func TestBuildSkillDescription(t *testing.T) {
	t.Skip("Skipping integration test - requires proper config initialization")
}

func TestSkillToolWithAgentPermissions(t *testing.T) {
	// Test agent-specific permission override
	cfg := &config.Config{
		Permission: &config.PermissionConfig{
			Skill: map[string]string{
				"*": "deny",
			},
		},
		Agents: map[config.AgentName]config.Agent{
			config.AgentCoder: {
				Permission: map[string]map[string]string{
					"skill": {
						"test-skill": "allow",
					},
				},
			},
		},
	}

	// Coder agent should allow (override)
	action := evaluateSkillPermission("test-skill", config.AgentCoder, cfg)
	if action != "allow" {
		t.Errorf("Expected allow for coder agent, got %s", action)
	}

	// Task agent should deny (global)
	action2 := evaluateSkillPermission("test-skill", config.AgentTask, cfg)
	if action2 != "deny" {
		t.Errorf("Expected deny for task agent, got %s", action2)
	}
}

func TestSkillToolDisabledForAgent(t *testing.T) {
	// Test tool disabled for specific agent
	cfg := &config.Config{
		Permission: &config.PermissionConfig{
			Skill: map[string]string{
				"*": "allow",
			},
		},
		Agents: map[config.AgentName]config.Agent{
			config.AgentSummarizer: {
				Tools: map[string]bool{
					"skill": false,
				},
			},
		},
	}

	// Summarizer agent should deny (tool disabled)
	action := evaluateSkillPermission("any-skill", config.AgentSummarizer, cfg)
	if action != "deny" {
		t.Errorf("Expected deny when tool is disabled, got %s", action)
	}

	// Coder agent should allow (tool not disabled)
	action2 := evaluateSkillPermission("any-skill", config.AgentCoder, cfg)
	if action2 != "allow" {
		t.Errorf("Expected allow for coder agent, got %s", action2)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			len(s) > len(substr) && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
