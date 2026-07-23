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
	"github.com/opencode-ai/opencode/internal/bridge"
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

// interactiveStructuredOutputPromptBase replaces structuredOutputPrompt
// when the agent is running an `interactive: true` flow step. The
// chat-bridge has already bound the agent's session to a human
// reviewer; this prompt teaches the agent to engage in multi-turn
// dialogue via the bridge (using the question tool for structured
// asks + plain agent responses for prose) until ALL the required
// fields of the output schema have been clarified, and only THEN
// emit struct_output.
//
// Without this override the default structuredOutputPrompt pushes the
// model to emit struct_output on its first turn — which short-
// circuits the entire human-in-the-loop interaction and makes
// interactive: true a no-op behaviorally.
//
// The "Base" suffix marks this as the static body; the dynamic
// per-step "## Reviewer details" suffix is appended by
// interactiveStructuredOutputPromptFor based on
// AgentPromptOptions.BoundPeers.
const interactiveStructuredOutputPromptBase = `
IMPORTANT — INTERACTIVE FLOW STEP:

You are running inside a human-in-the-loop flow step. Your session is
bound to a human reviewer via a chat bridge (Slack / Telegram /
Mattermost). Your job is to gather all the information needed to
populate the structured output schema by COLLABORATING with the
reviewer over multiple turns.

## How outbound messages reach the reviewer

⚠️ **The reviewer does NOT see your plain prose responses.** Your
assistant-text output stays inside this session — it does NOT fan out
to the chat platform. To say anything to the reviewer you MUST use one
of these two tools:

- ` + "`router_send`" + ` — sends a free-form message (text + optional
  markdown) to the bound peer. Use this for greetings, reflections,
  summaries, and anything else where you want prose to land in chat.
- ` + "`question`" + ` — sends a structured ask (yes/no, pick-from-options,
  single-line text) that renders as native UI on the chat platform
  (Slack buttons, Telegram inline keyboards, etc.) AND blocks waiting
  for the reviewer's reply. Use this for any clarification with a
  bounded answer shape.

Writing prose into your assistant message channel without calling
` + "`router_send`" + ` or ` + "`question`" + ` means the reviewer sees
NOTHING — you'd be talking to yourself across cycles. Don't do that.

## Round-trip pattern

The typical interactive step looks like:

1. ` + "`router_send`" + ` — greet the reviewer + state what you need (~1-2 sentences).
2. ` + "`question`" + ` — ask the first clarifying question (blocks for reply).
3. ` + "`router_send`" + ` — reflect what you heard back, confirm understanding.
4. ` + "`question`" + ` — ask the next question. (blocks)
5. ...repeat...
6. ` + "`router_send`" + ` — post the final scoped plan or summary.
7. ` + "`question`" + ` — ask for confirmation (yes/no).
8. ` + "`struct_output`" + ` — emit the schema-conformant result. **This ends the step.**

Every reviewer message arrives as a new user message in your session,
which triggers your next cycle automatically.

## Hard rules

- Use ` + "`router_send`" + ` or ` + "`question`" + ` for EVERY message you want the reviewer
  to see. Prose-only responses don't reach them.
- Do NOT emit ` + "`struct_output`" + ` until you have CONCRETE, REVIEWED values
  for every required field in the schema. Premature struct_output
  terminates the step and discards the conversation — there is no redo.
- ` + "`struct_output`" + ` is the FINAL action. Don't follow it with more
  chat — the step ends the moment it fires.
- Treat each reviewer message as a turn — acknowledge it (via
  ` + "`router_send`" + ` if needed) before moving on. Reflect back what you
  understood so they can correct you cheaply.
- Keep your environment in mind: this is a text/markdown chat surface,
  not a TUI. No tables, no ANSI colors, no wide layouts. Short
  paragraphs, bullets, and ` + "`inline code`" + ` are safe. Triple-backtick
  blocks render but stay small (≤20 lines).

The struct_output schema and the prompt above describe what to
collect. The reviewer is the source of truth — defer to them and
confirm ambiguous items via ` + "`router_send`" + ` before recording.`

// interactiveStructuredOutputPromptFor returns the interactive flow-step
// system-prompt suffix. When `peers` is non-empty the returned string
// includes a "## Reviewer details" section listing each peer's channel,
// peerId, and (when present) mention handle so the agent knows WHO it's
// bound to without flow authors having to template `${args.reviewer.*}`
// into the YAML prompt.
//
// Empty `peers` returns the legacy base const verbatim — preserves
// backwards-compat for callers (TUI, ACP, etc.) that haven't been
// updated to populate `AgentPromptOptions.BoundPeers`.
func interactiveStructuredOutputPromptFor(peers []bridge.PeerRef) string {
	if len(peers) == 0 {
		return interactiveStructuredOutputPromptBase
	}
	return interactiveStructuredOutputPromptBase + "\n\n" + reviewerDetailsSection(peers)
}

