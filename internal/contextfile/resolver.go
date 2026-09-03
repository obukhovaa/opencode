// Package contextfile owns context-file discovery, scoped resolution,
// templating, and memoization for agent system prompts.
//
// It is a LEAF package: it imports only the standard library,
// golang.org/x/sync/singleflight, and internal/logging. internal/config,
// internal/agent, internal/flow, and the LLM prompt/tool layers all import
// this package — never the reverse. That direction is load-bearing: the
// progressive-disclosure wrapper lives in the tool layer, and exporting
// resolution from internal/llm/prompt instead would create the cycle
// tools → prompt → tools (see openspec/changes/scoped-context-files/design.md D1).
package contextfile

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/opencode-ai/opencode/internal/logging"
)

// Mode controls how a declared context layer combines with the layers
// below it (step > agent > global).
type Mode string

const (
	// ModeReplace discards every layer below the declaring one. It is the
	// default when a layer declares context.paths — the motivating use
	// case is exclusion.
	ModeReplace Mode = "replace"
	// ModeAppend includes the declaring layer and continues downward to
	// the next declared layer, which applies its own mode by the same rule.
	ModeAppend Mode = "append"
)

// AgentContext scopes which context files feed an agent's system prompt.
// Referenced as config.Agent.Context and agent.AgentInfo.Context; defined
// here so the import edge always points at the leaf.
type AgentContext struct {
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	Mode  string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Nested opts this agent out of nested-context manifest and body
	// injection when set to false. nil means the default (true).
	Nested *bool `json:"nested,omitempty" yaml:"nested,omitempty"`
}

// StepContext is the flow-step counterpart of AgentContext, referenced as
// flow.Step.Context. Same shape, distinct type: the two surfaces evolve
// independently.
type StepContext struct {
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	Mode  string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Nested opts this step out of nested-context manifest and body
	// injection when set to false. nil means the default (true).
	Nested *bool `json:"nested,omitempty" yaml:"nested,omitempty"`
}

// TemplateVars carries the values for the four substitution tokens
// supported in context.paths entries. Env vars are read from the process
// environment at expansion time, not stored here.
type TemplateVars struct {
	Agent    string // ${agent}
	FlowID   string // ${flow.id}
	FlowStep string // ${flow.step}
}

var (
	// resolveCache memoizes resolved context strings for the process
	// lifetime — the same staleness semantics as the sync.Once this
	// replaces (editing a context file still requires a restart), but
	// keyed so N distinct resolved sets coexist per process (design D2).
	resolveCache sync.Map // digest string -> resolved string
	resolveGroup singleflight.Group

	// modeWarned dedupes the unknown-mode WARN per (layer, value) so a
	// typo logs once, not once per prompt build.
	modeWarned sync.Map

	tokenPattern = regexp.MustCompile(`\$\{([^}]*)\}`)
)

// layer is one resolved slice of the step > agent > global stack, with
// entries already template-expanded and containment-checked (scoped
// layers) or verbatim (global layer, for byte-compatibility).
type layer struct {
	entries []string
	mode    Mode
}

// Resolve computes the context block for a single path list, memoized for
// the process lifetime. Paths are the raw config entries (relative to
// workDir; a trailing "/" recurses). The mode participates in the cache
// key so the same list resolved under different modes gets distinct
// entries — required by the per-worktree draft this design subsumes.
func Resolve(paths []string, workDir string, mode Mode) string {
	layers := []layer{{entries: paths, mode: mode}}
	return resolveMemoized(memoKey(workDir, layers), workDir, layers)
}

