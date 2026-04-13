package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/skill"
)

const structuredOutputPrompt = `
IMPORTANT: The user has requested structured output. You MUST use the struct_output tool to provide your final response. Do NOT respond with plain text - you MUST call the struct_output tool with your answer formatted according to the schema.`

const parallelToolUsePrompt = `
You have the capability to call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. For example, if you need to read 3 files, call read 3 times in parallel rather than sequentially.`

func getEnvironmentInfo() string {
	cwd := config.WorkingDirectory()
	isGit := isGitRepo(cwd)
	platform := runtime.GOOS
	date := time.Now().Format("1/2/2006")
	ls := tools.NewLsTool(config.Get())
	r, _ := ls.Run(context.Background(), tools.ToolCall{
		Input: `{"path":"."}`,
	})
	return fmt.Sprintf(`Here is useful information about the environment you are running in:
<env>
Working directory: %s
Is directory a git repo: %s
Platform: %s
Today's date: %s
</env>
<project>
%s
</project>
		`, cwd, boolToYesNo(isGit), platform, date, r.Content)
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func lspInformation() string {
	return `# LSP Information
Tools that support it will also include useful diagnostics such as linting and typechecking.
- These diagnostics will be automatically enabled when you run the tool, and will be displayed in the output at the bottom within the <file_diagnostics></file_diagnostics> and <project_diagnostics></project_diagnostics> tags.
- Take necessary actions to fix the issues.
- You should ignore diagnostics of files that you did not change or are not related or caused by your changes unless the user explicitly asks you to fix them.
`
}

func boolToYesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func GetAgentPrompt(agentName config.AgentName, provider models.ModelProvider) string {
	reg := agentregistry.GetRegistry()

	var basePrompt string
	if info, ok := reg.Get(agentName); ok && info.Prompt != "" {
		basePrompt = info.Prompt
	} else {
		switch agentName {
		case config.AgentCoder:
			basePrompt = CoderPrompt()
		case config.AgentDescriptor:
			basePrompt = DescriptorPrompt(provider)
		case config.AgentExplorer:
			basePrompt = ExplorerPrompt(provider)
		case config.AgentSummarizer:
			basePrompt = SummarizerPrompt(provider)
		case config.AgentWorkhorse:
			basePrompt = WorkhorsePrompt(provider)
		case config.AgentHivemind:
			basePrompt = HivemindPrompt(provider)
		default:
			basePrompt = "You are a helpful assistant"
		}
	}

	// Append structured output instruction if the agent has the tool enabled
	if info, ok := reg.Get(agentName); ok {
		if info.Output != nil && info.Output.Schema != nil && reg.IsToolEnabled(agentName, tools.StructOutputToolName) {
			basePrompt += structuredOutputPrompt
		}
	}

	// Append parallel tool use encouragement for agents with tools
	if info, ok := reg.Get(agentName); ok {
		if reg.HasTools(agentName) && info.AllowsParallelToolUse() {
			basePrompt += parallelToolUsePrompt
		}
	}

	// Inject preloaded skills into prompt
	basePrompt += appendPreloadedSkills(agentName, reg)

	// Add environment info for primary agents
	if info, ok := reg.Get(agentName); ok {
		if info.Mode == config.AgentModeAgent {
			basePrompt += "\n\n" + getEnvironmentInfo()
		}
	}

	// Add LSP information if LSP servers are available and the agent has the LSP tool enabled
	cfg := config.Get()
	if len(install.ResolveServers(cfg)) > 0 && reg.IsToolEnabled(agentName, tools.LSPToolName) {
		basePrompt += "\n" + lspInformation()
	}

	contextContent := getContextFromPaths()
	if contextContent != "" {
		return fmt.Sprintf("%s\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n%s", basePrompt, contextContent)
	}
	return basePrompt
}

const preloadedSkillSizeWarningThreshold = 200 * 1024 // 200KB

// appendPreloadedSkills injects skills declared in AgentInfo.Skills into the prompt.
// Skills are only skipped if explicitly denied. ActionAsk is treated as allow because
// listing a skill in the agent definition is explicit user intent.
func appendPreloadedSkills(agentName string, reg agentregistry.Registry) string {
	info, ok := reg.Get(agentName)
	if !ok || len(info.Skills) == 0 {
		return ""
	}

	// Sort for deterministic output
	sorted := make([]string, len(info.Skills))
	copy(sorted, info.Skills)
	sort.Strings(sorted)

	var sb strings.Builder
	totalSize := 0

	for _, name := range sorted {
		// Check skill-specific permission patterns directly, bypassing IsToolEnabled.
		// Preloaded skills don't use the skill tool, so tools:{"skill": false} (which
		// disables runtime skill loading) must not block preloaded skill injection.
		action := permission.EvaluateToolPermission(tools.SkillToolName, name, info.Permission, reg.GlobalPermissions())
		if action == permission.ActionDeny {
			logging.Debug("Preloaded skill denied by permission, skipping", "agentID", agentName, "skill", name)
			continue
		}

		skillInfo, err := skill.Get(name)
		if err != nil {
			logging.Warn("Preloaded skill not found in registry, skipping", "agentID", agentName, "skill", name)
			continue
		}

		wrapped := skill.WrapSkillContent(name, skillInfo.Content)
		totalSize += len(wrapped)
		sb.WriteString("\n\n")
		sb.WriteString(wrapped)
	}

	if totalSize > preloadedSkillSizeWarningThreshold {
		logging.Warn("Preloaded skills total size exceeds recommended threshold",
			"agentID", agentName,
			"totalBytes", totalSize,
			"threshold", preloadedSkillSizeWarningThreshold,
		)
	}

	return sb.String()
}

var (
	onceContext    sync.Once
	contextContent string
)

func getContextFromPaths() string {
	onceContext.Do(func() {
		var (
			cfg          = config.Get()
			workDir      = cfg.WorkingDir
			contextPaths = cfg.ContextPaths
		)
		contextContent = processContextPaths(workDir, contextPaths)
		logging.Debug("Context content", "context", contextContent)
	})

	return contextContent
}

type contextEntry struct {
	path    string
	content string
}

func processContextPaths(workDir string, paths []string) string {
	var (
		wg       sync.WaitGroup
		resultCh = make(chan contextEntry)
	)

	// Track processed files to avoid duplicates
	processedFiles := make(map[string]bool)
	var processedMutex sync.Mutex

	for _, path := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()

			if strings.HasSuffix(p, "/") {
				filepath.WalkDir(filepath.Join(workDir, p), func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !d.IsDir() {
						if tryMarkProcessed(path, processedFiles, &processedMutex) {
							if content := processFile(path); content != "" {
								resultCh <- contextEntry{path: path, content: content}
							}
						}
					}
					return nil
				})
			} else {
				fullPath := filepath.Join(workDir, p)
				if tryMarkProcessed(fullPath, processedFiles, &processedMutex) {
					if content := processFile(fullPath); content != "" {
						resultCh <- contextEntry{path: fullPath, content: content}
					}
				}
			}
		}(path)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	entries := make([]contextEntry, 0)
	for entry := range resultCh {
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})

	contents := make([]string, 0, len(entries))
	for _, e := range entries {
		contents = append(contents, e.content)
	}
	return strings.Join(contents, "\n")
}

// tryMarkProcessed resolves symlinks to obtain the canonical path and uses it
// as the dedup key. This ensures that symlinks and different relative paths
// pointing to the same file are only processed once.
func tryMarkProcessed(path string, processed map[string]bool, mu *sync.Mutex) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	key := strings.ToLower(resolved)

	mu.Lock()
	defer mu.Unlock()
	if processed[key] {
		return false
	}
	processed[key] = true
	return true
}

func processFile(filePath string) string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	return "# From:" + filePath + "\n" + string(content)
}
