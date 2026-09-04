package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRegistry implements agentregistry.Registry for testing.
type mockRegistry struct {
	agents      map[string]agentregistry.AgentInfo
	globalPerms map[string]any
}

func (r *mockRegistry) Get(id string) (agentregistry.AgentInfo, bool) {
	a, ok := r.agents[id]
	return a, ok
}

func (r *mockRegistry) List() []agentregistry.AgentInfo { return nil }

func (r *mockRegistry) ListByMode(config.AgentMode) []agentregistry.AgentInfo { return nil }

func (r *mockRegistry) EvaluatePermission(agentID, toolName, input string) permission.Action {
	a, ok := r.agents[agentID]
	if !ok {
		return permission.ActionAsk
	}
	if !permission.IsToolEnabled(toolName, a.Tools) {
		return permission.ActionDeny
	}
	return permission.EvaluateToolPermission(toolName, input, a.Permission, r.globalPerms)
}

func (r *mockRegistry) EvaluateReadPermission(agentID, toolName, input string) permission.Action {
	a, ok := r.agents[agentID]
	if !ok {
		return permission.ActionAllow
	}
	if !permission.IsToolEnabled(toolName, a.Tools) {
		return permission.ActionDeny
	}
	return permission.EvaluateReadToolPermission(toolName, input, a.Permission, r.globalPerms)
}

func (r *mockRegistry) ReadDenyPatterns(agentID, toolName string) []string {
	a, ok := r.agents[agentID]
	if !ok {
		return permission.ReadDenyPatterns(toolName, nil, r.globalPerms)
	}
	return permission.ReadDenyPatterns(toolName, a.Permission, r.globalPerms)
}

func (r *mockRegistry) IsToolEnabled(agentID, toolName string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return true
	}
	return permission.IsToolEnabled(toolName, a.Tools)
}

func (r *mockRegistry) IsToolExplicitlyEnabled(agentID, toolName string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return false
	}
	if enabled, ok := a.Tools[toolName]; ok {
		return enabled
	}
	return false
}

func (r *mockRegistry) HasTools(agentID string) bool { return true }

func (r *mockRegistry) GlobalPermissions() map[string]any { return r.globalPerms }

// setupSkillDir creates a skill directory with a SKILL.md file and configures
// the skill registry to discover it.
func setupSkillDir(t *testing.T, tmpDir string, skillName, content string) {
	t.Helper()
	skillDir := filepath.Join(tmpDir, ".opencode", "skills", skillName)
	err := os.MkdirAll(skillDir, 0755)
	require.NoError(t, err)

	skillContent := "---\nname: " + skillName + "\ndescription: Test skill\n---\n\n" + content
	err = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644)
	require.NoError(t, err)
}

