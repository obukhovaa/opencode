package slashcmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/skill"
)

// MaxInvocationsPerMessage bounds how many invocations a single message may
// expand. A pasted block of slash-prefixed lines outside a code fence would
// otherwise inline one skill body per line into a single prompt. The limit sits
// far above any hand-authored turn and is enforced as an error rather than a
// silent truncation.
const MaxInvocationsPerMessage = 10

var (
	// ErrActionNotComposable is returned when an action command (one that
	// performs an application action rather than contributing prompt text) is
	// submitted alongside any other content.
	ErrActionNotComposable = errors.New("command cannot be combined with other content")
	// ErrTooManyInvocations is returned when a message exceeds
	// MaxInvocationsPerMessage.
	ErrTooManyInvocations = errors.New("too many slash commands in one message")
)

// Kind classifies a scanned invocation.
type Kind int

const (
	// KindUnresolved means the token matched no command or skill; the line is
	// left in the message verbatim.
	KindUnresolved Kind = iota
	// KindAction means the invocation performs an application action.
	KindAction
	// KindPrompt means the invocation contributes text to the user's message.
	KindPrompt
)

// Registry is the set of commands and skills an invocation may resolve against.
type Registry struct {
	Commands []CommandInfo
	Skills   []skill.Info
}

// Invocation is one slash-prefixed line found by Scan.
type Invocation struct {
	// Line is the 0-based index of the line within the scanned text.
	Line int
	// Escaped is true for a line starting with `\/`: it is emitted with the
	// backslash removed and is never resolved.
	Escaped bool
	Kind    Kind
	// Name is the token as typed, without the leading slash — "review" or
	// "skill:review".
	Name string
	// Args is the remainder of the line, trimmed.
	Args string
	// Command is set when Kind is KindAction or KindPrompt and the invocation
	// resolved to a command; Skill is set when it resolved to a skill.
	Command *CommandInfo
	Skill   *skill.Info
	// Err is set when the token resolved to a known name that may not be
	// invoked this way — currently a skill without `user-invocable: true`.
	Err error
}

// ExpandOptions carries the per-call context Expand needs.
type ExpandOptions struct {
	// SessionID substitutes ${SESSION_ID} in expanded content.
	SessionID string
	// Interactive is false for prompts supplied outside the TUI, where
	// TUI-only commands are rejected instead of run.
	Interactive bool
	// ShellExpand expands !`cmd` markup inside expanded content. It is injected
	// rather than imported so this package stays free of the config and shell
	// dependencies and so tests can expand without forking a process. A nil
	// hook disables shell markup expansion entirely.
	ShellExpand func(string) string
}

// Expansion is the result of expanding a submitted message.
type Expansion struct {
	// Prompt is the message to send. It is empty when Action is set.
	Prompt string
	// Action is set when the message was exactly one action command, in which
	// case nothing is sent and the caller runs the command's action instead.
	Action *CommandInfo
	// ActionArgs is the remainder of the action command's line. Callers whose
	// action commands collect their own arguments (/rename, /loop) ignore it.
	ActionArgs string
	// Count is the number of prompt invocations expanded.
	Count int
}

// Scan finds the slash invocations in text and classifies each one.
//
// A line is a candidate when its first character is a slash at column 0 and it
// is not inside a fenced code block. Requiring column 0, an unfenced line, and
// a token that resolves to an installed command is what keeps pasted prose and
// diffs from expanding; `\/` at the start of a line is the escape for the
// residual case.
func Scan(text string, reg Registry) []Invocation {
	var (
		invs   []Invocation
		fence  string // the marker that opened the current fence; "" when closed
		inCode bool
	)

	for i, line := range strings.Split(text, "\n") {
		if marker := fenceMarker(line); marker != "" {
			if !inCode {
				inCode, fence = true, marker
			} else if marker == fence {
				inCode, fence = false, ""
			}
			continue
		}
		if inCode {
			continue
		}
		if strings.HasPrefix(line, `\/`) {
			invs = append(invs, Invocation{Line: i, Escaped: true, Kind: KindUnresolved})
			continue
		}
		if !strings.HasPrefix(line, "/") {
			continue
		}

		parsed := Parse(line)
		if parsed == nil {
			continue
		}

		inv := Invocation{Line: i, Name: parsed.Name, Args: parsed.Args}
		if parsed.IsSkill {
			inv.Name = SkillPrefix + parsed.Name
		}

		action, err := Resolve(parsed, reg.Commands, reg.Skills, true)
		switch {
		case err != nil:
			inv.Err = err
		case action.Type == ActionCommand:
			inv.Command = action.Command
			if action.Command.IsAction() {
				inv.Kind = KindAction
			} else {
				inv.Kind = KindPrompt
			}
		case action.Type == ActionSkill:
			inv.Skill = action.Skill
			inv.Kind = KindPrompt
		}

		invs = append(invs, inv)
	}

	return invs
}

