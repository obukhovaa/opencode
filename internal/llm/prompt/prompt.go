package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
)

func GetAgentPrompt(agentName config.AgentName, provider models.ModelProvider) string {
	// Check registry for custom agent prompt first
	reg := agentregistry.GetRegistry()
	if info, ok := reg.Get(agentName); ok && info.Prompt != "" {
		basePrompt := info.Prompt
		contextContent := getContextFromPaths()
		if contextContent != "" {
			return fmt.Sprintf("%s\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n%s", basePrompt, contextContent)
		}
		return basePrompt
	}

	basePrompt := ""
	switch agentName {
	case config.AgentCoder:
		basePrompt = CoderPrompt(provider)
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

	if agentName == config.AgentCoder || agentName == config.AgentExplorer || agentName == config.AgentWorkhorse || agentName == config.AgentHivemind {
		contextContent := getContextFromPaths()
		if contextContent != "" {
			return fmt.Sprintf("%s\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n%s", basePrompt, contextContent)
		}
	}
	return basePrompt
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

func processContextPaths(workDir string, paths []string) string {
	var (
		wg       sync.WaitGroup
		resultCh = make(chan string)
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
							if result := processFile(path); result != "" {
								resultCh <- result
							}
						}
					}
					return nil
				})
			} else {
				fullPath := filepath.Join(workDir, p)
				if tryMarkProcessed(fullPath, processedFiles, &processedMutex) {
					if result := processFile(fullPath); result != "" {
						resultCh <- result
					}
				}
			}
		}(path)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make([]string, 0)
	for result := range resultCh {
		results = append(results, result)
	}

	return strings.Join(results, "\n")
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
