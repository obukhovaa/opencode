package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Block scalars are the recommended way to write a skill description: a plain
// scalar containing ": " makes the whole frontmatter unparseable and the skill
// silently disappears, and `>` / `|` are immune to that. Since authoring guides
// now tell teams to use them, discovery must actually accept every form —
// including the chomping and indentation modifiers, which are the parts people
// reach for and nobody tests.
func TestParseSkillFileBlockScalarDescriptions(t *testing.T) {
	const colonTrap = "Registry of things. Carries three reverse indexes: artifact, image and library."

	tests := []struct {
		name        string
		frontmatter string
		wantDesc    string
	}{
		{
			name: "folded > joins lines with spaces",
			frontmatter: "name: block-skill\ndescription: >\n" +
				"  Reads and writes widgets.\n" +
				"  Use when the user mentions widgets.\n",
			wantDesc: "Reads and writes widgets. Use when the user mentions widgets.",
		},
		{
			name: "folded >- strips the trailing newline",
			frontmatter: "name: block-skill\ndescription: >-\n" +
				"  Reads widgets.\n" +
				"  Use for widgets.\n",
			wantDesc: "Reads widgets. Use for widgets.",
		},
		{
			// Note the missing trailing newline: splitFrontmatter reassembles the
			// block from the lines BEFORE the closing `---` and adds no final
			// newline, so clip chomping has no last line break to keep. Harmless
			// for a description — trailing whitespace means nothing there — but
			// it is why `|` and `|+` do not reproduce textbook chomping.
			name: "literal | preserves interior newlines",
			frontmatter: "name: block-skill\ndescription: |\n" +
				"  Line one.\n" +
				"  Line two.\n",
			wantDesc: "Line one.\nLine two.",
		},
		{
			name: "literal |- strips the trailing newline",
			frontmatter: "name: block-skill\ndescription: |-\n" +
				"  Line one.\n" +
				"  Line two.\n",
			wantDesc: "Line one.\nLine two.",
		},
		{
			// Same reassembly artifact as `|` above: one fewer trailing newline
			// than the YAML spec's keep chomping would give.
			name: "literal |+ parses, minus one trailing newline",
			frontmatter: "name: block-skill\ndescription: |+\n" +
				"  Line one.\n" +
				"\n",
			wantDesc: "Line one.\n",
		},
		{
			name: "explicit indentation indicator >2",
			frontmatter: "name: block-skill\ndescription: >2\n" +
				"  Indented by exactly two.\n" +
				"  Second line.\n",
			wantDesc: "Indented by exactly two. Second line.",
		},
		{
			name: "folded scalar survives a colon-space that would break a plain scalar",
			frontmatter: "name: block-skill\ndescription: >-\n" +
				"  Registry of things. Carries three reverse indexes: artifact, image and\n" +
				"  library.\n",
			wantDesc: colonTrap,
		},
		{
			name: "folded scalar survives a hash that would start a comment",
			frontmatter: "name: block-skill\ndescription: >-\n" +
				"  Handles issue #123 and tag #release.\n",
			wantDesc: "Handles issue #123 and tag #release.",
		},
		{
			name: "folded scalar survives leading YAML indicators",
			frontmatter: "name: block-skill\ndescription: >-\n" +
				"  - not a list item, just a dash.\n",
			wantDesc: "- not a list item, just a dash.",
		},
		{
			name: "blank line in a folded scalar becomes a newline",
			frontmatter: "name: block-skill\ndescription: >-\n" +
				"  First paragraph.\n" +
				"\n" +
				"  Second paragraph.\n",
			wantDesc: "First paragraph.\nSecond paragraph.",
		},
		{
			name: "double-quoted scalar with a colon-space",
			frontmatter: "name: block-skill\n" +
				`description: "Registry of things. Carries three reverse indexes: artifact, image and library."` + "\n",
			wantDesc: colonTrap,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			skillDir := filepath.Join(dir, "block-skill")
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(skillDir, "SKILL.md")
			body := "---\n" + tt.frontmatter + "---\n\n# Body\n\nSkill body here.\n"
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := parseSkillFile(path)
			if err != nil {
				t.Fatalf("parseSkillFile: %v\n--- file ---\n%s", err, body)
			}
			if got.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", got.Description, tt.wantDesc)
			}
			if got.Name != "block-skill" {
				t.Errorf("name = %q, want block-skill", got.Name)
			}
			if !strings.Contains(got.Content, "Skill body here.") {
				t.Errorf("body was lost or truncated: %q", got.Content)
			}
		})
	}
}

// The length limit applies to the PARSED value, so the quoting or folding an
// author uses to stay safe does not eat into their budget — and a folded scalar
// whose SOURCE runs past the limit still passes when the folded value fits.
func TestParseSkillFileBlockScalarLengthIsMeasuredOnTheParsedValue(t *testing.T) {
	// 60 lines x 17 source chars is over 1024 bytes of source, but the folded
	// value is 60 x 16 + 59 joining spaces = 1019 characters.
	var sb strings.Builder
	sb.WriteString("name: block-skill\ndescription: >-\n")
	for i := 0; i < 60; i++ {
		sb.WriteString("  aaaaaaaaaaaaaaaa\n") // 2 indent + 16 chars
	}
	frontmatter := sb.String()
	if len(frontmatter) <= maxDescriptionLength {
		t.Fatalf("fixture is not large enough to be meaningful: %d source bytes", len(frontmatter))
	}

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "block-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\n"+frontmatter+"---\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseSkillFile(path)
	if err != nil {
		t.Fatalf("a folded description whose parsed value fits was rejected: %v", err)
	}
	if n := len(got.Description); n > maxDescriptionLength {
		t.Fatalf("parsed description is %d chars, over the limit — fixture is wrong", n)
	}
}

// splitFrontmatter finds the closing fence by scanning for a line that equals
// "---" after TrimSpace, rather than by parsing YAML. That is fine for every
// realistic description, but it means a block scalar cannot contain a line whose
// only content is "---": the frontmatter is cut short there, and what follows
// becomes the body. Pinned so the limitation is a known one rather than a
// mystery bug report, and so a future YAML-aware splitter can delete this test
// on purpose.
func TestParseSkillFileBlockScalarCannotContainABareTripleDash(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "block-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	body := "---\nname: block-skill\ndescription: >-\n" +
		"  Before the fence.\n" +
		"  ---\n" +
		"  After the fence.\n" +
		"---\nBody\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseSkillFile(path)
	if err != nil {
		// Also an acceptable outcome: what must not happen is silently keeping
		// a truncated description.
		if !strings.Contains(err.Error(), "description") && !strings.Contains(err.Error(), "frontmatter") {
			t.Fatalf("unexpected failure mode: %v", err)
		}
		return
	}
	if strings.Contains(got.Description, "After the fence") {
		t.Fatal("the splitter is now YAML-aware — delete this test and the caveat in docs/skills.md")
	}
	t.Logf("known limitation confirmed: description truncated to %q", got.Description)
}
