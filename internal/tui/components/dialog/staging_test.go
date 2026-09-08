package dialog

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/skill"
)

func TestArgumentFields(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		allowNamed bool
		wantNames  []string
		wantMode   ArgsMode
		wantOK     bool
	}{
		{
			name:       "named placeholders in declaration order",
			content:    "Look at $TARGET inside $SCOPE.",
			allowNamed: true,
			wantNames:  []string{"TARGET", "SCOPE"},
			wantMode:   ArgsModePositional,
			wantOK:     true,
		},
		{
			name:       "positional placeholders",
			content:    "From $0 to $1.",
			allowNamed: true,
			wantNames:  []string{"0", "1"},
			wantMode:   ArgsModePositional,
			wantOK:     true,
		},
		{
			name:       "indexed arguments",
			content:    "Fix issue $ARGUMENTS[0].",
			allowNamed: true,
			wantNames:  []string{"0"},
			wantMode:   ArgsModePositional,
			wantOK:     true,
		},
		{
			name:       "bare $ARGUMENTS is a whole-string field",
			content:    "Review $ARGUMENTS carefully.",
			allowNamed: true,
			wantNames:  []string{"ARGUMENTS"},
			wantMode:   ArgsModeWhole,
			wantOK:     true,
		},
		{
			name:       "no placeholders",
			content:    "Commit the staged changes.",
			allowNamed: true,
			wantOK:     false,
		},
		{
			// Skills document only $ARGUMENTS/$N/${SKILL_DIR}/${SESSION_ID}, so a
			// shell reference in a skill body is not a parameter.
			name:       "skills do not treat shell references as parameters",
			content:    "Use $HOME/.config and $PATH.",
			allowNamed: false,
			wantOK:     false,
		},
		{
			name:       "commands do treat $FOO as a parameter",
			content:    "Use $HOME/.config.",
			allowNamed: true,
			wantNames:  []string{"HOME"},
			wantMode:   ArgsModePositional,
			wantOK:     true,
		},
		{
			name:       "named placeholders win over a co-declared $ARGUMENTS",
			content:    "Do $ARGUMENTS for $TARGET.",
			allowNamed: true,
			wantNames:  []string{"TARGET"},
			wantMode:   ArgsModePositional,
			wantOK:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names, _, mode, ok := argumentFields(tt.content, "", tt.allowNamed)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(names, tt.wantNames) {
				t.Errorf("names = %#v, want %#v", names, tt.wantNames)
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %v, want %v", mode, tt.wantMode)
			}
		})
	}
}

