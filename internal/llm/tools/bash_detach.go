package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Shell-shape scanning for two gates in the bash tool (openspec
// bash-background-mode: "Self-detaching commands are rejected in background
// mode" and "The permission-exempt safe list covers only simple commands").
//
// This is a conservative, quote- and subshell-aware scan — not a shell
// parser. It answers two questions about a command's TOP LEVEL (outside
// single/double quotes, backticks, `(...)` and `$(...)`): which control
// operators, redirects and substitution openers appear, and which words sit
// at command position. Anything the scan cannot classify (unbalanced quotes
// or parentheses) is reported as ambiguous, and each gate resolves ambiguity
// in its own direction: the detach gate lets an ambiguous command through
// (a false rejection blocks legitimate work), the safe-list gate treats it
// as not simple (a false exemption skips a permission check).

type shellShape struct {
	// operators lists top-level control operators, redirects and
	// substitution openers in source order: ";", "\n", "|", "||", "&",
	// "&&", ">", ">>", "<", "&>", "&>>", "$(", "`", "(".
	operators []string
	// commandWords lists the first word of every top-level simple command
	// (leading NAME=value assignments are skipped).
	commandWords []string
	// ambiguous is set when quotes, parentheses or backticks are unbalanced.
	ambiguous bool
}

var assignmentWordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func scanShellShape(cmd string) shellShape {
	var sh shellShape
	var (
		inSingle, inDouble, inBacktick bool
		depth                          int // (...) and $(...) nesting
		word                           strings.Builder
		atCmdPos                       = true
	)
	runes := []rune(cmd)
	topLevel := func() bool { return depth == 0 && !inBacktick }
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		w := word.String()
		word.Reset()
		if topLevel() && atCmdPos {
			if assignmentWordRe.MatchString(w) {
				return // FOO=bar cmd — the command word is still to come
			}
			sh.commandWords = append(sh.commandWords, w)
			atCmdPos = false
		}
	}
	op := func(o string) {
		flushWord()
		if topLevel() {
			sh.operators = append(sh.operators, o)
		}
	}
	prevSignificant := func(i int) rune {
		for j := i - 1; j >= 0; j-- {
			if runes[j] != ' ' && runes[j] != '\t' {
				return runes[j]
			}
		}
		return 0
	}
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				word.WriteRune(c)
			}
			continue
		case inDouble:
			switch {
			case c == '\\' && i+1 < len(runes):
				word.WriteRune(runes[i+1])
				i++
			case c == '"':
				inDouble = false
			case c == '$' && i+1 < len(runes) && runes[i+1] == '(':
				// "$(…)" runs a command even inside double quotes.
				if topLevel() {
					sh.operators = append(sh.operators, "$(")
				}
				depth++
				i++
			case c == '`':
				if topLevel() {
					sh.operators = append(sh.operators, "`")
				}
				inBacktick = true
			default:
				word.WriteRune(c)
			}
			continue
		case inBacktick:
			if c == '`' {
				inBacktick = false
			}
			continue
		}
		switch c {
		case '\\':
			if i+1 < len(runes) {
				word.WriteRune(runes[i+1])
				i++
			}
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			op("`")
			inBacktick = true
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				op("$(")
				depth++
				i++
			} else {
				word.WriteRune(c)
			}
		case '(':
			op("(")
			depth++
			atCmdPos = true
		case ')':
			flushWord()
			if depth == 0 {
				sh.ambiguous = true
			} else {
				depth--
			}
			atCmdPos = false
		case ';', '\n':
			op(string(c))
			atCmdPos = true
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				op("||")
				i++
			} else {
				op("|")
			}
			atCmdPos = true
		case '&':
			switch {
			case i+1 < len(runes) && runes[i+1] == '&':
				op("&&")
				i++
				atCmdPos = true
			case i+1 < len(runes) && runes[i+1] == '>':
				// `&>file` / `&>>file`: a redirect of both streams.
				o := "&>"
				i++
				if i+1 < len(runes) && runes[i+1] == '>' {
					o = "&>>"
					i++
				}
				op(o)
			case prevSignificant(i) == '>' || prevSignificant(i) == '<':
				// `2>&1`, `>&2`, `<&0`: the `&` belongs to the redirect.
				word.WriteRune(c)
			default:
				op("&")
				atCmdPos = true
			}
		case '>':
			if i+1 < len(runes) && runes[i+1] == '>' {
				op(">>")
				i++
			} else {
				op(">")
			}
		case '<':
			op("<")
		case ' ', '\t':
			flushWord()
		default:
			word.WriteRune(c)
		}
	}
	flushWord()
	if inSingle || inDouble || inBacktick || depth != 0 {
		sh.ambiguous = true
	}
	return sh
}

// detachingCommands detach the work they launch from the shell that
// run_in_background tracks.
var detachingCommands = map[string]bool{"nohup": true, "setsid": true, "disown": true}

// detectSelfDetach reports whether cmd detaches its own work from the
// wrapper shell a run_in_background task tracks, and names the construct.
// Detection is deliberately conservative: only a top-level `&` control
// operator (one that backgrounds the pipeline before it — `&&` and the
// redirects `2>&1` / `&>` are not control operators) or `nohup` / `setsid`
// / `disown` at command position count, and a command the scan cannot
// classify is allowed through. A false rejection blocks legitimate work; a
// false accept only reproduces the pre-gate behavior.
func detectSelfDetach(cmd string) (construct string, detaches bool) {
	sh := scanShellShape(cmd)
	if sh.ambiguous {
		return "", false
	}
	for _, w := range sh.commandWords {
		if detachingCommands[filepath.Base(w)] {
			return filepath.Base(w), true
		}
	}
	for _, o := range sh.operators {
		if o == "&" {
			return "&", true
		}
	}
	return "", false
}

// hasTopLevelCompound reports whether cmd is more than one simple command:
// any top-level control operator, redirect, command substitution or
// subshell. Ambiguity resolves to true — the caller (the permission-exempt
// safe list) must then evaluate the command like any other.
func hasTopLevelCompound(cmd string) bool {
	sh := scanShellShape(cmd)
	return sh.ambiguous || len(sh.operators) > 0
}

// selfDetachRejection is the error ToolResult text for a refused
// run_in_background command. It names the fix AND forecloses the
// foreground fallback: a detaching command run in the foreground is an
// untracked process that no completion, wait, tasklist or taskstop sees,
// and (before this gate) `nohup …` even skipped the permission check.
func selfDetachRejection(construct string) string {
	return fmt.Sprintf(
		"Rejected: the command detaches its own work (`%s`) while run_in_background is set. "+
			"run_in_background already runs the command as a tracked background task; a detached child "+
			"would outlive the task, and the task would report completion the moment the wrapper shell "+
			"exits — before the real work is done, with the wrong output. Remove the `%s` and keep "+
			"run_in_background: true so the task tracks the work itself. Do NOT fall back to running the "+
			"detaching command in the foreground: that starts an untracked process no completion "+
			"notification, wait, tasklist or taskstop can observe.",
		construct, construct)
}
