package slashcmd

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantNil   bool
		wantName  string
		wantArgs  string
		wantSkill bool
	}{
		{
			name:    "empty string",
			input:   "",
			wantNil: true,
		},
		{
			name:    "no slash prefix",
			input:   "commit",
			wantNil: true,
		},
		{
			name:    "just a slash",
			input:   "/",
			wantNil: true,
		},
		{
			name:     "simple command",
			input:    "/commit",
			wantName: "commit",
		},
		{
			name:     "command with args",
			input:    "/review main",
			wantName: "review",
			wantArgs: "main",
		},
		{
			name:     "command with multiple args",
			input:    "/review main --focus security",
			wantName: "review",
			wantArgs: "main --focus security",
		},
		{
			name:      "skill command",
			input:     "/skill:git-release",
			wantName:  "git-release",
			wantSkill: true,
		},
		{
			name:      "skill command with args",
			input:     "/skill:git-release v2.1.0",
			wantName:  "git-release",
			wantArgs:  "v2.1.0",
			wantSkill: true,
		},
		{
			name:     "command with leading/trailing spaces",
			input:    "  /commit  ",
			wantName: "commit",
		},
		{
			name:     "command with prefix",
			input:    "/user:deploy",
			wantName: "user:deploy",
		},
		{
			name:    "skill prefix but no name",
			input:   "/skill:",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)
			if tt.wantNil {
				if result != nil {
					t.Errorf("Parse(%q) = %+v, want nil", tt.input, result)
				}
				return
			}
			if result == nil {
				t.Fatalf("Parse(%q) = nil, want non-nil", tt.input)
			}
			if result.Name != tt.wantName {
				t.Errorf("Parse(%q).Name = %q, want %q", tt.input, result.Name, tt.wantName)
			}
			if result.Args != tt.wantArgs {
				t.Errorf("Parse(%q).Args = %q, want %q", tt.input, result.Args, tt.wantArgs)
			}
			if result.IsSkill != tt.wantSkill {
				t.Errorf("Parse(%q).IsSkill = %v, want %v", tt.input, result.IsSkill, tt.wantSkill)
			}
		})
	}
}
