package slashcmd

import (
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/skill"
)

func boolPtr(b bool) *bool { return &b }

func TestResolve(t *testing.T) {
	commands := []CommandInfo{
		{ID: "commit", Title: "Commit"},
		{ID: "review", Title: "Review Code"},
		{ID: "compact", Title: "Compact Session", TUIOnly: true},
		{ID: "agents", Title: "List Agents", TUIOnly: true},
		{ID: "user:deploy", Title: "Deploy"},
		{ID: "project:lint", Title: "Lint"},
	}

	skills := []skill.Info{
		{Name: "git-release", Description: "Release helper", Content: "Release instructions"},
		{Name: "internal-codestyle", Description: "Code style", UserInvocable: boolPtr(false), Content: "Style guide"},
	}

	tests := []struct {
		name        string
		input       string
		interactive bool
		wantType    ActionType
		wantErr     error
		wantArgs    string
	}{
		{
			name:     "simple command match",
			input:    "/commit",
			wantType: ActionCommand,
		},
		{
			name:     "command with args",
			input:    "/review main",
			wantType: ActionCommand,
			wantArgs: "main",
		},
		{
			name:     "command by base name (strip prefix)",
			input:    "/deploy",
			wantType: ActionCommand,
		},
		{
			name:     "command by full ID with prefix",
			input:    "/user:deploy",
			wantType: ActionCommand,
		},
		{
			name:     "unrecognized command",
			input:    "/nonexistent",
			wantType: ActionNotFound,
		},
		{
			name:     "not a slash command",
			input:    "hello",
			wantType: ActionNotFound,
		},
		{
			name:     "user-invocable skill",
			input:    "/skill:git-release",
			wantType: ActionSkill,
			wantArgs: "",
		},
		{
			name:     "skill with args",
			input:    "/skill:git-release v2.1.0",
			wantType: ActionSkill,
			wantArgs: "v2.1.0",
		},
		{
			name:    "non-user-invocable skill",
			input:   "/skill:internal-codestyle",
			wantErr: ErrNotUserInvocable,
		},
		{
			name:     "unknown skill",
			input:    "/skill:unknown",
			wantType: ActionNotFound,
		},
		{
			name:        "TUI-only command in non-interactive",
			input:       "/compact",
			interactive: false,
			wantErr:     ErrTUIOnly,
		},
		{
			name:        "TUI-only command in interactive",
			input:       "/compact",
			interactive: true,
			wantType:    ActionCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := Parse(tt.input)
			action, err := Resolve(parsed, commands, skills, tt.interactive)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if action.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", action.Type, tt.wantType)
			}
			if action.Args != tt.wantArgs {
				t.Errorf("Args = %q, want %q", action.Args, tt.wantArgs)
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name      string
		action    *ResolvedAction
		sessionID string
		want      string
	}{
		{
			name: "skill without args",
			action: &ResolvedAction{
				Type:  ActionSkill,
				Skill: &skill.Info{Name: "test", Content: "Do the thing"},
			},
			want: "<skill_content name=\"test\">\nDo the thing\n</skill_content>",
		},
		{
			name: "skill with args appended when no placeholder",
			action: &ResolvedAction{
				Type:  ActionSkill,
				Skill: &skill.Info{Name: "test", Content: "Do the thing"},
				Args:  "v2.1.0",
			},
			want: "<skill_content name=\"test\">\nDo the thing\n\nARGUMENTS: v2.1.0\n</skill_content>",
		},
		{
			name: "skill with $ARGUMENTS placeholder",
			action: &ResolvedAction{
				Type:  ActionSkill,
				Skill: &skill.Info{Name: "test", Content: "Fix issue $ARGUMENTS"},
				Args:  "123",
			},
			want: "<skill_content name=\"test\">\nFix issue 123\n</skill_content>",
		},
		{
			name: "skill with positional args",
			action: &ResolvedAction{
				Type:  ActionSkill,
				Skill: &skill.Info{Name: "migrate", Content: "Migrate $0 from $1"},
				Args:  "SearchBar React",
			},
			want: "<skill_content name=\"migrate\">\nMigrate SearchBar from React\n</skill_content>",
		},
		{
			name:      "skill with session ID",
			sessionID: "sess-abc",
			action: &ResolvedAction{
				Type:  ActionSkill,
				Skill: &skill.Info{Name: "logger", Content: "Log to ${SESSION_ID}.log"},
			},
			want: "<skill_content name=\"logger\">\nLog to sess-abc.log\n</skill_content>",
		},
		{
			name:   "not found returns empty",
			action: &ResolvedAction{Type: ActionNotFound},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildPrompt(tt.action, tt.sessionID)
			if got != tt.want {
				t.Errorf("BuildPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubstituteArgs(t *testing.T) {
	tests := []struct {
		content string
		args    string
		want    string
	}{
		{"Review $ARGUMENTS code", "main", "Review main code"},
		{"No placeholders", "args", "No placeholders"},
		{"$ARGUMENTS twice: $ARGUMENTS", "x", "x twice: x"},
		{"Empty $ARGUMENTS", "", "Empty "},
	}

	for _, tt := range tests {
		got := SubstituteArgs(tt.content, tt.args)
		if got != tt.want {
			t.Errorf("SubstituteArgs(%q, %q) = %q, want %q", tt.content, tt.args, got, tt.want)
		}
	}
}
