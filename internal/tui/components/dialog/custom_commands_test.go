package dialog

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/opencode-ai/opencode/internal/slashcmd"
)

func TestNamedArgPattern(t *testing.T) {
	testCases := []struct {
		input    string
		expected []string
	}{
		{
			input:    "This is a test with $ARGUMENTS placeholder",
			expected: []string{"ARGUMENTS"},
		},
		{
			input:    "This is a test with $FOO and $BAR placeholders",
			expected: []string{"FOO", "BAR"},
		},
		{
			input:    "This is a test with $FOO_BAR and $BAZ123 placeholders",
			expected: []string{"FOO_BAR", "BAZ123"},
		},
		{
			input:    "This is a test with no placeholders",
			expected: []string{},
		},
		{
			input:    "This is a test with $FOO appearing twice: $FOO",
			expected: []string{"FOO"},
		},
		{
			input:    "This is a test with $1INVALID placeholder",
			expected: []string{},
		},
	}

	for _, tc := range testCases {
		matches := namedArgPattern.FindAllStringSubmatch(tc.input, -1)

		// Extract unique argument names
		argNames := make([]string, 0)
		argMap := make(map[string]bool)

		for _, match := range matches {
			argName := match[1] // Group 1 is the name without $
			if !argMap[argName] {
				argMap[argName] = true
				argNames = append(argNames, argName)
			}
		}

		// Check if we got the expected number of arguments
		if len(argNames) != len(tc.expected) {
			t.Errorf("Expected %d arguments, got %d for input: %s", len(tc.expected), len(argNames), tc.input)
			continue
		}

		// Check if we got the expected argument names
		for _, expectedArg := range tc.expected {
			found := false
			for _, actualArg := range argNames {
				if actualArg == expectedArg {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected argument %s not found in %v for input: %s", expectedArg, argNames, tc.input)
			}
		}
	}
}

func TestRegexPattern(t *testing.T) {
	pattern := regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

	validMatches := []string{
		"$FOO",
		"$BAR",
		"$FOO_BAR",
		"$BAZ123",
		"$ARGUMENTS",
	}

	invalidMatches := []string{
		"$foo",
		"$1BAR",
		"$_FOO",
		"FOO",
		"$",
	}

	for _, valid := range validMatches {
		if !pattern.MatchString(valid) {
			t.Errorf("Expected %s to match, but it didn't", valid)
		}
	}

	for _, invalid := range invalidMatches {
		if pattern.MatchString(invalid) {
			t.Errorf("Expected %s not to match, but it did", invalid)
		}
	}
}

func TestParseCommandMarkdown(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTitle string
		wantDesc  string
		wantBody  string
	}{
		{
			name:      "no frontmatter",
			input:     "Just a plain command body",
			wantTitle: "",
			wantDesc:  "",
			wantBody:  "Just a plain command body",
		},
		{
			name:      "with frontmatter",
			input:     "---\ntitle: My Command\ndescription: Does cool stuff\n---\nThe body content",
			wantTitle: "My Command",
			wantDesc:  "Does cool stuff",
			wantBody:  "The body content",
		},
		{
			name:      "frontmatter title only",
			input:     "---\ntitle: Title Only\n---\nBody here",
			wantTitle: "Title Only",
			wantDesc:  "",
			wantBody:  "Body here",
		},
		{
			name:      "frontmatter description only",
			input:     "---\ndescription: A description\n---\nBody here",
			wantTitle: "",
			wantDesc:  "A description",
			wantBody:  "Body here",
		},
		{
			name:      "empty body after frontmatter",
			input:     "---\ntitle: No Body\n---\n",
			wantTitle: "No Body",
			wantDesc:  "",
			wantBody:  "",
		},
		{
			name:      "incomplete frontmatter treated as body",
			input:     "---\ntitle: Broken\nno closing delimiter",
			wantTitle: "",
			wantDesc:  "",
			wantBody:  "---\ntitle: Broken\nno closing delimiter",
		},
		{
			name:      "body with newlines after frontmatter",
			input:     "---\ntitle: Test\n---\n\n\nBody after blank lines",
			wantTitle: "Test",
			wantDesc:  "",
			wantBody:  "Body after blank lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := parseCommandMarkdown([]byte(tt.input))
			if fm.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", fm.Title, tt.wantTitle)
			}
			if fm.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", fm.Description, tt.wantDesc)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestGetWorktreeRoot(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name       string
		setup      func() string
		wantResult string
	}{
		{
			name: "finds git root",
			setup: func() string {
				root := filepath.Join(tmpDir, "git-project")
				sub := filepath.Join(root, "src", "pkg")
				os.MkdirAll(filepath.Join(root, ".git"), 0o755)
				os.MkdirAll(sub, 0o755)
				return sub
			},
			wantResult: filepath.Join(tmpDir, "git-project"),
		},
		{
			name: "returns workingDir when no git root",
			setup: func() string {
				dir := filepath.Join(tmpDir, "no-git", "deep")
				os.MkdirAll(dir, 0o755)
				return dir
			},
			wantResult: filepath.Join(tmpDir, "no-git", "deep"),
		},
		{
			name: "workingDir is git root",
			setup: func() string {
				root := filepath.Join(tmpDir, "at-root")
				os.MkdirAll(filepath.Join(root, ".git"), 0o755)
				return root
			},
			wantResult: filepath.Join(tmpDir, "at-root"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workingDir := tt.setup()
			got := getWorktreeRoot(workingDir)
			if got != tt.wantResult {
				t.Errorf("getWorktreeRoot(%q) = %q, want %q", workingDir, got, tt.wantResult)
			}
		})
	}
}