// ResolveForAgent applies the three-layer merge with precedence
// step > agent > global. A layer is declared when it explicitly sets
// context.paths; the highest declared layer's mode controls whether the
// layers below it contribute (replace discards them, append continues
// downward). Output concatenates included layers in global → agent → step
// order, each layer sorted by absolute path internally, deduplicated
// across the whole merged set. With no agent or step declaration the
// global contextPaths resolve exactly as before this feature existed —
// byte-identical output.
func ResolveForAgent(globalPaths []string, agentCtx *AgentContext, stepCtx *StepContext, workDir string, vars TemplateVars) string {
	// Collected top-down (step, agent, global), then reversed for output.
	layers := make([]layer, 0, 3)
	replaced := false
	if stepCtx != nil && len(stepCtx.Paths) > 0 {
		mode := normalizeMode(stepCtx.Mode, "step")
		layers = append(layers, layer{entries: filterScopedEntries(stepCtx.Paths, vars, workDir), mode: mode})
		replaced = mode == ModeReplace
	}
	if !replaced && agentCtx != nil && len(agentCtx.Paths) > 0 {
		mode := normalizeMode(agentCtx.Mode, "agent")
		layers = append(layers, layer{entries: filterScopedEntries(agentCtx.Paths, vars, workDir), mode: mode})
		replaced = mode == ModeReplace
	}
	if !replaced {
		layers = append(layers, layer{entries: globalPaths, mode: ModeAppend})
	}
	slices.Reverse(layers)
	return resolveMemoized(memoKey(workDir, layers), workDir, layers)
}

// resolveMemoized returns the cached string for key, computing it at most
// once per process; concurrent callers for the same key block on the
// first computation instead of duplicating I/O.
func resolveMemoized(key, workDir string, layers []layer) string {
	if v, ok := resolveCache.Load(key); ok {
		return v.(string)
	}
	v, _, _ := resolveGroup.Do(key, func() (any, error) {
		if cached, ok := resolveCache.Load(key); ok {
			return cached, nil
		}
		s := resolveLayers(workDir, layers)
		resolveCache.Store(key, s)
		return s, nil
	})
	return v.(string)
}

