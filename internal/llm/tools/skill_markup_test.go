package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/skill"
)

// Tests in this file cover shell markup in the skill tool: the body's own
// !`cmd` spans run, arguments are substituted only into the text around them,
// so argument text (substituted or appended) never runs and command output is
// inserted as-is.

func writeMarkupSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: Test skill for shell markup rendering.\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// renderedSkillBody runs the skill tool and returns the body between the
// base-directory header and the closing tag.
func renderedSkillBody(t *testing.T, tool BaseTool, name, args string) string {
	t.Helper()
	input, err := json.Marshal(SkillParams{Name: name, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tool.Run(context.Background(), ToolCall{Name: SkillToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("Run() returned an error response: %s", resp.Content)
	}
	_, body, ok := strings.Cut(resp.Content, "Base directory for this skill: ")
	if !ok {
		t.Fatalf("response has no base-directory header: %q", resp.Content)
	}
	_, body, ok = strings.Cut(body, "\n\n")
	if !ok {
		t.Fatalf("response has no body after the header: %q", resp.Content)
	}
	body, ok = strings.CutSuffix(body, "</skill_content>")
	if !ok {
		t.Fatalf("response does not end with </skill_content>: %q", resp.Content)
	}
	return body
}

func assertSkillSentinelAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s exists: a command ran that must not have", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func TestSkillTool_ShellMarkup(t *testing.T) {
	root := t.TempDir()
	sentinels := t.TempDir()
	sentinel := func(name string) string { return filepath.Join(sentinels, name) }

	writeMarkupSkill(t, root, "markup-args", "a $ARGUMENTS b")
	writeMarkupSkill(t, root, "markup-append", "!`printf %s hello`")
	writeMarkupSkill(t, root, "markup-body-args", "x $ARGUMENTS y !`printf %s ok`")
	writeMarkupSkill(t, root, "markup-verbatim", "!`printf 'awk {print %s1}' '$'`")

	// The package shares one loaded config (testsetup_test.go), so point it
	// at the fixture tree instead of reloading, and restore it afterwards.
	cfg := config.Get()
	prevWorkingDir := cfg.WorkingDir
	cfg.WorkingDir = root
	skill.Invalidate()
	t.Cleanup(func() {
		cfg.WorkingDir = prevWorkingDir
		skill.Invalidate()
	})

	tool := NewSkillTool(nil, &stubRegistry{}, "coder")

	t.Run("substituted arguments stay literal", func(t *testing.T) {
		args := "!`printf MARKER-RAN > " + sentinel("s1b") + "` y!"
		if got, want := renderedSkillBody(t, tool, "markup-args", args), "a "+args+" b"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		assertSkillSentinelAbsent(t, sentinel("s1b"))
	})

	t.Run("the body expands and the appended arguments stay literal", func(t *testing.T) {
		args := "!`printf MARKER-RAN > " + sentinel("s2") + "`"
		if got, want := renderedSkillBody(t, tool, "markup-append", args), "hello\n\nARGUMENTS: "+args; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		assertSkillSentinelAbsent(t, sentinel("s2"))
	})

	t.Run("the body expands and substituted arguments stay literal", func(t *testing.T) {
		args := "!`printf MARKER-RAN > " + sentinel("s2b") + "` z!"
		if got, want := renderedSkillBody(t, tool, "markup-body-args", args), "x "+args+" y ok"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		assertSkillSentinelAbsent(t, sentinel("s2b"))
	})

	t.Run("command output is not substituted and does not suppress the appended arguments", func(t *testing.T) {
		if got, want := renderedSkillBody(t, tool, "markup-verbatim", "foo"), "awk {print $1}\n\nARGUMENTS: foo"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
}