// reviewerDetailsSection renders a "## Reviewer details" markdown block.
// Single-peer → bare per-peer fragment. Multi-peer → opens with the
// multi-reviewer fan-out preamble (matches the bridge's actual inbound
// attribution shape) + a numbered list of per-peer fragments.
func reviewerDetailsSection(peers []bridge.PeerRef) string {
	var b strings.Builder
	b.WriteString("## Reviewer details\n\n")
	if len(peers) == 1 {
		b.WriteString(renderOnePeer(peers[0], ""))
		return b.String()
	}
	fmt.Fprintf(&b,
		"You are bound to %d reviewers; outbound fans out to all, inbound from any routes back here with `[<who> via <channel>]: ` attribution prefix.\n\n",
		len(peers),
	)
	for i, p := range peers {
		b.WriteString(renderOnePeer(p, fmt.Sprintf("%d. ", i+1)))
	}
	return b.String()
}

// renderOnePeer formats a single PeerRef as a one- or two-sentence
// markdown fragment. `labelPrefix` is "" for single-peer renders and
// "<n>. " for numbered multi-peer entries. The "begin your FIRST
// `router_send` with the mention …" sentence is dropped when
// peer.Mention is empty (e.g. Slack DMs where pinging makes no sense).
func renderOnePeer(peer bridge.PeerRef, labelPrefix string) string {
	var primary, mentionSentence string
	switch peer.Channel {
	case "slack":
		primary = fmt.Sprintf("You are bound to %s in Slack channel `%s`.", peerMentionOrFallback(peer, "the bound peer"), peer.PeerID)
		if peer.Mention != "" {
			mentionSentence = fmt.Sprintf("Begin your FIRST `router_send` with the mention `%s` to ping them.", peer.Mention)
		}
	case "telegram":
		primary = fmt.Sprintf("You are bound to chat `%s` on Telegram.", peer.PeerID)
		if peer.Mention != "" {
			mentionSentence = fmt.Sprintf("The reviewer's first-message ping handle is `%s` (use it once in your FIRST `router_send`).", peer.Mention)
		}
	case "mattermost":
		primary = fmt.Sprintf("You are bound to %s in Mattermost channel `%s`.", peerMentionOrFallback(peer, "the bound peer"), peer.PeerID)
		if peer.Mention != "" {
			mentionSentence = fmt.Sprintf("Begin your FIRST `router_send` with the mention `%s` to ping them.", peer.Mention)
		}
	default:
		// Unknown channel: render a generic line so the agent at least
		// sees something. Don't suppress — silent gaps cause the agent
		// to guess (the exact bug this prompt closes).
		primary = fmt.Sprintf("You are bound to peer `%s` on channel `%s`.", peer.PeerID, peer.Channel)
		if peer.Mention != "" {
			mentionSentence = fmt.Sprintf("First-message handle: `%s`.", peer.Mention)
		}
	}
	if mentionSentence == "" {
		return labelPrefix + primary + "\n"
	}
	return labelPrefix + primary + " " + mentionSentence + "\n"
}

func peerMentionOrFallback(peer bridge.PeerRef, fallback string) string {
	if peer.Mention != "" {
		return peer.Mention
	}
	return fallback
}

const parallelToolUsePrompt = `
You have the capability to call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. For example, if you need to read 3 files, call read 3 times in parallel rather than sequentially.`

// backgroundTasksPrompt is the no-poll contract for background work. It is
// appended for EVERY agent with tool access — independent of the agent's
// (possibly custom) base prompt — because the guidance previously lived only
// in CoderPrompt, which is skipped when info.Prompt is set; custom-prompt
// flow agents never saw it and busy-waited with foreground sleeps (incident
// CD-4761). The runtime additionally enforces this contract in
// non-interactive runs (see openspec background-tasks / bash-background-mode
// specs); the prompt is defense-in-depth that saves wasted cycles.
const backgroundTasksPrompt = `
# Background tasks (event-driven, no polling)
- For long-running shell work (test suites, builds, deploys, log tails) prefer ` + "`bash`" + ` with ` + "`run_in_background: true`" + ` over a synchronous bash with a sleep loop. The tool returns immediately with a ` + "`task_id`" + ` and an ` + "`output_file`" + ` path; a synthetic completion notification arrives automatically when the subprocess exits.
- For parallel sub-work, use the ` + "`task`" + ` tool with ` + "`async: true`" + ` to fan out subagents in the background. Same pattern: immediate ack with ` + "`task_id`" + `, synthetic completion when the subagent finishes.
- To watch a streaming command for specific markers, use ` + "`monitor`" + ` (cmd + pattern). Matched lines are coalesced into per-window notifications — strictly better than ` + "`while true; do sleep 5; grep ERROR ...; done`" + `.
- ` + "`tasklist`" + ` is for ONE-SHOT inventory queries only. Do NOT poll it. Completion notifications arrive automatically.
- ` + "`taskstop`" + ` kills a background task and emits a synthetic ` + "`killed`" + ` completion. Use only when the task is no longer useful.
- DO NOT use ` + "`sleep N`" + ` followed by status-check tool calls — every polling round costs tokens and invalidates the prompt cache. Spawn the work in background and let the notification system wake you when it finishes. In non-interactive (flow) runs the runtime holds your turn open until pending background tasks complete and converts a foreground ` + "`sleep`" + ` into that same wait — sleeping can never observe progress sooner.`