func TestAppendPreloadedSkills(t *testing.T) {
	t.Run("no skills configured", func(t *testing.T) {
		reg := &mockRegistry{
			agents: map[string]agentregistry.AgentInfo{
				"test-agent": {ID: "test-agent"},
			},
		}
		result := appendPreloadedSkills("test-agent", reg)
		assert.Empty(t, result)
	})

	t.Run("agent not found", func(t *testing.T) {
		reg := &mockRegistry{agents: map[string]agentregistry.AgentInfo{}}
		result := appendPreloadedSkills("nonexistent", reg)
		assert.Empty(t, result)
	})

	// Tests that need real skill files share a single config/skill cycle.
	t.Run("with skill files", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupSkillDir(t, tmpDir, "my-skill", "This is my skill content.")
		setupSkillDir(t, tmpDir, "ask-skill", "Ask skill content.")
		setupSkillDir(t, tmpDir, "denied-skill", "Denied content.")
		setupSkillDir(t, tmpDir, "alpha-skill", "Alpha content.")
		setupSkillDir(t, tmpDir, "beta-skill", "Beta content.")
		setupSkillDir(t, tmpDir, "default-skill", "Default permission content.")

		config.Reset()
		_, err := config.Load(tmpDir, false)
		require.NoError(t, err)
		skill.Invalidate()
		t.Cleanup(func() {
			config.Reset()
			skill.Invalidate()
		})

		t.Run("skill not found in skill registry", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"nonexistent-skill"},
					},
				},
			}
			result := appendPreloadedSkills("test-agent", reg)
			assert.Empty(t, result, "should return empty when skill not found")
		})

		t.Run("skill found and allowed", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"my-skill"},
						Permission: map[string]any{
							"skill": "allow",
						},
					},
				},
			}

			result := appendPreloadedSkills("test-agent", reg)
			assert.Contains(t, result, `<skill_content name="my-skill">`)
			assert.Contains(t, result, "This is my skill content.")
			assert.Contains(t, result, `</skill_content>`)
		})

		t.Run("skill with ask permission is injected", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"ask-skill"},
						Permission: map[string]any{
							"skill": "ask",
						},
					},
				},
			}

			result := appendPreloadedSkills("test-agent", reg)
			assert.Contains(t, result, `<skill_content name="ask-skill">`)
			assert.Contains(t, result, "Ask skill content.")
		})

		t.Run("skill with deny permission is skipped", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"denied-skill"},
						Permission: map[string]any{
							"skill": "deny",
						},
					},
				},
			}

			result := appendPreloadedSkills("test-agent", reg)
			assert.Empty(t, result, "denied skill should not be injected")
		})

		t.Run("multiple skills sorted alphabetically", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"beta-skill", "alpha-skill"},
					},
				},
			}

			result := appendPreloadedSkills("test-agent", reg)
			alphaIdx := strings.Index(result, "alpha-skill")
			betaIdx := strings.Index(result, "beta-skill")
			assert.Greater(t, alphaIdx, -1, "alpha-skill should be in output")
			assert.Greater(t, betaIdx, -1, "beta-skill should be in output")
			assert.Less(t, alphaIdx, betaIdx, "alpha-skill should appear before beta-skill")
		})

		t.Run("default permission allows preloaded skill", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"default-skill"},
						// No permission rules — EvaluatePermission returns ActionAsk
					},
				},
			}

			result := appendPreloadedSkills("test-agent", reg)
			assert.Contains(t, result, `<skill_content name="default-skill">`)
			assert.Contains(t, result, "Default permission content.")
		})

		t.Run("skill tool disabled but preloaded skill still injected", func(t *testing.T) {
			reg := &mockRegistry{
				agents: map[string]agentregistry.AgentInfo{
					"test-agent": {
						ID:     "test-agent",
						Skills: []string{"my-skill"},
						Tools: map[string]bool{
							"skill": false, // disable runtime skill tool
						},
					},
				},
			}

			result := appendPreloadedSkills("test-agent", reg)
			assert.Contains(t, result, `<skill_content name="my-skill">`)
			assert.Contains(t, result, "This is my skill content.")
		})
	})
}

func TestTaskToolNameMatchesAgentConst(t *testing.T) {
	// taskToolName is duplicated in this package to avoid an import cycle.
	// This test ensures it stays in sync with agent.TaskToolName.
	assert.Equal(t, "task", taskToolName, "taskToolName must match agent.TaskToolName")
}

func TestCronCreateToolNameMatchesToolsConst(t *testing.T) {
	// cronCreateToolName is duplicated in this package to avoid an import cycle.
	// This test ensures it stays in sync with tools.CronCreateToolName.
	assert.Equal(t, "croncreate", cronCreateToolName, "cronCreateToolName must match tools.CronCreateToolName")
}

func createTestFiles(t *testing.T, tmpDir string, testFiles []string) {
	t.Helper()
	for _, path := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		if path[len(path)-1] == '/' {
			err := os.MkdirAll(fullPath, 0755)
			require.NoError(t, err)
		} else {
			dir := filepath.Dir(fullPath)
			err := os.MkdirAll(dir, 0755)
			require.NoError(t, err)
			err = os.WriteFile(fullPath, []byte(path+": test content"), 0644)
			require.NoError(t, err)
		}
	}
}
