package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func Key(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func CanonicalRemote(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if !strings.Contains(remote, "://") {
		if i := strings.Index(remote, ":"); i > 0 && !strings.Contains(remote[:i], "/") {
			remote = "ssh://" + remote[:i] + "/" + remote[i+1:]
		}
	}
	u, err := url.Parse(remote)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ssh" && u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "git") {
		return "", fmt.Errorf("origin is not a network Git URL; configure memory.projectID")
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && port != "22" && port != "443" && port != "80" && port != "9418" {
		host += ":" + port
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if repo == "" || strings.Contains(repo, "..") {
		return "", fmt.Errorf("origin has no valid repository path")
	}
	return "git:" + host + "/" + repo, nil
}

func ProjectID(directory, cwd, explicit string) (string, error) {
	id := explicit
	if id == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		remote, err := exec.CommandContext(ctx, "git", "-C", cwd, "config", "--get", "remote.origin.url").Output()
		if err == nil {
			id, err = CanonicalRemote(string(remote))
		}
		if err != nil || id == "" {
			absolute, err := filepath.Abs(cwd)
			if err != nil {
				return "", err
			}
			id = "local:" + filepath.ToSlash(absolute)
		}
	}
	aliases := map[string]string{}
	data, err := os.ReadFile(filepath.Join(directory, "aliases.json"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &aliases); err != nil {
			return "", fmt.Errorf("memory aliases: %w", err)
		}
	}
	seen := map[string]bool{}
	for aliases[id] != "" {
		if seen[id] {
			return "", fmt.Errorf("memory alias cycle")
		}
		seen[id] = true
		id = aliases[id]
	}
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "\x00\r\n") {
		return "", fmt.Errorf("invalid memory project identity")
	}
	return id, nil
}
