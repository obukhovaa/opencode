package service

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// FILE: protocol — matches the TS bridge's outbound parser. An agent
// outbound MAY include one or more "FILE:<path>" lines; each is replaced
// at delivery time with the file attached to the platform message. The
// surrounding text is forwarded as-is.
//
// Lines are recognised only when:
//
//   - "FILE:" appears at the start of the line (whitespace allowed before
//     but no other content)
//   - the rest of the line is an absolute path under the bridge media
//     store (security: prevents an agent from exfiltrating arbitrary
//     files via the chat surface)

const (
	// fileTokenPrefix is the literal prefix that introduces an attachment
	// line in agent outbound text.
	fileTokenPrefix = "FILE:"

	// mediaSubdir is the directory under config.Data.Directory where the
	// bridge stores both inbound downloads (adapter-side) and outbound
	// attachments staged for delivery (Service-side).
	mediaSubdir = "bridge/media"
)

// ErrUnsafeMediaPath is returned by ParseFileTokens when an FILE: line
// points outside the bridge media store. The agent's outbound is
// forwarded without the attachment; a warn is logged.
var ErrUnsafeMediaPath = errors.New("bridge: FILE: path is not under the media store")

// ParseFileTokens scans text for FILE: lines, returns the surrounding
// text (with the FILE: lines removed) plus the parsed bridge.Attachment
// values. Paths must be absolute and prefixed by mediaRoot — anything
// else is rejected (returned in unsafe alongside the cleaned text and
// remaining safe attachments) so an agent cannot exfiltrate arbitrary
// files into a chat surface.
func ParseFileTokens(text, mediaRoot string) (clean string, atts []bridge.Attachment, unsafe []string) {
	if !strings.Contains(text, fileTokenPrefix) {
		return text, nil, nil
	}
	mediaRoot, _ = filepath.Abs(filepath.Clean(mediaRoot))

	var buf strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, fileTokenPrefix) {
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, fileTokenPrefix))
			att, err := loadMediaAttachment(path, mediaRoot)
			if err != nil {
				unsafe = append(unsafe, path)
				continue
			}
			atts = append(atts, att)
			continue
		}
		if !first {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
		first = false
	}
	return strings.TrimSpace(buf.String()), atts, unsafe
}

// loadMediaAttachment validates that path is under mediaRoot and reads
// it into a bridge.Attachment. Returns ErrUnsafeMediaPath if the path
// escapes the root. Exported (lowercase but package-internal) for the
// HTTP send handler.
func loadMediaAttachment(path, mediaRoot string) (bridge.Attachment, error) {
	if path == "" {
		return bridge.Attachment{}, errors.New("empty FILE: path")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return bridge.Attachment{}, err
	}
	rootAbs, err := filepath.Abs(filepath.Clean(mediaRoot))
	if err != nil {
		return bridge.Attachment{}, err
	}
	// macOS uses /private/tmp as the canonical path; /tmp is a symlink.
	// EvalSymlinks lets us compare canonical-vs-canonical so a user
	// passing the "/tmp/.." form against a server whose dataDir
	// resolves through /private/tmp/ doesn't trip the safety check.
	// We only resolve when the candidate exists so a probe-for-write
	// (path doesn't yet exist) still validates by lexical comparison.
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		abs = eval
	}
	if eval, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = eval
	}
	if !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) && abs != rootAbs {
		return bridge.Attachment{}, ErrUnsafeMediaPath
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return bridge.Attachment{}, fmt.Errorf("read FILE: %s: %w", abs, err)
	}
	return bridge.Attachment{
		FilePath: abs,
		FileName: filepath.Base(abs),
		Content:  data,
	}, nil
}

// MediaDir returns the bridge's media-store directory under the
// configured data directory. The directory is created if it doesn't
// exist. Used by adapters for inbound file persistence and by the
// FILE: outbound parser for outbound path validation.
func (s *Service) MediaDir() (string, error) {
	if s.dataDir == "" {
		return "", errors.New("bridge: data directory is empty")
	}
	dir := filepath.Join(s.dataDir, mediaSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("bridge: media dir: %w", err)
	}
	return dir, nil
}

// StoreOutboundFile copies content into the bridge's media store under a
// fresh UUID-named file and returns the absolute path. Used by HTTP
// handlers for /router/send file uploads and (later) by the router_send
// agent tool when the agent provides file content via the tool input
// rather than a FILE: line.
func (s *Service) StoreOutboundFile(filename string, content []byte) (string, error) {
	dir, err := s.MediaDir()
	if err != nil {
		return "", err
	}
	base := uuid.NewString()
	if filename != "" {
		base = base + "-" + filepath.Base(filename)
	}
	dest := filepath.Join(dir, base)
	if err := os.WriteFile(dest, content, 0o600); err != nil {
		return "", fmt.Errorf("bridge: write media: %w", err)
	}
	return dest, nil
}

// formatRelativeAge renders a unix-millis timestamp as a human-readable
// relative age (e.g. "3m ago", "2h ago"). Used by chat command output
// (`/sessions`, `/session`). Ported from the TS bridge's text.ts.
func formatRelativeAge(unixMillis int64, nowUnixMillis int64) string {
	if unixMillis <= 0 {
		return "never"
	}
	deltaMs := nowUnixMillis - unixMillis
	if deltaMs < 0 {
		deltaMs = 0
	}
	deltaSec := deltaMs / 1000
	switch {
	case deltaSec < 60:
		return fmt.Sprintf("%ds ago", deltaSec)
	case deltaSec < 3600:
		return fmt.Sprintf("%dm ago", deltaSec/60)
	case deltaSec < 86400:
		return fmt.Sprintf("%dh ago", deltaSec/3600)
	default:
		return fmt.Sprintf("%dd ago", deltaSec/86400)
	}
}

// formatTokens renders a token count compactly: "1.2k", "3.4M", etc.
// Ported from the TS bridge's text.ts. Used by chat command output.
func formatTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// formatCost renders the running session cost in USD. Two-decimal
// format matches what the TUI's status bar shows (status.go:167), so
// the reviewer sees the same number in chat and in the local UI. For
// sub-cent runs (cost < $0.005 rounds to $0.00) we emit "<$0.01" so
// the zero isn't misread as "free" — it tells the reviewer the run
// happened but cost less than one cent.
func formatCost(cost float64) string {
	if cost <= 0 {
		return "$0.00"
	}
	if cost < 0.005 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", cost)
}

// formatNextRun renders a future unix-seconds timestamp as a short
// "in 3m" / "in 2h" / "in 4d" string plus an absolute fallback when
// the job is further out than a day. Past/zero timestamps render as
// "due" — the scheduler should pick those up on its next tick.
func formatNextRun(unixSec int64, now time.Time) string {
	if unixSec <= 0 {
		return "—"
	}
	target := time.Unix(unixSec, 0)
	delta := target.Sub(now)
	if delta <= 0 {
		return "due"
	}
	switch {
	case delta < time.Minute:
		return fmt.Sprintf("in %ds", int(delta.Seconds()))
	case delta < time.Hour:
		return fmt.Sprintf("in %dm", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(delta.Hours()))
	default:
		return fmt.Sprintf("%s (in %dd)", target.Format("Mon 15:04"), int(delta.Hours()/24))
	}
}

// truncateOneLine collapses newlines and runs of whitespace into single
// spaces and truncates to max runes with an ellipsis. Used to render
// multi-line bodies (e.g. cron prompts) as a single table cell without
// breaking row alignment. A non-positive max disables truncation.
func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
