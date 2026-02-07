package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/opencode-ai/opencode/internal/logging"
)

// BinDir returns the directory where auto-installed LSP binaries are stored.
func BinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".opencode", "bin")
	}
	return filepath.Join(home, ".opencode", "bin")
}

// ResolveCommand finds or installs the LSP server binary and returns the command + args.
func ResolveCommand(ctx context.Context, server ResolvedServer, disableDownload bool) (string, []string, error) {
	if len(server.Command) == 0 {
		return "", nil, fmt.Errorf("no command configured for %s", server.ID)
	}

	cmd := server.Command[0]
	args := server.Command[1:]

	// If user provided an absolute path, use it directly
	if filepath.IsAbs(cmd) {
		if _, err := os.Stat(cmd); err == nil {
			logServerVersion(cmd, server.ID)
			return cmd, args, nil
		}
		return "", nil, fmt.Errorf("configured command not found: %s", cmd)
	}

	// Check system PATH
	if path, err := exec.LookPath(cmd); err == nil {
		logServerVersion(path, server.ID)
		return path, args, nil
	}

	// Check our bin directory
	binDir := BinDir()
	localBin := filepath.Join(binDir, cmd)
	if _, err := os.Stat(localBin); err == nil {
		return localBin, args, nil
	}

	// For npm packages, check node_modules/.bin
	npmBin := filepath.Join(binDir, "node_modules", ".bin", cmd)
	if _, err := os.Stat(npmBin); err == nil {
		return npmBin, args, nil
	}

	// Try auto-install if not disabled
	if disableDownload || server.Strategy == StrategyNone {
		return "", nil, fmt.Errorf("binary %q not found for %s (auto-install disabled or not supported)", cmd, server.ID)
	}

	logging.Info("Auto-installing LSP server", "name", server.ID, "strategy", server.Strategy)

	var err error
	switch server.Strategy {
	case StrategyNpm:
		err = installNpm(ctx, server)
	case StrategyGoInstall:
		err = installGo(ctx, server)
	case StrategyGitHubRelease:
		err = installGitHubRelease(ctx, server)
	default:
		return "", nil, fmt.Errorf("unknown install strategy for %s", server.ID)
	}

	if err != nil {
		return "", nil, fmt.Errorf("auto-install failed for %s: %w", server.ID, err)
	}

	// Re-check after install
	if path, err := exec.LookPath(cmd); err == nil {
		logServerVersion(path, server.ID)
		return path, args, nil
	}
	if _, err := os.Stat(localBin); err == nil {
		logServerVersion(localBin, server.ID)
		return localBin, args, nil
	}
	if _, err := os.Stat(npmBin); err == nil {
		logServerVersion(npmBin, server.ID)
		return npmBin, args, nil
	}

	return "", nil, fmt.Errorf("binary %q still not found after install for %s", cmd, server.ID)
}

// logServerVersion attempts to get and log the server version for debugging.
func logServerVersion(binaryPath, serverID string) {
	for _, flag := range []string{"--version", "version", "-v"} {
		cmd := exec.Command(binaryPath, flag)
		output, err := cmd.Output()
		if err == nil {
			version := strings.TrimSpace(strings.Split(string(output), "\n")[0])
			if version != "" {
				logging.Info("LSP server resolved", "name", serverID, "path", binaryPath, "version", version)
				return
			}
		}
	}
	logging.Info("LSP server resolved", "name", serverID, "path", binaryPath)
}

func installNpm(ctx context.Context, server ResolvedServer) error {
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm not found in PATH, cannot auto-install %s", server.ID)
	}

	binDir := BinDir()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	packages := strings.Fields(server.InstallPackage)
	args := append([]string{"install", "--prefix", binDir}, packages...)

	cmd := exec.CommandContext(ctx, npmPath, args...)
	cmd.Dir = binDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("npm install failed: %w\noutput: %s", err, string(output))
	}

	logging.Info("Successfully installed LSP server via npm", "name", server.ID)
	return nil
}

