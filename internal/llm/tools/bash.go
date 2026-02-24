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

const bashDescriptionTemplate = `Executes a given bash command in a persistent shell session with optional timeout, ensuring proper handling and security measures.

All commands run in ${directory} by default. Use the ` + "`workdir`" + ` parameter if you need to run a command in a different directory. AVOID using ` + "`cd <directory> && <command>`" + ` patterns - use ` + "`workdir`" + ` instead.

IMPORTANT: This tool is for terminal operations like git, npm, docker, etc. DO NOT use it for file operations (reading, writing, editing, searching, finding files) - use the specialized tools for this instead.

Before executing the command, please follow these steps:

1. Directory Verification:
   - If the command will create new directories or files, first use ` + "`ls`" + ` to verify the parent directory exists and is the correct location
   - For example, before running "mkdir foo/bar", first use ` + "`ls foo`" + ` to check that "foo" exists and is the intended parent directory

2. Command Execution:
   - Always quote file paths that contain spaces with double quotes (e.g., rm "path with spaces/file.txt")
   - Examples of proper quoting:
     - mkdir "/Users/name/My Documents" (correct)
     - mkdir /Users/name/My Documents (incorrect - will fail)
     - python "/path/with spaces/script.py" (correct)
     - python /path/with spaces/script.py (incorrect - will fail)
   - After ensuring proper quoting, execute the command.
   - Capture the output of the command.

Usage notes:
  - The command argument is required.
  - You can specify an optional timeout in milliseconds. If not specified, commands will time out after 120000ms (2 minutes).
  - It is very helpful if you write a clear, concise description of what this command does in 5-10 words.
  - If the output exceeds ${maxLines} lines or ${maxBytes} bytes, the full output is saved to a temp file and a truncated preview (first/last 500 lines) is shown. Use the View tool with offset/limit to read specific sections of the saved file, or Grep to search the full content. Because of this, you do NOT need to use ` + "`head`" + `, ` + "`tail`" + `, or other truncation commands to limit output - just run the command directly.

  - Avoid using Bash with the ` + "`find`, `grep`, `cat`, `head`, `tail`, `sed`, `awk`, or `echo`" + ` commands, unless explicitly instructed or when these commands are truly necessary for the task. Instead, always prefer using the dedicated tools for these commands:
    - File search: Use Glob (NOT find or ls)
    - Content search: Use Grep (NOT grep or rg)
    - Read files: Use View (NOT cat/head/tail)
    - Edit files: Use Edit (NOT sed/awk)
    - Write files: Use Write (NOT echo >/cat <<EOF)
    - Communication: Output text directly (NOT echo/printf)
  - When issuing multiple commands:
    - If the commands are independent and can run in parallel, make multiple Bash tool calls in a single message. For example, if you need to run "git status" and "git diff", send a single message with two Bash tool calls in parallel.
    - If the commands depend on each other and must run sequentially, use a single Bash call with '&&' to chain them together (e.g., ` + "`git add . && git commit -m \"message\" && git push`" + `). For instance, if one operation must complete before another starts (like mkdir before cp, Write before Bash for git operations, or git add before git commit), run these operations sequentially instead.
    - Use ';' only when you need to run commands sequentially but don't care if earlier commands fail
    - DO NOT use newlines to separate commands (newlines are ok in quoted strings)
  - AVOID using ` + "`cd <directory> && <command>`" + `. Use the ` + "`workdir`" + ` parameter to change directories instead.
    <good-example>
    Use workdir="/foo/bar" with command: pytest tests
    </good-example>
    <bad-example>
    cd /foo/bar && pytest tests
    </bad-example>

# Committing changes with git

Only create commits when requested by the user. If unclear, ask first. When the user asks you to create a new git commit, follow these steps carefully:

Git Safety Protocol:
- NEVER update the git config
- NEVER run destructive/irreversible git commands (like push --force, hard reset, etc) unless the user explicitly requests them
- NEVER skip hooks (--no-verify, --no-gpg-sign, etc) unless the user explicitly requests it
- NEVER run force push to main/master, warn the user if they request it
- Avoid git commit --amend. ONLY use --amend when ALL conditions are met:
  (1) User explicitly requested amend, OR commit SUCCEEDED but pre-commit hook auto-modified files that need including
  (2) HEAD commit was created by you in this conversation (verify: git log -1 --format='%an %ae')
  (3) Commit has NOT been pushed to remote (verify: git status shows "Your branch is ahead")
- CRITICAL: If commit FAILED or was REJECTED by hook, NEVER amend - fix the issue and create a NEW commit
- CRITICAL: If you already pushed to remote, NEVER amend unless user explicitly requests it (requires force push)
- NEVER commit changes unless the user explicitly asks you to. It is VERY IMPORTANT to only commit when explicitly asked, otherwise the user will feel that you are being too proactive.

1. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following bash commands in parallel, each using the Bash tool:
  - Run a git status command to see all untracked files.
  - Run a git diff command to see both staged and unstaged changes that will be committed.
  - Run a git log command to see recent commit messages, so that you can follow this repository's commit message style.
2. Analyze all staged changes (both previously staged and newly added) and draft a commit message:
  - Summarize the nature of the changes (eg. new feature, enhancement to an existing feature, bug fix, refactoring, test, docs, etc.). Ensure the message accurately reflects the changes and their purpose (i.e. "add" means a wholly new feature, "update" means an enhancement to an existing feature, "fix" means a bug fix, etc.).
  - Do not commit files that likely contain secrets (.env, credentials.json, etc.). Warn the user if they specifically request to commit those files
  - Draft a concise (1-2 sentences) commit message that focuses on the "why" rather than the "what"
  - Ensure it accurately reflects the changes and their purpose
3. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following commands:
   - Add relevant untracked files to the staging area.
   - Create the commit with a message
   - Run git status after the commit completes to verify success.
   Note: git status depends on the commit completing, so run it sequentially after the commit.
4. If the commit fails due to pre-commit hook, fix the issue and create a NEW commit (see amend rules above)

Important notes:
- NEVER run additional commands to read or explore code, besides git bash commands
- DO NOT push to the remote repository unless the user explicitly asks you to do so
- IMPORTANT: Never use git commands with the -i flag (like git rebase -i or git add -i) since they require interactive input which is not supported.
- If there are no changes to commit (i.e., no untracked files and no modifications), do not create an empty commit

# Creating pull requests
Use the gh command via the Bash tool for ALL GitHub-related tasks including working with issues, pull requests, checks, and releases. If given a Github URL use the gh command to get the information needed.

IMPORTANT: When the user asks you to create a pull request, follow these steps carefully:

1. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following bash commands in parallel using the Bash tool, in order to understand the current state of the branch since it diverged from the main branch:
   - Run a git status command to see all untracked files
   - Run a git diff command to see both staged and unstaged changes that will be committed
   - Check if the current branch tracks a remote branch and is up to date with the remote, so you know if you need to push to the remote
   - Run a git log command and ` + "`git diff [base-branch]...HEAD`" + ` to understand the full commit history for the current branch (from the time it diverged from the base branch)
2. Analyze all changes that will be included in the pull request, making sure to look at all relevant commits (NOT just the latest commit, but ALL commits that will be included in the pull request!!!), and draft a pull request summary
3. You can call multiple tools in a single response. When multiple independent pieces of information are requested and all commands are likely to succeed, run multiple tool calls in parallel for optimal performance. run the following commands in parallel:
   - Create new branch if needed
   - Push to remote with -u flag if needed
   - Create PR using gh pr create with the format below. Use a HEREDOC to pass the body to ensure correct formatting.
<example>
gh pr create --title "the pr title" --body "$(cat <<'EOF'
## Summary
<1-3 bullet points>
</example>

Important:
- Return the PR URL when you're done, so the user can see it

# Other common operations
- View comments on a Github PR: gh api repos/foo/bar/pulls/123/comments`

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
				"description": "Clear, concise description of what this command does in 5-10 words. Examples:\nInput: ls\nOutput: Lists files in current directory\n\nInput: git status\nOutput: Shows working tree status\n\nInput: npm install\nOutput: Installs package dependencies\n\nInput: mkdir foo\nOutput: Creates directory 'foo'",
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

	isSafeReadOnly := false
	cmdLower := strings.ToLower(params.Command)

	for _, safe := range safeReadOnlyCommands {
		if strings.HasPrefix(cmdLower, strings.ToLower(safe)) {
			if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
				isSafeReadOnly = true
				break
			}
		}
	}

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
			p := b.permissions.Request(
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
