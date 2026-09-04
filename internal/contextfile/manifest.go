package contextfile

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Manifest rendering caps (design D7). The total-byte cap bounds an
// adversarially large subtree: overflow degrades to paths-only lines,
// then to a trailing "... N more files not shown" summary.
const (
	// DefaultManifestMaxBytes bounds the whole rendered manifest block.
	DefaultManifestMaxBytes = 8192
	// manifestLabelMaxChars truncates a file's label (frontmatter
	// description or first markdown heading).
	manifestLabelMaxChars = 120
	// manifestLabelReadBytes bounds how much of a file is read when
	// extracting its label — descriptions and headings live at the top.
	manifestLabelReadBytes = 32768
)

// ManifestConfig parametrizes RenderManifest. The zero value gets the
// default byte cap.
type ManifestConfig struct {
	// MaxBytes caps the rendered block; <= 0 means DefaultManifestMaxBytes.
	MaxBytes int
	// WalkTruncated notes that the discovery walk hit its maxFiles cap,
	// so the header tells the model the listing is incomplete.
	WalkTruncated bool
}

const manifestHeader = "# Nested Context Files"

const manifestExplainer = "The following context files live in subdirectories of the workspace. " +
	"Their bodies are NOT loaded into this prompt; each file's content is injected automatically " +
	"into the result of the first tool call that touches its directory."

// RenderManifest renders the compact, cache-stable manifest section
// listing the discovered nested context files: one line per file with the
// relative-to-workDir path and a short label. Returns "" when nothing was
// discovered — zero prompt delta for repos without nested context files.
// Labels are the ones computed at discovery time (DiscoveryResult.Labels);
// this function never reads the disk, which makes the manifest a pure —
// and therefore structurally byte-stable — function of the cached
// discovery result (design D3/D7).
func RenderManifest(discovered []string, labels map[string]string, workDir string, cfg ManifestConfig) string {
	if len(discovered) == 0 {
		return ""
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultManifestMaxBytes
	}

	header := manifestHeader
	if cfg.WalkTruncated {
		header += " (listing truncated at the discovery cap)"
	}
	header += "\n" + manifestExplainer + "\n"

	rels := make([]string, len(discovered))
	for i, abs := range discovered {
		rel, err := filepath.Rel(workDir, abs)
		if err != nil {
			rel = abs
		}
		rels[i] = rel
	}

	labeled := make([]string, len(discovered))
	pathsOnly := make([]string, len(discovered))
	for i, abs := range discovered {
		pathsOnly[i] = "- " + rels[i]
		if label := labels[abs]; label != "" {
			labeled[i] = "- " + rels[i] + ": " + label
		} else {
			labeled[i] = pathsOnly[i]
		}
	}

	if block := header + strings.Join(labeled, "\n"); len(block) <= maxBytes {
		return block
	}
	if block := header + strings.Join(pathsOnly, "\n"); len(block) <= maxBytes {
		return block
	}
	// Keep as many path lines as fit, then summarize the rest.
	kept := make([]string, 0, len(pathsOnly))
	used := len(header)
	for i, line := range pathsOnly {
		trailer := fmt.Sprintf("\n... %d more files not shown", len(pathsOnly)-i)
		if used+len(line)+1+len(trailer) > maxBytes {
			return header + strings.Join(kept, "\n") + trailer
		}
		kept = append(kept, line)
		used += len(line) + 1
	}
	return header + strings.Join(kept, "\n")
}

// extractLabel returns a short human label for a context file: the YAML
// frontmatter `description` value if the content starts with a
// frontmatter block, else the first markdown heading, else "" (path-only
// line). Reads at most manifestLabelReadBytes from r. Called ONLY from
// the discovery walk — on a file already opened via the beneath-only
// OpenBeneath path and fstat-verified regular — never at prompt-build
// time. No YAML library on purpose — this package is a leaf, and a
// top-of-file line scan is all a label needs.
func extractLabel(r io.Reader) string {
	scanner := bufio.NewScanner(io.LimitReader(r, manifestLabelReadBytes))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	inFrontmatter := false
	firstContentLine := true
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if firstContentLine {
			if trimmed == "" {
				continue
			}
			firstContentLine = false
			if trimmed == "---" {
				inFrontmatter = true
				continue
			}
		}
		if inFrontmatter {
			if trimmed == "---" {
				inFrontmatter = false
				continue
			}
			if desc, ok := strings.CutPrefix(trimmed, "description:"); ok {
				return truncateLabel(strings.Trim(strings.TrimSpace(desc), `"'`))
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			return truncateLabel(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
		}
	}
	return ""
}

func truncateLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= manifestLabelMaxChars {
		return s
	}
	return string(runes[:manifestLabelMaxChars]) + "…"
}