func TestStagedInvocationAndArgs(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		names  []string
		values []string
		mode   ArgsMode
		want   string
	}{
		{
			name: "no arguments leaves a trailing space to type into",
			id:   "skill:reviewer",
			want: "/skill:reviewer ",
		},
		{
			name:   "positional values are quoted individually",
			id:     "project:pos",
			names:  []string{"0", "1"},
			values: []string{"HEAD~3", "src/internal tools"},
			mode:   ArgsModePositional,
			want:   `/project:pos HEAD~3 "src/internal tools"`,
		},
		{
			// A skill may reference only $2. Its value has to be staged in slot
			// 2, or expansion binds it to nothing.
			name:   "non-contiguous indices are padded to their slots",
			id:     "skill:sparse",
			names:  []string{"2"},
			values: []string{"third"},
			mode:   ArgsModePositional,
			want:   `/skill:sparse "" "" third`,
		},
		{
			name:   "indices starting above zero keep their order",
			id:     "skill:sparse",
			names:  []string{"1", "3"},
			values: []string{"one", "three"},
			mode:   ArgsModePositional,
			want:   `/skill:sparse "" one "" three`,
		},
		{
			name:   "named values stage positionally",
			id:     "project:scope",
			names:  []string{"TARGET", "SCOPE"},
			values: []string{"HEAD~3", "src/"},
			mode:   ArgsModePositional,
			want:   "/project:scope HEAD~3 src/",
		},
		{
			// $ARGUMENTS binds the whole unsplit string, so the value is staged
			// verbatim — quoting it would put quotes into the prompt.
			name:   "whole-string value is not quoted",
			id:     "review",
			values: []string{"the last two commits"},
			mode:   ArgsModeWhole,
			want:   "/review the last two commits",
		},
		{
			name:   "empty whole-string value stages a bare invocation",
			id:     "review",
			names:  []string{"ARGUMENTS"},
			values: []string{""},
			mode:   ArgsModeWhole,
			want:   "/review ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StagedInvocation(tt.id, StagedArgs(tt.names, tt.values, tt.mode))
			if got != tt.want {
				t.Errorf("staged text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStageCommandHandler(t *testing.T) {
	t.Run("a command with no placeholders stages directly", func(t *testing.T) {
		cmd := Command{}
		cmd.ID = "commit"
		cmd.Content = "Commit the staged changes."

		msg := StageCommandHandler(cmd)()
		stage, ok := msg.(StageInvocationMsg)
		if !ok {
			t.Fatalf("got %T, want StageInvocationMsg", msg)
		}
		if stage.Text != "/commit " {
			t.Errorf("staged text = %q, want %q", stage.Text, "/commit ")
		}
	})

	t.Run("a parameterized command opens the dialog", func(t *testing.T) {
		cmd := Command{}
		cmd.ID = "project:scope"
		cmd.Content = "Look at $TARGET inside $SCOPE."

		msg := StageCommandHandler(cmd)()
		dlg, ok := msg.(ShowMultiArgumentsDialogMsg)
		if !ok {
			t.Fatalf("got %T, want ShowMultiArgumentsDialogMsg", msg)
		}
		if dlg.CommandID != "project:scope" {
			t.Errorf("CommandID = %q", dlg.CommandID)
		}
		if !reflect.DeepEqual(dlg.ArgNames, []string{"TARGET", "SCOPE"}) {
			t.Errorf("ArgNames = %#v", dlg.ArgNames)
		}
		if dlg.Mode != ArgsModePositional {
			t.Errorf("Mode = %v, want positional", dlg.Mode)
		}
	})
}

func TestStageSkillHandlerStagesBareSkill(t *testing.T) {
	s := &skill.Info{Name: "reviewer", Content: "Review the diff."}
	msg := StageSkillHandler(s)()
	stage, ok := msg.(StageInvocationMsg)
	if !ok {
		t.Fatalf("got %T, want StageInvocationMsg", msg)
	}
	if stage.Text != "/skill:reviewer " {
		t.Errorf("staged text = %q, want %q", stage.Text, "/skill:reviewer ")
	}
}

// typeInto feeds each rune of value into the dialog as a key press.
func typeInto(m tea.Model, value string) tea.Model {
	for _, r := range value {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// TestMultiArgumentsDialogSubmitCarriesOrderedValues covers the whole dialog →
// staging chain: what the user typed must come back in field order, with the
// mode that decides how it is quoted.
func TestMultiArgumentsDialogSubmitCarriesOrderedValues(t *testing.T) {
	var m tea.Model = NewMultiArgumentsDialogCmp(
		"project:pos",
		"From $0 to $1.",
		[]string{"0", "1"},
		nil,
		ArgsModePositional,
	)

	m = typeInto(m, "HEAD~3")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = typeInto(m, "two words")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting the last field returned no cmd")
	}
	closeMsg, ok := cmd().(CloseMultiArgumentsDialogMsg)
	if !ok {
		t.Fatalf("got %T, want CloseMultiArgumentsDialogMsg", cmd())
	}
	if !closeMsg.Submit {
		t.Fatal("Submit = false")
	}
	if !reflect.DeepEqual(closeMsg.Values, []string{"HEAD~3", "two words"}) {
		t.Fatalf("Values = %#v", closeMsg.Values)
	}
	if closeMsg.Mode != ArgsModePositional {
		t.Errorf("Mode = %v, want positional", closeMsg.Mode)
	}

	staged := StagedInvocation(closeMsg.CommandID, StagedArgs(closeMsg.ArgNames, closeMsg.Values, closeMsg.Mode))
	if staged != `/project:pos HEAD~3 "two words"` {
		t.Errorf("staged text = %q", staged)
	}
}

// TestMultiArgumentsDialogCancelStagesNothing: esc must not produce a staged
// invocation, so the editor is left exactly as the user had it.
func TestMultiArgumentsDialogCancelStagesNothing(t *testing.T) {
	var m tea.Model = NewMultiArgumentsDialogCmp(
		"review",
		"Review $ARGUMENTS carefully.",
		[]string{"ARGUMENTS"},
		nil,
		ArgsModeWhole,
	)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc returned no cmd")
	}
	closeMsg, ok := cmd().(CloseMultiArgumentsDialogMsg)
	if !ok {
		t.Fatalf("got %T, want CloseMultiArgumentsDialogMsg", cmd())
	}
	if closeMsg.Submit {
		t.Error("Submit = true after esc")
	}
	if closeMsg.Values != nil {
		t.Errorf("Values = %#v after esc, want nil", closeMsg.Values)
	}
}