func TestLoadCommandsFromDir(t *testing.T) {
	tmpDir := t.TempDir()
	commandsDir := filepath.Join(tmpDir, ".agents", "commands")
	os.MkdirAll(commandsDir, 0o755)

	os.WriteFile(filepath.Join(commandsDir, "test.md"), []byte("---\ntitle: Test\n---\nDo something"), 0o644)
	os.MkdirAll(filepath.Join(commandsDir, "sub"), 0o755)
	os.WriteFile(filepath.Join(commandsDir, "sub", "nested.md"), []byte("Nested body"), 0o644)
	os.WriteFile(filepath.Join(commandsDir, "ignore.txt"), []byte("not a command"), 0o644)

	cmds, err := loadCommandsFromDir(commandsDir, slashcmd.ProjectCommandPrefix)
	if err != nil {
		t.Fatalf("loadCommandsFromDir failed: %v", err)
	}

	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}

	foundTest, foundNested := false, false
	for _, cmd := range cmds {
		switch cmd.ID {
		case "project:test":
			foundTest = true
			if cmd.Title != "Test" {
				t.Errorf("expected title 'Test', got %q", cmd.Title)
			}
		case "project:sub:nested":
			foundNested = true
		}
	}
	if !foundTest {
		t.Error("command 'project:test' not found")
	}
	if !foundNested {
		t.Error("command 'project:sub:nested' not found")
	}
}

func TestAddScopeHints(t *testing.T) {
	tests := []struct {
		name     string
		commands []Command
		want     map[string]string // ID -> expected Title
	}{
		{
			name: "no duplicates, no hints",
			commands: []Command{
				{CommandInfo: slashcmd.CommandInfo{ID: "user:deploy", Title: "Deploy"}},
				{CommandInfo: slashcmd.CommandInfo{ID: "project:lint", Title: "Lint"}},
			},
			want: map[string]string{
				"user:deploy":  "Deploy",
				"project:lint": "Lint",
			},
		},
		{
			name: "duplicate base name gets hints",
			commands: []Command{
				{CommandInfo: slashcmd.CommandInfo{ID: "user:deploy", Title: "Deploy"}},
				{CommandInfo: slashcmd.CommandInfo{ID: "project:deploy", Title: "Deploy"}},
			},
			want: map[string]string{
				"user:deploy":    "Deploy (user)",
				"project:deploy": "Deploy (project)",
			},
		},
		{
			name: "mixed duplicates and unique",
			commands: []Command{
				{CommandInfo: slashcmd.CommandInfo{ID: "user:deploy", Title: "Deploy"}},
				{CommandInfo: slashcmd.CommandInfo{ID: "project:deploy", Title: "Deploy"}},
				{CommandInfo: slashcmd.CommandInfo{ID: "project:lint", Title: "Lint"}},
			},
			want: map[string]string{
				"user:deploy":    "Deploy (user)",
				"project:deploy": "Deploy (project)",
				"project:lint":   "Lint",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addScopeHints(tt.commands)
			for _, cmd := range tt.commands {
				if expected, ok := tt.want[cmd.ID]; ok {
					if cmd.Title != expected {
						t.Errorf("command %q: title = %q, want %q", cmd.ID, cmd.Title, expected)
					}
				}
			}
		})
	}
}

func TestLoadCommandsFromNonexistentDir(t *testing.T) {
	cmds, err := loadCommandsFromDir("/nonexistent/path", slashcmd.ProjectCommandPrefix)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if cmds != nil {
		t.Fatalf("expected nil commands, got: %v", cmds)
	}
}