// memoKey digests workDir plus, per layer, the mode and the sorted
// absolute entry list (trailing-slash recursion markers preserved).
// Encoding the resolved path set — not an agent name — is what keeps the
// key stable across config reshuffles that leave the effective paths
// unchanged (design D2).
func memoKey(workDir string, layers []layer) string {
	h := sha256.New()
	io.WriteString(h, workDir)
	for _, l := range layers {
		io.WriteString(h, "\x00layer\x00")
		io.WriteString(h, string(l.mode))
		abs := make([]string, 0, len(l.entries))
		for _, e := range l.entries {
			p := filepath.Join(workDir, e)
			if strings.HasSuffix(e, "/") {
				p += "/"
			}
			abs = append(abs, p)
		}
		sort.Strings(abs)
		for _, p := range abs {
			io.WriteString(h, "\x00")
			io.WriteString(h, p)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveLayers renders each layer with the preserved single-layer
// semantics (sort-by-absolute-path, "# From:" headers, silent skip) and a
// dedup map SHARED across layers, then joins non-empty layer blocks with
// a single "\n" — the same joiner used between files within a layer.
func resolveLayers(workDir string, layers []layer) string {
	processed := make(map[string]bool)
	var mu sync.Mutex
	blocks := make([]string, 0, len(layers))
	for _, l := range layers {
		if block := resolveLayer(workDir, l.entries, processed, &mu); block != "" {
			blocks = append(blocks, block)
		}
	}
	return strings.Join(blocks, "\n")
}

type contextEntry struct {
	path    string
	content string
}

// resolveLayer is the moved body of the former
// internal/llm/prompt.processContextPaths, with the dedup map lifted to a
// parameter so it can be shared across layers. Every observable behavior
// is preserved: trailing-slash entries recurse via WalkDir, file entries
// silently skip missing files, entries sort by absolute path, and files
// join with a single "\n".
func resolveLayer(workDir string, paths []string, processed map[string]bool, processedMu *sync.Mutex) string {
	var (
		wg       sync.WaitGroup
		resultCh = make(chan contextEntry)
	)

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
						if tryMarkProcessed(path, processed, processedMu) {
							if content := processFile(path); content != "" {
								resultCh <- contextEntry{path: path, content: content}
							}
						}
					}
					return nil
				})
			} else {
				fullPath := filepath.Join(workDir, p)
				if tryMarkProcessed(fullPath, processed, processedMu) {
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

// normalizeMode maps a declared mode string to a Mode. Empty means the
// default replace (the field only exists when the user opted in to
// overriding, and the motivating requirement is exclusion). An
// unrecognized value warns once per (layer, value) and falls back to
// append — a typo must never silently drop the project's root
// instructions (design D4).
func normalizeMode(raw, layerName string) Mode {
	switch raw {
	case "", string(ModeReplace):
		return ModeReplace
	case string(ModeAppend):
		return ModeAppend
	default:
		if _, seen := modeWarned.LoadOrStore(layerName+"\x00"+raw, struct{}{}); !seen {
			logging.Warn("Unrecognized context mode, falling back to append", "layer", layerName, "mode", raw)
		}
		return ModeAppend
	}
}

// filterScopedEntries template-expands and containment-checks the entries
// of a declared (agent or step) layer, dropping the ones that fail either
// check. The global layer never passes through here — its entries are
// consumed verbatim for byte-compatibility with the pre-feature resolver.
func filterScopedEntries(entries []string, vars TemplateVars, workDir string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		expanded, ok := expandTokens(e, vars)
		if !ok {
			continue
		}
		if !containedInWorkDir(expanded, workDir) {
			continue
		}
		out = append(out, expanded)
	}
	return out
}

// expandTokens substitutes ${agent}, ${flow.id}, ${flow.step}, and
// ${env.VAR} in a context path entry. No shell execution, no glob
// expansion, no recursive substitution. Returns ok=false (with a DEBUG
// log) when the entry contains an unknown token, retains a literal ${...}
// residue after substitution, or when any recognized token expands to an
// empty value — e.g. AGENTS.${flow.id}.md outside a flow must skip, not
// probe AGENTS..md on disk (design D5).
func expandTokens(entry string, vars TemplateVars) (string, bool) {
	skipReason := ""
	expanded := tokenPattern.ReplaceAllStringFunc(entry, func(tok string) string {
		name := tok[2 : len(tok)-1]
		var value string
		switch {
		case name == "agent":
			value = vars.Agent
		case name == "flow.id":
			value = vars.FlowID
		case name == "flow.step":
			value = vars.FlowStep
		case strings.HasPrefix(name, "env."):
			value = os.Getenv(strings.TrimPrefix(name, "env."))
		default:
			if skipReason == "" {
				skipReason = "unknown token " + tok
			}
			return tok
		}
		if value == "" && skipReason == "" {
			skipReason = "empty value for " + tok
		}
		return value
	})
	if skipReason == "" && strings.Contains(expanded, "${") {
		skipReason = "unresolved ${...} residue"
	}
	if skipReason != "" {
		logging.Debug("Skipping context path entry", "entry", entry, "reason", skipReason)
		return "", false
	}
	return expanded, true
}

// containedInWorkDir rejects (with a WARN naming entry and resolved path)
// any entry whose joined, cleaned, symlink-resolved absolute path is not
// strictly inside workDir. Symlink chains cannot bypass it: when the path
// exists, the comparison runs on EvalSymlinks output for both sides; when
// it does not exist the lexical comparison suffices, because a dangling
// path is silently skipped by the reader anyway.
func containedInWorkDir(entry, workDir string) bool {
	cleaned := filepath.Clean(filepath.Join(workDir, entry))
	cleanRoot := filepath.Clean(workDir)

	inside := false
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		if resolvedRoot, rootErr := filepath.EvalSymlinks(cleanRoot); rootErr == nil {
			inside = strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator))
		} else {
			inside = strings.HasPrefix(cleaned, cleanRoot+string(os.PathSeparator))
		}
	} else {
		inside = strings.HasPrefix(cleaned, cleanRoot+string(os.PathSeparator))
	}
	if !inside {
		logging.Warn("Context path entry escapes the working directory, rejecting", "entry", entry, "resolved", cleaned, "workDir", cleanRoot)
	}
	return inside
}
