package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/permission"
)

type BashParams struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout"`
	Workdir     string `json:"workdir"`
	Description string `json:"description"`
	// RunInBackground spawns the command as a detached subprocess. The tool
	// returns immediately with an ack carrying a task_id and the path to the
	// per-task output file under `<data.dir>/tasks/<task_id>.out`. When the
	// subprocess exits, a synthetic Assistant(ToolCall)+Tool(ToolResult)
	// pair is injected into the bound session via the task package's
	// EnqueueTaskCompletion primitive.
	//
	// The 600s synchronous timeout cap does NOT apply when RunInBackground
	// is true — the subprocess can run until natural exit, `taskstop`,
	// opencode shutdown, or the pod's activeDeadlineSeconds.
	RunInBackground bool `json:"run_in_background,omitempty"`
}

type BashPermissionsParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
	Workdir string `json:"workdir"`
}

type BashResponseMetadata struct {
	StartTime    int64  `json:"start_time"`
	EndTime      int64  `json:"end_time"`
	Description  string `json:"description,omitempty"`
	ExitCode     int    `json:"exit_code"`
	TempFilePath string `json:"temp_file_path,omitempty"`
}
type bashTool struct {
	permissions permission.Service
	registry    agentregistry.Registry
}

const (
	BashToolName = "bash"

	DefaultTimeout = 2 * 60 * 1000  // 2 minutes in milliseconds
	MaxTimeout     = 10 * 60 * 1000 // 10 minutes in milliseconds
	MaxOutputBytes = 50 * 1024      // 50KB
	MaxOutputLines = 2000
)

var safeReadOnlyCommands = []string{
	"ls", "echo", "pwd", "date", "cal", "uptime", "whoami", "id", "groups", "env", "printenv", "set", "unset", "which", "type", "whereis",
	"whatis", "uname", "hostname", "df", "du", "free", "top", "ps", "kill", "killall", "nice", "nohup", "time", "timeout",

	"git status", "git log", "git diff", "git show", "git branch", "git tag", "git remote", "git ls-files", "git ls-remote",
	"git rev-parse", "git config --get", "git config --list", "git describe", "git blame", "git grep", "git shortlog",

	"go version", "go help", "go list", "go env", "go doc", "go vet", "go fmt", "go mod", "go test", "go build", "go run", "go install", "go clean",
}

func bashDescription() string {
	r := strings.NewReplacer(
		"${directory}", config.WorkingDirectory(),
		"${maxBytes}", strconv.Itoa(MaxOutputBytes),
		"${maxLines}", strconv.Itoa(MaxOutputLines),
	)
	return r.Replace(bashDescriptionTemplate)
}

const bashDescriptionTemplate = `Executes a bash command in a persistent shell session.

Commands run in ${directory} by default; use the ` + "`workdir`" + ` parameter to run elsewhere instead of ` + "`cd <dir> && <command>`" + `. Quote file paths that contain spaces. The default timeout is 120000ms (2 minutes); set ` + "`timeout`" + ` for commands that need longer.

Output: if it exceeds ${maxLines} lines or ${maxBytes} bytes, the full output is saved to a temp file and a truncated preview (first/last 500 lines) is shown. Search the saved file with the grep tool; the read tool works on it only under 250KB — for bigger files extract line ranges with sed in bash. Never pipe the original command through ` + "`head`/`tail`" + ` to pre-truncate; just run it.

For long-running work (test suites, builds, deploys, log tails) pass ` + "`run_in_background: true`" + ` instead of waiting in the foreground or sleeping.

Prefer dedicated tools over shell equivalents:
- File search: glob (not find); content search: grep tool (not grep/rg)
- Read files: read (not cat/head/tail); edit files: edit (not sed/awk)
- Write files: write (not echo >/heredoc); delete: delete (not rm)
- Fetch web content: webfetch (not curl); fall back to curl only for binary downloads or when webfetch fails
- Communicate with the user via your text output, never echo
This tool is for real terminal operations: git, go, npm, docker, and anything without a dedicated tool.

Issue independent commands as multiple bash calls in one response; chain dependent commands with ` + "`&&`" + ` in a single call (` + "`;`" + ` when earlier failures may be ignored). Do not separate commands with newlines.

Git/GitHub policy:
- NEVER commit, push, or modify git config unless the user explicitly asks
- NEVER use destructive flags (push --force, reset --hard) or skip hooks (--no-verify, --no-gpg-sign) unless explicitly requested; never force-push main/master — warn the user if they ask for it
- NEVER amend unless the user explicitly asks: never amend pushed commits or commits you did not create in this conversation, and after a failed or hook-rejected commit fix the issue and create a NEW commit instead of amending
- Interactive flags (git rebase -i, git add -i) are not supported
- Follow the repository's commit-message style; do not commit files that likely contain secrets, and do not create empty commits
- Use the gh CLI for all GitHub operations (PRs, issues, checks, releases); return the PR URL after creating one`

