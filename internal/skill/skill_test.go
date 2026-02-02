package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple name", "git-release", false},
		{"valid single word", "docker", false},
		{"valid multiple hyphens", "my-cool-skill", false},
		{"valid with numbers", "skill-123", false},
		{"empty name", "", true},
		{"uppercase", "Git-Release", true},
		{"starts with hyphen", "-skill", true},
		{"ends with hyphen", "skill-", true},
		{"consecutive hyphens", "skill--name", true},
		{"underscore", "skill_name", true},
		{"space", "skill name", true},
		{"too long", string(make([]byte, 65)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDescription(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid description", "A helpful skill", false},
		{"max length", string(make([]byte, 1024)), false},
		{"empty description", "", true},
		{"too long", string(make([]byte, 1025)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDescription(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDescription() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantFrontmatter string
		wantContent     string
		wantErr         bool
	}{
		{
			name: "valid frontmatter",
			input: `---
name: test
description: A test skill
---

# Content here`,
			wantFrontmatter: "name: test\ndescription: A test skill",
			wantContent:     "\n# Content here",
			wantErr:         false,
		},
		{
			name: "no content",
			input: `---
name: test
description: A test skill
---`,
			wantFrontmatter: "name: test\ndescription: A test skill",
			wantContent:     "",
			wantErr:         false,
		},
		{
			name:    "missing start delimiter",
			input:   "name: test\n---",
			wantErr: true,
		},
		{
			name: "missing end delimiter",
			input: `---
name: test
description: A test skill`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frontmatter, content, err := splitFrontmatter(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("splitFrontmatter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if frontmatter != tt.wantFrontmatter {
					t.Errorf("splitFrontmatter() frontmatter = %q, want %q", frontmatter, tt.wantFrontmatter)
				}
				if content != tt.wantContent {
					t.Errorf("splitFrontmatter() content = %q, want %q", content, tt.wantContent)
				}
			}
		})
	}
}

func TestParseSkillFile(t *testing.T) {
	// Create temporary directory for test files
	tmpDir := t.TempDir()

	// Create a valid skill file
	validSkillDir := filepath.Join(tmpDir, "git-release")
	if err := os.MkdirAll(validSkillDir, 0755); err != nil {
		t.Fatal(err)
	}

	validSkillContent := `---
name: git-release
description: Create consistent releases and changelogs
license: MIT
compatibility: opencode
metadata:
  audience: maintainers
---

## What I do

- Draft release notes
- Propose version bump
`

	validSkillPath := filepath.Join(validSkillDir, "SKILL.md")
	if err := os.WriteFile(validSkillPath, []byte(validSkillContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create skill with name mismatch
	mismatchDir := filepath.Join(tmpDir, "wrong-name")
	if err := os.MkdirAll(mismatchDir, 0755); err != nil {
		t.Fatal(err)
	}

	mismatchContent := `---
name: different-name
description: This name doesn't match directory
---

Content here
`

	mismatchPath := filepath.Join(mismatchDir, "SKILL.md")
	if err := os.WriteFile(mismatchPath, []byte(mismatchContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
		check   func(*testing.T, *Info)
	}{
		{
			name:    "valid skill",
			path:    validSkillPath,
			wantErr: false,
			check: func(t *testing.T, info *Info) {
				if info.Name != "git-release" {
					t.Errorf("Name = %q, want %q", info.Name, "git-release")
				}
				if info.Description != "Create consistent releases and changelogs" {
					t.Errorf("Description = %q, want %q", info.Description, "Create consistent releases and changelogs")
				}
				if info.License != "MIT" {
					t.Errorf("License = %q, want %q", info.License, "MIT")
				}
				if info.Location != validSkillPath {
					t.Errorf("Location = %q, want %q", info.Location, validSkillPath)
				}
				if !contains(info.Content, "Draft release notes") {
					t.Errorf("Content missing expected text")
				}
			},
		},
		{
			name:    "name mismatch",
			path:    mismatchPath,
			wantErr: true,
		},
		{
			name:    "nonexistent file",
			path:    filepath.Join(tmpDir, "nonexistent", "SKILL.md"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := parseSkillFile(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSkillFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, info)
			}
		})
	}
}

func TestGetWorktreeRoot(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()

	// Create nested directories
	gitDir := filepath.Join(tmpDir, "project")
	subDir := filepath.Join(gitDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create .git directory
	dotGit := filepath.Join(gitDir, ".git")
	if err := os.MkdirAll(dotGit, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		workingDir string
		want       string
	}{
		{
			name:       "from git root",
			workingDir: gitDir,
			want:       gitDir,
		},
		{
			name:       "from subdirectory",
			workingDir: subDir,
			want:       gitDir,
		},
		{
			name:       "not in git repo",
			workingDir: tmpDir,
			want:       tmpDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getWorktreeRoot(tt.workingDir)
			if got != tt.want {
				t.Errorf("getWorktreeRoot() = %q, want %q", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && 
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || 
		len(s) > len(substr) && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
