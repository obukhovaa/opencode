package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/logging"
)

var projectIDCache sync.Map // map[string]string

// GetProjectID determines the project ID for the given working directory.
// It first attempts to use the Git repository origin URL, falling back to
// the directory name if Git is not available or configured.
// Results are cached per working directory.
func GetProjectID(workingDir string) string {
	// Check cache first
	if cached, ok := projectIDCache.Load(workingDir); ok {
		return cached.(string)
	}

	// Compute project ID
	var projectID string
	if id, err := getProjectIDFromGit(workingDir); err == nil && id != "" {
		logging.Debug("Using Git-based project ID", "project_id", id, "working_dir", workingDir)
		projectID = id
	} else {
		projectID = getProjectIDFromDirectory(workingDir)
		logging.Debug("Using directory-based project ID", "project_id", projectID, "working_dir", workingDir)
	}

	// Store in cache (LoadOrStore handles race condition)
	actual, _ := projectIDCache.LoadOrStore(workingDir, projectID)
	return actual.(string)
}

// getProjectIDFromGit attempts to get the project ID from Git remote origin URL.
func getProjectIDFromGit(workingDir string) (string, error) {
	// Add timeout to prevent hanging on slow Git operations
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.Dir = workingDir

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	originURL := strings.TrimSpace(string(output))
	if originURL == "" {
		return "", nil
	}

	return normalizeGitURL(originURL), nil
}

// normalizeGitURL normalizes a Git URL to a consistent project ID format.
// Examples:
//   - https://github.com/opencode-ai/opencode.git → github.com/opencode-ai/opencode
//   - git@github.com:opencode-ai/opencode.git → github.com/opencode-ai/opencode
//   - https://gitlab.com/myteam/myproject → gitlab.com/myteam/myproject
func normalizeGitURL(url string) string {
	// Remove trailing slashes first
	url = strings.TrimRight(url, "/")

	// Remove .git suffix
	url = strings.TrimSuffix(url, ".git")

	// Handle SSH URLs (git@github.com:user/repo)
	if strings.HasPrefix(url, "git@") {
		// Convert git@github.com:user/repo to github.com/user/repo
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
		return url
	}

	// Handle HTTPS URLs (https://github.com/user/repo)
	if strings.HasPrefix(url, "https://") {
		return strings.TrimPrefix(url, "https://")
	}

	// Handle HTTP URLs (http://github.com/user/repo)
	if strings.HasPrefix(url, "http://") {
		return strings.TrimPrefix(url, "http://")
	}

	// Return as-is if no known protocol
	return url
}

// getProjectIDFromDirectory returns the base name of the working directory.
func getProjectIDFromDirectory(workingDir string) string {
	return filepath.Base(workingDir)
}