// fenceMarker returns the code-fence marker a line opens or closes, or "" when
// the line is not a fence delimiter.
func fenceMarker(line string) string {
	trimmed := strings.TrimSpace(line)
	for _, marker := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, marker) {
			return marker
		}
	}
	return ""
}

// Expand replaces every prompt invocation in text with its expanded content,
// in place, leaving all other text byte-identical.
//
// It returns an error, and no partial expansion, when the message cannot be
// sent as written: a skill that is not user-invocable, a TUI-only command in a
// non-interactive prompt, more than MaxInvocationsPerMessage invocations, or an
// action command combined with other content.
func Expand(text string, reg Registry, opts ExpandOptions) (Expansion, error) {
	invs := Scan(text, reg)

	var (
		actions  []Invocation
		prompts  int
		escapes  int
		expandAt = make(map[int]Invocation, len(invs))
	)

	for _, inv := range invs {
		if inv.Err != nil {
			return Expansion{}, inv.Err
		}
		if inv.Command != nil && inv.Command.TUIOnly && !opts.Interactive {
			return Expansion{}, fmt.Errorf("%w: '/%s'", ErrTUIOnly, inv.Command.ID)
		}

		switch {
		case inv.Escaped:
			escapes++
			expandAt[inv.Line] = inv
		case inv.Kind == KindAction:
			actions = append(actions, inv)
		case inv.Kind == KindPrompt:
			prompts++
			expandAt[inv.Line] = inv
		}
	}

	if len(actions) > 0 {
		if len(actions) == 1 && prompts == 0 && escapes == 0 && isBareAction(text, actions[0]) {
			return Expansion{Action: actions[0].Command, ActionArgs: actions[0].Args}, nil
		}
		return Expansion{}, fmt.Errorf("%w: '/%s'", ErrActionNotComposable, actions[0].Command.ID)
	}

	if prompts > MaxInvocationsPerMessage {
		return Expansion{}, fmt.Errorf("%w: %d found, at most %d per message", ErrTooManyInvocations, prompts, MaxInvocationsPerMessage)
	}

	if len(expandAt) == 0 {
		return Expansion{Prompt: text}, nil
	}

	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		inv, ok := expandAt[i]
		switch {
		case !ok:
			out = append(out, line)
		case inv.Escaped:
			out = append(out, line[1:])
		default:
			out = append(out, expandInvocation(inv, opts))
		}
	}

	return Expansion{Prompt: strings.Join(out, "\n"), Count: prompts}, nil
}

// isBareAction reports whether inv is the whole message, so its action can run
// instead of a message being sent.
//
// Every other line must be blank, and text on the invocation's own line counts
// as other content unless the command declares an argument hint — that hint is
// how an action command that takes arguments (/rename, /loop) is distinguished
// from one that does not (/new, /compact). Without the distinction,
// "/new and then look at the diff" would silently clear the session and discard
// the sentence, which is the surprise this change exists to remove.
func isBareAction(text string, inv Invocation) bool {
	if inv.Args != "" && inv.Command.ArgumentHint == "" {
		return false
	}
	for i, l := range strings.Split(text, "\n") {
		if i != inv.Line && strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// expandInvocation renders one prompt invocation: argument binding, then
// ${SKILL_DIR} / ${SESSION_ID} substitution, then shell markup, then — for a
// skill — the <skill_content> wrapper that tells the model the skill is already
// loaded so it does not re-invoke the skill tool.
func expandInvocation(inv Invocation, opts ExpandOptions) string {
	shell := opts.ShellExpand
	if shell == nil {
		shell = func(s string) string { return s }
	}

	if inv.Skill != nil {
		content := skill.SubstituteContent(inv.Skill.Content, skill.SubstituteParams{
			Args:      inv.Args,
			SkillDir:  filepath.Dir(inv.Skill.Location),
			SessionID: opts.SessionID,
		})
		return skill.WrapSkillContent(inv.Skill.Name, shell(content))
	}

	// Named placeholders bind positionally by first-appearance order, which is
	// the order the argument dialog collected them in. When the content declares
	// any of them the arguments are considered consumed, so SubstituteContent
	// must not also append an "ARGUMENTS:" line.
	names := NamedPlaceholders(inv.Command.Content)
	content := bindNamed(inv.Command.Content, names, skill.SplitArgs(inv.Args))
	content = skill.SubstituteContent(content, skill.SubstituteParams{
		Args:               inv.Args,
		SessionID:          opts.SessionID,
		SuppressArgsAppend: len(names) > 0,
	})
	return shell(content)
}
