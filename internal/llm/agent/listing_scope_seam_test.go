package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/skill"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// listingScopeWorkspace lays out a Piano-style workspace: shared skills and
// subagent types under .agents, and two team repos with their own.
func listingScopeWorkspace(t *testing.T) *agentFactory {
	t.Helper()
	workDir := t.TempDir()
	for _, sk := range []string{".agents/skills/shared-one", "composer/skills/composer-x", "bdata/skills/bdata-y"} {
		name := filepath.Base(sk)
		writeFile(t, filepath.Join(workDir, sk, "SKILL.md"),
			"---\nname: "+name+"\ndescription: Does "+name+" things.\n---\nBody of "+name+".\n")
	}
	for _, ty := range []string{"composer/types/composer-helper.md", "bdata/types/bdata-helper.md"} {
		writeFile(t, filepath.Join(workDir, ty), "---\ndescription: Helper from "+ty+".\nmode: subagent\n---\nYou help.\n")
	}

	loadConfigIn(t, workDir)
	cfg := config.Get()
	cfg.WorkingDir = workDir
	cfg.Providers[models.ProviderAnthropic] = config.Provider{APIKey: "test-key"}
	cfg.Skills = &config.SkillsConfig{Paths: []string{"composer/skills", "bdata/skills"}}
	cfg.AgentPaths = []string{"composer/types", "bdata/types"}
	// A primary agent also builds summarizer/descriptor providers; pin them to
	// the test provider so ambient VERTEXAI_* env cannot pick theirs.
	for _, helper := range []config.AgentName{config.AgentSummarizer, config.AgentDescriptor} {
		cfg.Agents[helper] = config.Agent{Model: models.Claude45Haiku}
	}
	cfg.Agents["seam-parent"] = config.Agent{
		Model:      models.Claude45Haiku,
		Mode:       config.AgentModeAgent,
		Prompt:     "You are the listing scope seam agent.",
		AllowTools: []string{"skill", "task"},
	}
	cfg.Agents["seam-config-sub"] = config.Agent{
		Model:       models.Claude45Haiku,
		Mode:        config.AgentModeSubagent,
		Description: "Defined in config, without a file.",
		Prompt:      "You help.",
		AllowTools:  []string{"skill"},
	}
	agentregistry.InvalidateRegistry()
	skill.Invalidate()
	t.Cleanup(agentregistry.InvalidateRegistry)
	t.Cleanup(skill.Invalidate)

	return &agentFactory{
		registry:    agentregistry.GetRegistry(),
		permissions: permission.NewPermissionService(),
		mcpRegistry: seamStubMCPRegistry{Broker: pubsub.NewBroker[MCPServerEvent]()},
		stepCache:   map[stepCacheKey]Service{},
	}
}

func toolDescription(t *testing.T, svc Service, name string) string {
	t.Helper()
	built, ok := svc.(*agent)
	require.True(t, ok)
	for _, tl := range built.resolveTools() {
		if tl.Info().Name == name {
			return tl.Info().Description
		}
	}
	t.Fatalf("agent has no %q tool", name)
	return ""
}

var composerScope = agentregistry.ListingScope{
	Skills:    []string{".agents/skills/**", "composer/skills/**"},
	Subagents: []string{"composer/types/**"},
}

func TestFactoryNewAgent_ListingScopeReachesSkillAndTaskTools(t *testing.T) {
	factory := listingScopeWorkspace(t)
	ctx := tools.WithListingScope(context.Background(), composerScope)
	svc, err := factory.NewAgent(ctx, "seam-parent", nil, "work", false, nil, nil,
		contextfile.TemplateVars{FlowID: "f", FlowStep: "work"}, ModelOverride{})
	require.NoError(t, err)

	skills := toolDescription(t, svc, "skill")
	assert.Contains(t, skills, "<name>shared-one</name>")
	assert.Contains(t, skills, "<name>composer-x</name>")
	assert.NotContains(t, skills, "<name>bdata-y</name>")
	assert.Contains(t, skills, `<other_skills count="1">bdata-y</other_skills>`)

	task := toolDescription(t, svc, "task")
	assert.Contains(t, task, "- composer-helper: Helper from composer/types/composer-helper.md.")
	assert.Contains(t, task, "- seam-config-sub: Defined in config, without a file.",
		"an agent defined in config has no file and is always listed in full")
	assert.NotContains(t, task, "- bdata-helper:")
	assert.Contains(t, task, "when the task needs it): bdata-helper")
}

