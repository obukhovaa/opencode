// Package clitool implements workspace-defined CLI tools: declarative
// manifests (`.agents/tools/<name>.yaml`) that wrap a host binary as a
// first-class opencode tool. The package owns the manifest format, its
// strict loading and validation, discovery across workspace and global
// directories, the argument policy, structured-template rendering and the
// argv-only executor. It deliberately knows nothing about agents, sessions
// or the LLM tool interface: `internal/llm/tools` wraps a Manifest into a
// BaseTool and `internal/clitool/mcpserve` serves the same manifests over
// MCP, so both paths share one policy implementation.
//
// See docs/cli-tools.md and openspec/specs/workspace-cli-tools/spec.md.
package clitool

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode selects how the model supplies arguments.
type Mode string

// Grant selects how an agent receives the tool.
type Grant string

const (
	// ModeArgv exposes a verbatim `args: string[]` parameter; the schema never
	// enumerates the binary's subcommands or flags.
	ModeArgv Mode = "argv"
	// ModeStructured exposes the manifest's own JSON-Schema parameters and
	// renders them onto a fixed argv template.
	ModeStructured Mode = "structured"

	// GrantExplicit (default) requires the agent to name the tool in `tools`
	// or `allowTools`; a bare "*" does not grant it.
	GrantExplicit Grant = "explicit"
	// GrantImplicit follows the ordinary deny-list semantics of `tools`.
	GrantImplicit Grant = "implicit"

	// DefaultTimeout bounds a call when neither the manifest nor the call
	// sets one.
	DefaultTimeout = 2 * time.Minute
	// DefaultMaxTimeout caps the per-call `timeout` parameter.
	DefaultMaxTimeout = 10 * time.Minute
	// DefaultMaxOutputBytes matches the bash / MCP / webfetch caps.
	DefaultMaxOutputBytes = 50 * 1024
	// DefaultHelpMaxBytes bounds a captured `--help` block.
	DefaultHelpMaxBytes = 4096
	// MaxDescriptionBytes bounds the author-written description.
	MaxDescriptionBytes = 4096
	// MaxNameLength bounds the tool name.
	MaxNameLength = 64
	// helpCaptureTimeout bounds the one-off `--help` run at load time.
	helpCaptureTimeout = 10 * time.Second
)

// nameRe is the tool-name grammar: lowercase because viper lowercases the
// keys of `tools` / `deferredTools` maps loaded from .opencode.json, so an
// uppercase name could never be granted from JSON config.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// placeholderRe matches `{param}` references inside a structured template.
var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ReservedNames are tool names a manifest may not take: the built-in tools
// of internal/llm/tools and internal/llm/agent plus the engine-injected
// ones. A test in internal/llm/agent keeps this list in step with the
// constants it mirrors (clitool cannot import that package: it would be a
// cycle, since the tools package wraps manifests).
var ReservedNames = map[string]bool{
	"bash": true, "edit": true, "multiedit": true, "write": true, "read": true,
	"glob": true, "grep": true, "ls": true, "delete": true, "patch": true,
	"view_image": true, "webfetch": true, "websearch": true, "sourcegraph": true,
	"lsp": true, "skill": true, "struct_output": true, "toolsearch": true,
	"question": true, "todowrite": true, "monitor": true, "tasklist": true,
	"taskstop": true, "croncreate": true, "crondelete": true, "cronlist": true,
	"router_send": true, "heartbeat": true, "task": true,
}

// ArgsPolicy is the hard argument policy. Deny patterns are evaluated first,
// against every single argument and against the space-joined vector; allow
// patterns (when any) must match the joined vector. Patterns use the same
// `*` wildcard as tool permissions.
type ArgsPolicy struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// EnvPolicy shapes the child environment.
type EnvPolicy struct {
	// Inherit (default true) passes the parent environment through. When
	// false only PATH, HOME, TMPDIR and Pass survive.
	Inherit *bool `yaml:"inherit"`
	// Pass lists variable names kept when Inherit is false.
	Pass []string `yaml:"pass"`
	// Set adds literal variables last; values may use `${env.NAME}` tokens,
	// expanded from the parent environment (unknown tokens expand to "").
	Set map[string]string `yaml:"set"`
}

// HelpSpec declares the optional one-off `--help` capture appended to the
// tool description at load time.
type HelpSpec struct {
	Args     []string `yaml:"args"`
	MaxBytes int      `yaml:"maxBytes"`
}

