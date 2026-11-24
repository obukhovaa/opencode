package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/skill"
)

// teamSkills builds skills at workspace-relative locations, which the scope
// matcher resolves against the working directory.
func teamSkills(entries map[string]string) []skill.Info {
	var out []skill.Info
	for _, name := range []string{"bdata-events", "bdata-y", "composer-devops", "shared-one"} {
		dir, ok := entries[name]
		if !ok {
			continue
		}
		out = append(out, skill.Info{
			Name:        name,
			Description: "Does " + name + " things. Use when the user asks about " + name + ".",
			Location:    dir + "/" + name + "/SKILL.md",
		})
	}
	return out
}

var piano = map[string]string{
	"bdata-events":    "bdata/skills",
	"bdata-y":         "bdata/skills",
	"composer-devops": "composer/skills",
	"shared-one":      ".agents/skills",
}

var composerOnly = []string{".agents/skills/**", "composer/skills/**"}

func TestSkillListing_UnscopedIsUnchanged(t *testing.T) {
	all := teamSkills(piano)
	tool := &skillTool{registry: &scopedRegistry{}, agentID: "coder"}
	full, other := tool.splitByListingScope(all)
	if len(full) != len(all) || other != nil {
		t.Fatalf("unscoped split = %d full, %q other", len(full), other)
	}
	lim := skillListingLimits{maxDescriptionChars: 500}
	if renderSkillListing(full, lim, other) != renderAvailableSkills(all, lim) {
		t.Fatal("an unscoped listing must be byte-identical to today's")
	}
}

func TestSkillListing_TwoTiers(t *testing.T) {
	tool := &skillTool{registry: &scopedRegistry{}, agentID: "coder", listingScope: composerOnly}
	full, other := tool.splitByListingScope(teamSkills(piano))
	block := renderSkillListing(full, skillListingLimits{maxDescriptionChars: 500}, other)

	for _, name := range []string{"composer-devops", "shared-one"} {
		if !strings.Contains(block, "<name>"+name+"</name>") {
			t.Errorf("%s should be listed in full:\n%s", name, block)
		}
	}
	if strings.Contains(block, "<name>bdata-") {
		t.Errorf("out-of-scope skills must not be described:\n%s", block)
	}
	if !strings.Contains(block, `<other_skills count="2">bdata-events, bdata-y</other_skills>`) {
		t.Errorf("names-only tier missing or unsorted:\n%s", block)
	}
	if !strings.Contains(block, "can still be loaded by name") {
		t.Errorf("names-only note missing:\n%s", block)
	}
	if !strings.HasSuffix(block, "</available_skills>") {
		t.Errorf("block must stay well-formed:\n%s", block)
	}
}

func TestSkillListing_DeniedSkillInNeitherTier(t *testing.T) {
	reg := &scopedRegistry{deniedFor: map[string]map[string]bool{"coder": {"bdata-events": true}}}
	tool := &skillTool{registry: reg, agentID: "coder", listingScope: composerOnly}
	full, other := tool.splitByListingScope(tool.filterSkillsByPermission(teamSkills(piano)))
	block := renderSkillListing(full, skillListingLimits{}, other)
	if strings.Contains(block, "bdata-events") {
		t.Fatalf("a denied skill must not appear, not even by name:\n%s", block)
	}
	if !strings.Contains(block, `<other_skills count="1">bdata-y</other_skills>`) {
		t.Fatalf("permitted out-of-scope skill missing:\n%s", block)
	}
}

func TestSkillListing_BudgetKeepsEveryName(t *testing.T) {
	tool := &skillTool{registry: &scopedRegistry{}, agentID: "coder", listingScope: composerOnly}
	all := teamSkills(piano)
	for i := range all {
		// Entries longer than the partial-listing note, so a drop is possible.
		all[i].Description = strings.Repeat(all[i].Name+" ", 60)
	}
	full, other := tool.splitByListingScope(all)
	// Room for the partial wrapper, the names-only tier and one full entry,
	// but not the second.
	lim := skillListingLimits{maxTotalChars: partialWrapperChars(len(full)) +
		utf8.RuneCountInString(renderOtherSkills(other)) +
		utf8.RuneCountInString(renderSkillEntry(full[0], 0)) + 1}
	block := renderSkillListing(full, lim, other)

	if !strings.Contains(block, `<other_skills count="2">bdata-events, bdata-y</other_skills>`) {
		t.Fatalf("a tight budget must never cut the names-only tier:\n%s", block)
	}
	if !strings.Contains(block, `showing="1" total="2"`) {
		t.Fatalf("the budget should drop a full entry and disclose it:\n%s", block)
	}
	if n := utf8.RuneCountInString(block); n > lim.maxTotalChars {
		t.Fatalf("block is %d chars, over the %d budget", n, lim.maxTotalChars)
	}
}

func TestSkillListing_NothingOutOfScopeEqualsUnscoped(t *testing.T) {
	inScope := teamSkills(map[string]string{"composer-devops": "composer/skills", "shared-one": ".agents/skills"})
	tool := &skillTool{registry: &scopedRegistry{}, agentID: "coder", listingScope: composerOnly}
	full, other := tool.splitByListingScope(inScope)
	lim := skillListingLimits{maxDescriptionChars: 500}
	if renderSkillListing(full, lim, other) != renderAvailableSkills(inScope, lim) {
		t.Fatal("with nothing out of scope the block must equal the unscoped one")
	}
}
