package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, dir, body string) string {
	t.Helper()
	skillDir := filepath.Join(root, dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScanDirectoryReportsDroppedSkills covers the two rejections seen in the
// wild: a description over the 1024-char frontmatter limit, and a frontmatter
// name that does not match its directory. Both make the skill invisible to
// every agent, so discovery must hand back a reason rather than only logging
// one line per file among hundreds.
func TestScanDirectoryReportsDroppedSkills(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "good-skill", "---\nname: good-skill\ndescription: A fine skill.\n---\nBody")
	longDesc := writeSkill(t, root, "long-desc",
		"---\nname: long-desc\ndescription: \""+strings.Repeat("x", maxDescriptionLength+1)+"\"\n---\nBody")
	mismatch := writeSkill(t, root, "events",
		"---\nname: dcs-events\ndescription: Namespaced name, plain directory.\n---\nBody")

	skills, dropped := scanDirectory(root, "**/SKILL.md")

	if len(skills) != 1 || skills[0].Name != "good-skill" {
		t.Fatalf("registered %v, want only good-skill", skillNames(skills))
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped %d skills, want 2: %+v", len(dropped), dropped)
	}

	byPath := map[string]string{}
	for _, d := range dropped {
		byPath[d.Path] = d.Reason
	}
	if reason, ok := byPath[longDesc]; !ok {
		t.Errorf("over-long description not reported; got %+v", dropped)
	} else if !strings.Contains(reason, "frontmatter") && !strings.Contains(reason, "description") {
		t.Errorf("reason %q does not say why the skill was dropped", reason)
	}
	if reason, ok := byPath[mismatch]; !ok {
		t.Errorf("name/directory mismatch not reported; got %+v", dropped)
	} else if !strings.Contains(reason, "dcs-events") || !strings.Contains(reason, "events") {
		t.Errorf("reason %q should name both the frontmatter name and the directory", reason)
	}
}

// TestDiscoverCustomPathsReportsMissingPath guards the workspace-assembly
// failure mode: a team checkout that never landed leaves its configured
// skills path missing, and every skill under it disappears. "No skills" and
// "no checkout" must not look the same.
func TestDiscoverCustomPathsReportsMissingPath(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("present", "ok-skill"),
		"---\nname: ok-skill\ndescription: Present and correct.\n---\nBody")

	skills, dropped := discoverCustomPaths([]string{"present", "absent-team/skills"}, root)

	if len(skills) != 1 || skills[0].Name != "ok-skill" {
		t.Fatalf("registered %v, want only ok-skill", skillNames(skills))
	}
	if len(dropped) != 1 {
		t.Fatalf("dropped %d entries, want 1: %+v", len(dropped), dropped)
	}
	if !strings.Contains(dropped[0].Reason, "absent-team/skills") {
		t.Errorf("reason %q should name the configured path that did not resolve", dropped[0].Reason)
	}
}

func skillNames(skills []Info) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}