// Manifest is one CLI tool definition. Exported fields map 1:1 to the YAML
// surface; the unexported ones are resolved at load.
type Manifest struct {
	Name           string         `yaml:"name"`
	Description    string         `yaml:"description"`
	Command        string         `yaml:"command"`
	Mode           Mode           `yaml:"mode"`
	PrefixArgs     []string       `yaml:"prefixArgs"`
	Args           ArgsPolicy     `yaml:"args"`
	Stdin          bool           `yaml:"stdin"`
	Env            EnvPolicy      `yaml:"env"`
	Cwd            string         `yaml:"cwd"`
	Timeout        string         `yaml:"timeout"`
	MaxTimeout     string         `yaml:"maxTimeout"`
	MaxOutputBytes *int           `yaml:"maxOutputBytes"`
	Help           *HelpSpec      `yaml:"help"`
	Grant          Grant          `yaml:"grant"`
	Permission     any            `yaml:"permission"`
	Parameters     map[string]any `yaml:"parameters"`
	Required       []string       `yaml:"required"`
	Argv           []any          `yaml:"argv"`

	// Location is the manifest file the tool was loaded from.
	Location string `yaml:"-"`
	// WorkingDir is the opencode working directory the manifest was resolved
	// against (cwd confinement, relative command paths).
	WorkingDir string `yaml:"-"`
	// ResolvedCommand is the absolute path of the binary, or "" when it could
	// not be found at load time (the tool still loads; calls fail clearly).
	ResolvedCommand string `yaml:"-"`
	// ResolvedCwd is the absolute working directory for the child process.
	ResolvedCwd string `yaml:"-"`
	// HelpText is the captured `--help` output, "" when not captured.
	HelpText string `yaml:"-"`

	timeout    time.Duration
	maxTimeout time.Duration
	maxOutput  int
	permission map[string]any
	template   []templateEntry
	paramTypes map[string]string
}

// templateEntry is one parsed element of a structured `argv` template.
type templateEntry struct {
	literal string
	group   []templateEntry
	isGroup bool
	refs    []string // placeholders referenced by this entry (or group)
}

// Load reads, decodes and validates one manifest. workingDir is the opencode
// working directory. The returned error names every problem found.
func Load(path, workingDir string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, path, workingDir)
}

// Parse decodes and validates manifest bytes. location is recorded for
// diagnostics and the basename check.
func Parse(raw []byte, location, workingDir string) (*Manifest, error) {
	m := &Manifest{Location: location, WorkingDir: workingDir}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// Strict: a misspelt security field (`alow:`) must fail, not be ignored.
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := m.resolve(); err != nil {
		return nil, err
	}
	return m, nil
}

