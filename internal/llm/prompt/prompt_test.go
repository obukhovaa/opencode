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

func TestGetContextFromPaths(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	_, err := config.Load(tmpDir, false)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg := config.Get()
	cfg.WorkingDir = tmpDir
	cfg.ContextPaths = []string{
		"file.txt",
		"directory/",
	}
	testFiles := []string{
		"file.txt",
		"directory/file_a.txt",
		"directory/file_b.txt",
		"directory/file_c.txt",
	}

	createTestFiles(t, tmpDir, testFiles)

	context := processContextPaths(tmpDir, cfg.ContextPaths)
	assert.Contains(t, context, "file.txt: test content")
	assert.Contains(t, context, "directory/file_a.txt: test content")
	assert.Contains(t, context, "directory/file_b.txt: test content")
	assert.Contains(t, context, "directory/file_c.txt: test content")
}

func TestProcessContextPaths(t *testing.T) {
	t.Parallel()

	t.Run("single file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"a.txt"})

		result := processContextPaths(tmpDir, []string{"a.txt"})
		assert.Contains(t, result, "a.txt: test content")
	})

	t.Run("directory with trailing slash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"docs/one.txt", "docs/two.txt"})

		result := processContextPaths(tmpDir, []string{"docs/"})
		assert.Contains(t, result, "one.txt: test content")
		assert.Contains(t, result, "two.txt: test content")
	})

	t.Run("symlink to file is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"real.txt"})

		err := os.Symlink(filepath.Join(tmpDir, "real.txt"), filepath.Join(tmpDir, "link.txt"))
		require.NoError(t, err)

		result := processContextPaths(tmpDir, []string{"real.txt", "link.txt"})
		count := countOccurrences(result, "real.txt: test content")
		assert.Equal(t, 1, count, "symlinked file should only appear once")
	})

	t.Run("symlink to directory is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"realdir/file.txt"})

		err := os.Symlink(filepath.Join(tmpDir, "realdir"), filepath.Join(tmpDir, "linkdir"))
		require.NoError(t, err)

		result := processContextPaths(tmpDir, []string{"realdir/", "linkdir/"})
		count := countOccurrences(result, "file.txt: test content")
		assert.Equal(t, 1, count, "file in symlinked directory should only appear once")
	})

	t.Run("same file listed twice is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"dup.txt"})

		result := processContextPaths(tmpDir, []string{"dup.txt", "dup.txt"})
		count := countOccurrences(result, "dup.txt: test content")
		assert.Equal(t, 1, count, "duplicate path should only appear once")
	})

	t.Run("file in directory and explicit path is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"ctx/notes.txt"})

		result := processContextPaths(tmpDir, []string{"ctx/", "ctx/notes.txt"})
		count := countOccurrences(result, "notes.txt: test content")
		assert.Equal(t, 1, count, "file listed both via directory and explicit path should only appear once")
	})

	t.Run("nonexistent path produces no output", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		result := processContextPaths(tmpDir, []string{"does-not-exist.txt"})
		assert.Empty(t, result)
	})

	t.Run("empty paths produces no output", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		result := processContextPaths(tmpDir, []string{})
		assert.Empty(t, result)
	})

	t.Run("symlink in walked directory is deduplicated with explicit path", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"source.txt"})

		err := os.MkdirAll(filepath.Join(tmpDir, "dir"), 0755)
		require.NoError(t, err)
		err = os.Symlink(filepath.Join(tmpDir, "source.txt"), filepath.Join(tmpDir, "dir", "link.txt"))
		require.NoError(t, err)

		result := processContextPaths(tmpDir, []string{"source.txt", "dir/"})
		count := countOccurrences(result, "source.txt: test content")
		assert.Equal(t, 1, count, "symlink inside directory should be deduplicated against explicit path")
	})
}

func countOccurrences(s, substr string) int {
	count := 0
	idx := 0
	for {
		i := indexAt(s, substr, idx)
		if i == -1 {
			break
		}
		count++
		idx = i + len(substr)
	}
	return count
}

func indexAt(s, substr string, start int) int {
	if start >= len(s) {
		return -1
	}
	i := indexOf(s[start:], substr)
	if i == -1 {
		return -1
	}
	return start + i
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

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

func (r *mockRegistry) IsToolEnabled(agentID, toolName string) bool {
	a, ok := r.agents[agentID]
	if !ok {
		return true
	}
	return permission.IsToolEnabled(toolName, a.Tools)
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