// taskToolReportingPrompt instructs primary (mode=agent) agents that have the
// task tool enabled to surface each subagent's task_id to the user so it can
// be referenced or resumed later. This mirrors how Claude Code's coordinator
// reports "Agent ID X is still around..." when summarizing subagent work.
// The task-tool response content includes a <task_id>...</task_id> trailer
// that this prompt teaches the agent to extract and surface.
const taskToolReportingPrompt = `
# Subagent task IDs

Whenever you invoke the task tool, its result includes a ` + "`<task_id>...</task_id>`" + ` trailer. When reporting back to the user, mention the task_id together with a one-line description of what each subagent did so the user can reference or ask to resume it. If you launched multiple subagents in a single turn, list every task_id. Use natural phrasing, for example: "Task abcd1234 (explorer — audited the auth module) is still around if you want to dig deeper." To continue a subagent's session later, pass its task_id back to the task tool along with a new prompt. Do NOT surface a task_id if it was not present in the tool result (e.g., when the subagent produced struct_output).`

// cronToolPrompt instructs agents that have cron tools enabled to use them
// directly when the user asks for reminders, recurring tasks, or scheduled work.
// Without this guidance, agents tend to delegate scheduling requests to subagents
// instead of calling croncreate themselves.
const cronToolPrompt = `
# Scheduling & Reminders

You have cron tools (croncreate, crondelete, cronlist) available. When the user asks you to:
- Set a reminder ("remind me at...", "remind me in...", "remind me every...")
- Schedule recurring work ("every hour check...", "every morning run...")
- Run something later ("at 3pm do...", "in 30 minutes...")
- Set up a timer or periodic task

Use the croncreate tool DIRECTLY — do NOT delegate to a subagent. You are the agent responsible for creating cron jobs. The croncreate tool handles scheduling the prompt to run at the specified time via a subagent automatically.`

// taskToolName matches agent.TaskToolName. Duplicated here to avoid an import
// cycle between the prompt and llm/agent packages.
const taskToolName = "task"

// cronCreateToolName matches tools.CronCreateToolName. Duplicated here to avoid
// an import cycle.
const cronCreateToolName = "croncreate"