// resolve applies defaults and validates. Every problem is collected so the
// author fixes a manifest in one pass.
func (m *Manifest) resolve() error {
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	// --- identity ---------------------------------------------------------
	if m.Name == "" {
		fail("name is required")
	} else if !nameRe.MatchString(m.Name) {
		fail("name %q must match %s", m.Name, nameRe.String())
	} else if ReservedNames[m.Name] {
		fail("name %q is reserved for a built-in tool", m.Name)
	}
	if m.Location != "" && m.Name != "" {
		base := strings.TrimSuffix(filepath.Base(m.Location), filepath.Ext(m.Location))
		if base != m.Name {
			fail("file basename %q must equal name %q", base, m.Name)
		}
	}
	if strings.TrimSpace(m.Description) == "" {
		fail("description is required")
	} else if len(m.Description) > MaxDescriptionBytes {
		fail("description is %d bytes; the limit is %d", len(m.Description), MaxDescriptionBytes)
	}
	if strings.TrimSpace(m.Command) == "" {
		fail("command is required")
	}

	// --- enums --------------------------------------------------------------
	switch m.Mode {
	case "":
		m.Mode = ModeArgv
	case ModeArgv, ModeStructured:
	default:
		fail("mode %q must be %q or %q", m.Mode, ModeArgv, ModeStructured)
	}
	switch m.Grant {
	case "":
		m.Grant = GrantExplicit
	case GrantExplicit, GrantImplicit:
	default:
		fail("grant %q must be %q or %q", m.Grant, GrantExplicit, GrantImplicit)
	}

	// --- patterns -----------------------------------------------------------
	for _, p := range m.Args.Allow {
		if strings.TrimSpace(p) == "" {
			fail("args.allow contains an empty pattern")
		}
	}
	for _, p := range m.Args.Deny {
		if strings.TrimSpace(p) == "" {
			fail("args.deny contains an empty pattern")
		}
	}
	for _, a := range m.PrefixArgs {
		if strings.ContainsRune(a, 0) {
			fail("prefixArgs contains a NUL byte")
		}
	}

	// --- durations and caps -------------------------------------------------
	var err error
	if m.timeout, err = parseDuration(m.Timeout, DefaultTimeout); err != nil {
		fail("timeout: %v", err)
	}
	if m.maxTimeout, err = parseDuration(m.MaxTimeout, DefaultMaxTimeout); err != nil {
		fail("maxTimeout: %v", err)
	}
	if m.timeout <= 0 {
		fail("timeout must be positive")
	}
	if m.maxTimeout < m.timeout {
		fail("maxTimeout (%s) must not be smaller than timeout (%s)", m.maxTimeout, m.timeout)
	}
	switch {
	case m.MaxOutputBytes == nil || *m.MaxOutputBytes == 0:
		m.maxOutput = DefaultMaxOutputBytes
	case *m.MaxOutputBytes < 0:
		m.maxOutput = -1
	default:
		m.maxOutput = *m.MaxOutputBytes
	}
	if m.Help != nil {
		if len(m.Help.Args) == 0 {
			fail("help.args must list at least one argument (e.g. [\"--help\"])")
		}
		if m.Help.MaxBytes <= 0 {
			m.Help.MaxBytes = DefaultHelpMaxBytes
		}
	}
	for k := range m.Env.Set {
		if k == "" || strings.ContainsAny(k, "= \t\n") {
			fail("env.set has an invalid variable name %q", k)
		}
	}

	// --- permission default -------------------------------------------------
	if m.permission, err = normalizePermission(m.Permission); err != nil {
		fail("permission: %v", err)
	}

	// --- structured mode ----------------------------------------------------
	if m.Mode == ModeStructured {
		m.paramTypes = map[string]string{}
		for name, raw := range m.Parameters {
			if !placeholderNameOK(name) {
				fail("parameters: %q is not a valid parameter name", name)
				continue
			}
			schema, ok := raw.(map[string]any)
			if !ok {
				fail("parameters.%s must be a JSON-Schema object", name)
				continue
			}
			typ, _ := schema["type"].(string)
			switch typ {
			case "string", "integer", "number", "boolean", "array":
			case "":
				typ = "string"
				schema["type"] = "string"
			default:
				fail("parameters.%s: unsupported type %q (string, integer, number, boolean, array)", name, typ)
			}
			if typ == "array" {
				items, _ := schema["items"].(map[string]any)
				if it, _ := items["type"].(string); it != "string" {
					fail("parameters.%s: array parameters must declare items.type: string", name)
				}
			}
			m.paramTypes[name] = typ
		}
		for _, r := range m.Required {
			if _, ok := m.Parameters[r]; !ok {
				fail("required names unknown parameter %q", r)
			}
		}
		if len(m.Argv) == 0 {
			fail("structured mode needs an argv template")
		}
		tmpl, refs, terr := parseTemplate(m.Argv, m.paramTypes, false)
		if terr != nil {
			fail("argv: %v", terr)
		}
		m.template = tmpl
		for _, r := range refs {
			if _, ok := m.Parameters[r]; !ok {
				fail("argv references undeclared parameter {%s}", r)
			}
		}
	} else {
		if len(m.Parameters) > 0 || len(m.Required) > 0 || len(m.Argv) > 0 {
			fail("parameters, required and argv are only valid with mode: structured")
		}
	}

	// --- filesystem ---------------------------------------------------------
	if m.WorkingDir != "" {
		cwd, cerr := resolveCwd(m.Cwd, m.WorkingDir)
		if cerr != nil {
			fail("cwd: %v", cerr)
		}
		m.ResolvedCwd = cwd
	}
	if m.Command != "" {
		m.ResolvedCommand = resolveCommand(m.Command, m.WorkingDir)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// ResolveCommand re-resolves the binary (a call-time retry for a manifest
// whose command was missing at load).
func (m *Manifest) ResolveCommand() string {
	if m.ResolvedCommand == "" {
		m.ResolvedCommand = resolveCommand(m.Command, m.WorkingDir)
	}
	return m.ResolvedCommand
}

// resolveCommand turns `command` into an absolute path, or "" when it cannot
// be found. A bare name is looked up on PATH; anything containing a path
// separator is taken as a path, relative ones against the working directory.
func resolveCommand(command, workingDir string) string {
	if strings.ContainsRune(command, os.PathSeparator) || strings.HasPrefix(command, ".") {
		p := command
		if !filepath.IsAbs(p) {
			p = filepath.Join(workingDir, p)
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
		return ""
	}
	p, err := exec.LookPath(command)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// resolveCwd confines the child's working directory to the opencode working
// directory (symlinks resolved on both sides).
func resolveCwd(cwd, workingDir string) (string, error) {
	root, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		root = filepath.Clean(workingDir)
	}
	if strings.TrimSpace(cwd) == "" {
		return root, nil
	}
	p := cwd
	if !filepath.IsAbs(p) {
		p = filepath.Join(workingDir, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%q is not an existing directory inside the working directory", cwd)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%q is not an existing directory inside the working directory", cwd)
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%q resolves outside the working directory", cwd)
	}
	return resolved, nil
}

// parseDuration accepts a Go duration ("2m", "90s") or a bare number of
// seconds; "" yields def.
func parseDuration(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("%q is not a duration (use e.g. \"2m\", \"90s\" or a number of seconds)", s)
}

// normalizePermission turns the `permission` field into the pattern-map
// shape the permission evaluator understands: a bare action string becomes
// {"*": action}; a map must hold string actions.
func normalizePermission(v any) (map[string]any, error) {
	switch p := v.(type) {
	case nil:
		return nil, nil
	case string:
		if !validAction(p) {
			return nil, fmt.Errorf("%q is not allow, ask or deny", p)
		}
		return map[string]any{"*": strings.ToLower(p)}, nil
	case map[string]any:
		out := make(map[string]any, len(p))
		for k, raw := range p {
			s, ok := raw.(string)
			if !ok || !validAction(s) {
				return nil, fmt.Errorf("%q must map to allow, ask or deny", k)
			}
			if strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("empty pattern")
			}
			out[k] = strings.ToLower(s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an action string or a pattern map")
	}
}

func validAction(s string) bool {
	switch strings.ToLower(s) {
	case "allow", "ask", "deny":
		return true
	}
	return false
}

func placeholderNameOK(name string) bool {
	return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name)
}

// --- accessors used by the tool wrapper, the MCP server and `tools list` ---

// TimeoutDefault is the per-call timeout when the call sets none.
func (m *Manifest) TimeoutDefault() time.Duration { return m.timeout }

// TimeoutMax caps the per-call `timeout` parameter.
func (m *Manifest) TimeoutMax() time.Duration { return m.maxTimeout }

// MaxOutput is the context cap in bytes; -1 means unbounded.
func (m *Manifest) MaxOutput() int { return m.maxOutput }

// PermissionDefault is the manifest's default permission layer (nil when
// unset), in the pattern-map shape of agent permission values.
func (m *Manifest) PermissionDefault() map[string]any { return m.permission }

// EnvInherit reports whether the child inherits the parent environment.
func (m *Manifest) EnvInherit() bool { return m.Env.Inherit == nil || *m.Env.Inherit }

// CommandFound reports whether the binary resolved.
func (m *Manifest) CommandFound() bool { return m.ResolvedCommand != "" }

// FullDescription is what the model reads: the author text, a mode note and
// the captured help block when present.
func (m *Manifest) FullDescription() string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(m.Description))
	switch m.Mode {
	case ModeArgv:
		fmt.Fprintf(&sb, "\n\nRuns the `%s` binary directly. `args` are passed verbatim as separate argv entries — no shell is involved, so quoting, pipes, `&&`, redirects and `$VAR` are not interpreted; put each argument in its own entry.", filepath.Base(m.Command))
		if len(m.Args.Deny) > 0 || len(m.Args.Allow) > 0 {
			sb.WriteString(" Arguments outside the tool's policy are rejected before anything runs.")
		}
		if m.Stdin {
			sb.WriteString(" `stdin` is written to the process's standard input.")
		}
	case ModeStructured:
		fmt.Fprintf(&sb, "\n\nRuns the `%s` binary with a fixed argument template filled from the parameters; no shell is involved.", filepath.Base(m.Command))
	}
	if m.HelpText != "" {
		sb.WriteString("\n\nHelp output of the installed binary:\n```\n")
		sb.WriteString(m.HelpText)
		sb.WriteString("\n```")
	}
	return sb.String()
}

// InputSchema returns the JSON-Schema `properties` and `required` the tool
// advertises.
func (m *Manifest) InputSchema() (map[string]any, []string) {
	if m.Mode == ModeStructured {
		props := make(map[string]any, len(m.Parameters))
		for k, v := range m.Parameters {
			props[k] = v
		}
		req := append([]string(nil), m.Required...)
		if req == nil {
			req = []string{}
		}
		return props, req
	}
	props := map[string]any{
		"args": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Arguments for the binary, one argv entry per element, passed verbatim (no shell).",
		},
		"timeout": map[string]any{
			"type":        "integer",
			"description": fmt.Sprintf("Seconds before the process is killed (default %d, max %d).", int(m.timeout.Seconds()), int(m.maxTimeout.Seconds())),
		},
	}
	if m.Stdin {
		props["stdin"] = map[string]any{
			"type":        "string",
			"description": "Text written to the process's standard input.",
		}
	}
	return props, []string{"args"}
}
