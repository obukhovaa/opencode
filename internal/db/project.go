package db

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/logging"
)

// GetProjectID determines the project ID for the given working directory.
// It first attempts to use the Git repository origin URL, falling back to
// the directory name if Git is not available or configured.
func GetProjectID(workingDir string) string {
	// Try Git first
	if projectID, err := getProjectIDFromGit(workingDir); err == nil && projectID != "" {
		logging.Debug("Using Git-based project ID", "project_id", projectID, "working_dir", workingDir)
		return projectID
	}

	// Fallback to directory name
	projectID := getProjectIDFromDirectory(workingDir)
	logging.Debug("Using directory-based project ID", "project_id", projectID, "working_dir", workingDir)
	return projectID
}

// getProjectIDFromGit attempts to get the project ID from Git remote origin URL.
func getProjectIDFromGit(workingDir string) (string, error) {
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
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
	// Remove .git suffix
	url = strings.TrimSuffix(url, ".git")

	// Remove trailing slashes
	url = strings.TrimRight(url, "/")

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