func NewBashTool(permission permission.Service, reg agentregistry.Registry) BaseTool {
	return &bashTool{
		permissions: permission,
		registry:    reg,
	}
}

func (b *bashTool) Info() ToolInfo {
	return ToolInfo{
		Name:        BashToolName,
		Description: bashDescription(),
		Parameters: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The command to execute",
			},
			"timeout": map[string]any{
				"type":        "number",
				"description": "Optional timeout in milliseconds (max 600000)",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("The working directory to run the command in. Defaults to %s. Use this instead of 'cd' commands.", config.WorkingDirectory()),
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Clear, concise description of what this command does in 5-10 words",
			},
			"run_in_background": map[string]any{
				"type":        "boolean",
				"description": "If true, start the command as a detached subprocess. The tool returns IMMEDIATELY with an ack containing a `task_id` and an `output_file` path. The subprocess keeps running; when it exits, a synthetic completion notification is automatically injected into this session (no polling — wait for the notification). Use this for long-running commands (test suites, builds, deploys) instead of `sleep` loops. The 600s timeout cap does NOT apply in background mode. Use the `tasklist` tool to inspect, and the `taskstop` tool to kill a background task.",
			},
		},
		Required: []string{"command", "description"},
	}
}

func (b *bashTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.Timeout > MaxTimeout {
		params.Timeout = MaxTimeout
	} else if params.Timeout <= 0 {
		params.Timeout = DefaultTimeout
	}

	if params.Command == "" {
		return NewTextErrorResponse("missing command"), nil
	}

	workdir := params.Workdir
	if workdir == "" {
		workdir = config.WorkingDirectory()
	}

	isSafeReadOnly := IsSafeReadOnlyCommand(params.Command)

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session ID and message ID are required for creating a new file")
	}
	if !isSafeReadOnly {
		action := b.registry.EvaluatePermission(string(GetAgentID(ctx)), BashToolName, params.Command)
		switch action {
		case permission.ActionAllow:
			// Allowed by config, skip interactive permission
		case permission.ActionDeny:
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		default:
			// "ask" or unset: fall through to interactive permission
			p := b.permissions.Request(ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        workdir,
					ToolName:    BashToolName,
					Action:      "execute",
					Description: fmt.Sprintf("Execute command: %s", params.Command),
					Params: BashPermissionsParams{
						Command: params.Command,
						Workdir: workdir,
					},
				},
			)
			if !p {
				return NewEmptyResponse(), permission.ErrorPermissionDenied
			}
		}
	}
	if params.RunInBackground {
		return b.runBackground(ctx, call, params, workdir, sessionID)
	}
	// Anti-spin: in a non-interactive run with pending non-monitor
	// background tasks, a foreground command whose sole effect is a
	// wall-clock wait is redirected to the deterministic background-task
	// wait instead of executing the sleep (see bash_wait.go).
	if resp, intercepted := interceptForegroundWait(ctx, params.Command, sessionID); intercepted {
		return resp, nil
	}
	startTime := time.Now()
	sh := shell.GetPersistentShell(workdir)
	if sh == nil {
		return NewEmptyResponse(), fmt.Errorf("failed to create shell instance")
	}
	stdout, stderr, exitCode, interrupted, err := sh.Exec(ctx, params.Command, params.Timeout)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("error executing command: %w", err)
	}

	stdoutResult := persistAndTruncate(stdout, "stdout", BashToolName)
	stderrResult := persistAndTruncate(stderr, "stderr", BashToolName)

	errorMessage := stderrResult.content
	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	output := stdoutResult.content
	hasBothOutputs := output != "" && errorMessage != ""

	if hasBothOutputs {
		output += "\n"
	}

	if errorMessage != "" {
		output += "\n" + errorMessage
	}

	tempPath := stdoutResult.filePath
	if tempPath == "" {
		tempPath = stderrResult.filePath
	}

	metadata := BashResponseMetadata{
		StartTime:    startTime.UnixMilli(),
		EndTime:      time.Now().UnixMilli(),
		Description:  params.Description,
		ExitCode:     exitCode,
		TempFilePath: tempPath,
	}
	if output == "" {
		return WithResponseMetadata(NewTextResponse("no output"), metadata), nil
	}
	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

func (b *bashTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	var params BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return false
	}
	return IsSafeReadOnlyCommand(params.Command)
}

func (b *bashTool) IsBaseline() bool { return true }

type persistResult struct {
	content  string
	filePath string
}

func persistAndTruncate(content, label, tool string) persistResult {
	if content == "" {
		return persistResult{}
	}

	lines := strings.Split(content, "\n")
	totalBytes := len(content)

	if totalBytes <= MaxOutputBytes && len(lines) <= MaxOutputLines {
		return persistResult{content: content}
	}

	filePath := persistToTempFile(content, fmt.Sprintf("%s-%s", tool, label))
	preview, totalLines := buildPreview(content, TruncatedHeadLines, TruncatedTailLines)
	header := buildTruncationHeader(label, totalLines, filePath, totalBytes)

	return persistResult{
		content:  header + preview,
		filePath: filePath,
	}
}