func installGo(ctx context.Context, server ResolvedServer) error {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found in PATH, cannot auto-install %s", server.ID)
	}

	binDir := BinDir()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	cmd := exec.CommandContext(ctx, goPath, "install", server.InstallPackage)
	cmd.Env = append(os.Environ(), "GOBIN="+binDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go install failed: %w\noutput: %s", err, string(output))
	}

	logging.Info("Successfully installed LSP server via go install", "name", server.ID)
	return nil
}

func installGitHubRelease(ctx context.Context, server ResolvedServer) error {
	if server.InstallRepo == "" {
		return fmt.Errorf("no GitHub repo configured for %s", server.ID)
	}

	binDir := BinDir()
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	// Fetch latest release info
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", server.InstallRepo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch release info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API returned status %d for %s", resp.StatusCode, server.InstallRepo)
	}

	var release struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to decode release info: %w", err)
	}

	// Find matching asset for current platform
	asset := findMatchingAsset(release.Assets, server.ID)
	if asset == "" {
		return fmt.Errorf("no matching release asset found for %s on %s/%s", server.ID, runtime.GOOS, runtime.GOARCH)
	}

	// Download the asset
	logging.Info("Downloading LSP server", "name", server.ID, "url", asset)
	req, err = http.NewRequestWithContext(ctx, "GET", asset, nil)
	if err != nil {
		return err
	}

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download release: %w", err)
	}
	defer resp.Body.Close()

	// Save to a temp file and extract
	tmpFile, err := os.CreateTemp(binDir, "lsp-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to download: %w", err)
	}
	tmpFile.Close()

	// Extract based on file extension
	downloadName := filepath.Base(asset)
	switch {
	case strings.HasSuffix(downloadName, ".tar.gz") || strings.HasSuffix(downloadName, ".tgz"):
		return extractTarGz(tmpFile.Name(), binDir, server.Command[0])
	case strings.HasSuffix(downloadName, ".zip"):
		return extractZip(tmpFile.Name(), binDir, server.Command[0])
	default:
		// Assume it's a raw binary
		dest := filepath.Join(binDir, server.Command[0])
		if err := os.Rename(tmpFile.Name(), dest); err != nil {
			return err
		}
		return os.Chmod(dest, 0o755)
	}
}

type releaseAsset struct {
	Name string
	URL  string
}

func findMatchingAsset(assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}, serverID string) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Platform name variations
	osNames := []string{goos}
	archNames := []string{goarch}

	switch goos {
	case "darwin":
		osNames = append(osNames, "macos", "osx", "apple")
	case "linux":
		osNames = append(osNames, "linux")
	case "windows":
		osNames = append(osNames, "win", "windows")
	}

	switch goarch {
	case "amd64":
		archNames = append(archNames, "x86_64", "x64")
	case "arm64":
		archNames = append(archNames, "aarch64")
	}

	for _, a := range assets {
		name := strings.ToLower(a.Name)
		osMatch := false
		archMatch := false

		for _, os := range osNames {
			if strings.Contains(name, os) {
				osMatch = true
				break
			}
		}
		for _, arch := range archNames {
			if strings.Contains(name, arch) {
				archMatch = true
				break
			}
		}

		if osMatch && archMatch {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

func extractTarGz(src, destDir, binaryName string) error {
	cmd := exec.Command("tar", "xzf", src, "-C", destDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar extraction failed: %w\noutput: %s", err, string(output))
	}

	// Try to find and make the binary executable
	binary := filepath.Join(destDir, binaryName)
	if _, err := os.Stat(binary); err == nil {
		return os.Chmod(binary, 0o755)
	}

	// Search for the binary in subdirectories
	err = filepath.WalkDir(destDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == binaryName {
			return os.Chmod(path, 0o755)
		}
		return nil
	})
	return err
}

func extractZip(src, destDir, binaryName string) error {
	cmd := exec.Command("unzip", "-o", src, "-d", destDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("unzip failed: %w\noutput: %s", err, string(output))
	}

	binary := filepath.Join(destDir, binaryName)
	if _, err := os.Stat(binary); err == nil {
		return os.Chmod(binary, 0o755)
	}
	return nil
}