func getEnvironmentInfo() string {
	cwd := config.WorkingDirectory()
	isGit := isGitRepo(cwd)
	platform := runtime.GOOS
	date := time.Now().Format("1/2/2006")
	ls := tools.NewLsTool(config.Get(), nil, nil)
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

// AgentPromptOptions tunes per-call overrides for GetAgentPrompt.
// Today only `Interactive` is exposed — set true when the agent is
// running an `interactive: true` flow step so the multi-turn-friendly
// structured-output prompt is appended. Future per-call overrides
// (e.g. forcing a specific tool subset's guidance off) go here.
type AgentPromptOptions struct {
	// Interactive overrides the registered AgentInfo.Interactive
	// (which is the in-memory flag set by AgentFactory.NewAgent for
	// this specific agent instance). Callers that pass through the
	// AgentInfo from NewAgent should set this to AgentInfo.Interactive.
	Interactive bool

	// BoundPeers is the resolved list of chat-bridge peers the agent's
	// session is bound to for the current interactive flow step. The
	// flow runner populates it from the resolved interaction.target
	// before calling NewAgent. When non-empty AND Interactive is true,
	// the auto-injected interactive prompt grows a "## Reviewer details"
	// section so the agent knows the mention handle / channel / peerId
	// without flow authors having to template ${args.reviewer.*} into
	// the YAML prompt (the legacy resolver doesn't support nested-path
	// access anyway — see internal/flow/service.go::substituteScoped).
	//
	// Empty / nil for non-interactive agents (TUI, ACP, non-bound
	// flow steps). The interactive branch is the only consumer.
	BoundPeers []bridge.PeerRef

	// HasOutputSchema is true when the agent is running a flow step that
	// declares an `output.schema`. Like Interactive, this MUST be plumbed
	// through opts: the per-step schema is injected onto the factory's
	// per-call AgentInfo copy (AgentFactory.NewAgent), but the prompt
	// builder re-fetches the ORIGINAL registry entry via reg.Get, which
	// does NOT carry it. Without this flag a non-interactive flow step on
	// an agent type that declares no STATIC output schema (e.g. a general
	// piano-developer / coder agent) never receives the structuredOutputPrompt
	// instruction, so the model may answer in prose and strand the flow
	// (its routing rules see no output fields). Callers that pass through
	// the AgentInfo from NewAgent should set this to
	// (AgentInfo.Output != nil && AgentInfo.Output.Schema != nil).
	HasOutputSchema bool
}

// GetAgentPromptWithOptions is GetAgentPrompt + per-call overrides.
// The registered AgentInfo is still consulted for tool gating,
// permissions, skills, etc. — only the prompt-shape selection bits
// come from opts.
func GetAgentPromptWithOptions(agentName config.AgentName, provider models.ModelProvider, opts AgentPromptOptions) string {
	return getAgentPromptInternal(agentName, provider, opts)
}

func GetAgentPrompt(agentName config.AgentName, provider models.ModelProvider) string {
	return getAgentPromptInternal(agentName, provider, AgentPromptOptions{})
}

func getAgentPromptInternal(agentName config.AgentName, provider models.ModelProvider, opts AgentPromptOptions) string {
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

	// Append structured output instruction if the agent has the tool
	// enabled. Interactive flow steps get a multi-turn-friendly variant
	// (see interactiveStructuredOutputPromptBase) — it tells the agent to
	// collaborate with the human reviewer via the chat bridge over
	// multiple turns and reserve struct_output for the END. Without
	// this swap, the terse default prompt pushes the model to emit
	// struct_output on its first turn, effectively skipping the
	// human-in-the-loop.
	//
	// opts.Interactive (set by AgentFactory.NewAgent for interactive
	// flow steps) wins over info.Interactive (which is also set in
	// the same code path — they're kept in sync but the per-call
	// override is the authoritative one because the registered
	// AgentInfo from reg.Get() returns the original Registry copy,
	// NOT the per-call infoCopy with Interactive set).
	if info, ok := reg.Get(agentName); ok {
		// A flow step's output schema is injected onto the factory's
		// per-call AgentInfo copy, which reg.Get does NOT return here (it
		// returns the original registry entry — same reason Interactive is
		// plumbed through opts). opts.HasOutputSchema carries that per-call
		// presence; info.Output covers agents that declare a STATIC schema
		// in their definition. Either one arms the struct_output prompt.
		hasOutputSchema := opts.HasOutputSchema || (info.Output != nil && info.Output.Schema != nil)
		// Interactive flow steps always get the multi-turn prompt: the
		// flow service injects both struct_output + the output schema
		// dynamically, so the static info.Output check would miss them.
		// Without this branch the agent never sees the "you MUST call
		// router_send / question to reach the reviewer" instructions
		// and silently role-plays both sides of the conversation
		// before emitting struct_output — defeating the whole point of
		// interactive: true.
		if opts.Interactive || info.Interactive {
			basePrompt += interactiveStructuredOutputPromptFor(opts.BoundPeers)
		} else if hasOutputSchema && reg.IsToolEnabled(agentName, tools.StructOutputToolName) {
			basePrompt += structuredOutputPrompt
		}
	}

	// Append parallel tool use encouragement for agents with tools
	if info, ok := reg.Get(agentName); ok {
		if reg.HasTools(agentName) && info.AllowsParallelToolUse() {
			basePrompt += parallelToolUsePrompt
		}
	}

	// Append the background-tasks no-poll contract for every agent with
	// tool access — deliberately NOT part of any base prompt so custom-
	// prompt agents (info.Prompt != "") receive it too. Tool-less agents
	// (summarizer, descriptor) are exempt: they cannot spawn or poll
	// background work.
	if reg.HasTools(agentName) {
		basePrompt += backgroundTasksPrompt
	}

	// Append task_id reporting guidance for primary agents that can spawn
	// subagents via the task tool. Subagents (mode != agent) don't report
	// task_ids to the user themselves.
	if info, ok := reg.Get(agentName); ok {
		if info.Mode == config.AgentModeAgent && reg.IsToolEnabled(agentName, taskToolName) {
			basePrompt += taskToolReportingPrompt
		}
	}

	// Append cron scheduling guidance for agents with cron tools enabled.
	// Cron tools are default-deny, so use IsToolExplicitlyEnabled to match
	// the gating in NewToolSet (agent/tools.go).
	if info, ok := reg.Get(agentName); ok {
		if info.Mode == config.AgentModeAgent && reg.IsToolExplicitlyEnabled(agentName, cronCreateToolName) {
			basePrompt += cronToolPrompt
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