func TestFactoryNewAgent_UnscopedListingsAreUnchanged(t *testing.T) {
	factory := listingScopeWorkspace(t)
	svc, err := factory.NewAgent(context.Background(), "seam-parent", nil, "", false, nil, nil,
		contextfile.TemplateVars{}, ModelOverride{})
	require.NoError(t, err)

	skills := toolDescription(t, svc, "skill")
	assert.Contains(t, skills, "<name>bdata-y</name>")
	assert.NotContains(t, skills, "other_skills")
	task := toolDescription(t, svc, "task")
	assert.Contains(t, task, "- bdata-helper:")
	assert.NotContains(t, task, "Other subagent types")
}

// A subagent the task tool builds on the step's tool-call ctx is scoped the
// same way: Run hands the factory that ctx.
func TestFactoryNewAgent_SubagentInheritsScopeFromCtx(t *testing.T) {
	factory := listingScopeWorkspace(t)
	ctx := tools.WithListingScope(context.Background(), composerScope)
	sub, err := factory.NewAgent(ctx, "seam-config-sub", nil, "", false, nil, nil, contextfile.TemplateVars{}, ModelOverride{})
	require.NoError(t, err)
	assert.Contains(t, toolDescription(t, sub, "skill"), `<other_skills count="1">bdata-y</other_skills>`)
}

func TestFactoryNewAgent_StepCacheFollowsScope(t *testing.T) {
	factory := listingScopeWorkspace(t)
	build := func(scope agentregistry.ListingScope) Service {
		svc, err := factory.NewAgent(tools.WithListingScope(context.Background(), scope), "seam-parent", nil, "work", false, nil, nil,
			contextfile.TemplateVars{FlowStep: "work"}, ModelOverride{})
		require.NoError(t, err)
		svc.(*agent).resolveTools()
		return svc
	}
	first := build(composerScope)
	assert.Same(t, first, build(composerScope), "the same scope reuses the cached step agent")
	other := build(agentregistry.ListingScope{Skills: []string{"bdata/skills/**"}})
	assert.NotSame(t, first, other, "a re-entered step with another scope must build a new agent")
}

func TestInheritStepToolShaping_CopiesScopeOntoDetachedCtx(t *testing.T) {
	parent := tools.WithDeniedTools(tools.WithListingScope(context.Background(), composerScope), []string{"gitlab_*"})
	run := inheritStepToolShaping(context.Background(), parent)
	assert.True(t, slices.Equal(tools.ListingScopeFrom(run).Skills, composerScope.Skills))
	assert.Equal(t, []string{"gitlab_*"}, tools.DeniedTools(run))

	unscoped := inheritStepToolShaping(context.Background(), context.Background())
	assert.Nil(t, tools.ListingScopeFrom(unscoped).Skills)
}

func TestStepCacheKey_ListingScopeMisses(t *testing.T) {
	f := &agentFactory{stepCache: map[stepCacheKey]Service{}}
	base := stepCacheKey{stepID: "implement", agentID: "piano-developer", listing: tools.ListingScopeKey(composerScope)}
	f.stepCache[base] = nil
	if _, ok := f.stepCache[stepCacheKey{stepID: "implement", agentID: "piano-developer"}]; ok {
		t.Error("an unscoped arrival must not reuse the scoped agent")
	}
}

// Scope never narrows what `task` accepts: the unknown-type error still names
// every subagent, the names-only ones included.
func TestTaskTool_UnknownTypeListsNamesOnlySubagents(t *testing.T) {
	factory := listingScopeWorkspace(t)
	tool := NewAgentTool(nil, factory.permissions, factory.registry, factory, "seam-parent", composerScope.Subagents)
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "s1")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "m1")
	resp, err := tool.Run(ctx, tools.ToolCall{ID: "c1", Name: TaskToolName,
		Input: `{"prompt":"x","subagent_type":"nope","task_title":"t"}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "bdata-helper")
	assert.Contains(t, resp.Content, "composer-helper")
	assert.NotContains(t, tool.Info().Description, "- bdata-helper:")
}

func TestSkillTool_LoadsNamesOnlySkill(t *testing.T) {
	factory := listingScopeWorkspace(t)
	tool := tools.NewSkillTool(factory.permissions, factory.registry, "seam-parent", composerScope.Skills)
	require.Contains(t, tool.Info().Description, `<other_skills count="1">bdata-y</other_skills>`)
	factory.permissions.AutoApproveSession("s1")
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, tools.ToolCall{ID: "c1", Name: tools.SkillToolName, Input: `{"name":"bdata-y"}`})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Body of bdata-y.")
}
