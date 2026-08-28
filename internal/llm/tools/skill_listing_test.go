package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/skill"
)

// scopedRegistry answers EvaluatePermission per agent and records the agent ID
// each call carried. The recording is the point: the listing used to ask with
// an empty agent ID, which silently fell back to global permissions and made
// every agent-specific skill rule invisible to the tool description.
type scopedRegistry struct {
	stubRegistry
	deniedFor map[string]map[string]bool // agentID -> skill name -> denied
	seen      []string
}

func (r *scopedRegistry) EvaluatePermission(agentID, toolName, input string) permission.Action {
	r.seen = append(r.seen, agentID)
	if names, ok := r.deniedFor[agentID]; ok && names[input] {
		return permission.ActionDeny
	}
	return permission.ActionAllow
}

func skillsFixture(names ...string) []skill.Info {
	out := make([]skill.Info, 0, len(names))
	for _, n := range names {
		out = append(out, skill.Info{
			Name:        n,
			Description: "Does " + n + " things. Use when the user asks about " + n + ".",
			Location:    "/workspace/team/skills/" + n + "/SKILL.md",
		})
	}
	return out
}

func TestFilterSkillsByPermissionScopesToAgent(t *testing.T) {
	reg := &scopedRegistry{deniedFor: map[string]map[string]bool{
		"coder": {"bdata-events": true},
	}}
	all := skillsFixture("bdata-events", "composer-devops")

	coder := &skillTool{registry: reg, agentID: "coder"}
	got := coder.filterSkillsByPermission(all)
	if len(got) != 1 || got[0].Name != "composer-devops" {
		t.Fatalf("coder listing = %v, want only composer-devops", names(got))
	}

	for _, id := range reg.seen {
		if id != "coder" {
			t.Fatalf("EvaluatePermission was asked with agent ID %q, want %q — an empty ID "+
				"falls back to global permissions and ignores the agent's own rules", id, "coder")
		}
	}

	// A different agent is unaffected by coder's deny rule.
	other := &skillTool{registry: reg, agentID: "explorer"}
	if got := other.filterSkillsByPermission(all); len(got) != 2 {
		t.Errorf("explorer listing = %v, want both skills", names(got))
	}
}

func TestFilterSkillsByPermissionNilRegistry(t *testing.T) {
	tool := &skillTool{}
	all := skillsFixture("a-skill", "b-skill")
	if got := tool.filterSkillsByPermission(all); len(got) != 2 {
		t.Fatalf("nil registry filtered %d skills; Info() must stay callable without a "+
			"permission model", len(all)-len(got))
	}
}

func names(skills []skill.Info) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}

func TestRenderAvailableSkillsOmitsLocation(t *testing.T) {
	block := renderAvailableSkills(skillsFixture("composer-devops"), skillListingLimits{})
	if strings.Contains(block, "<location>") || strings.Contains(block, "file://") {
		t.Errorf("listing still carries a <location>; the path is unusable before the skill is "+
			"loaded and Run reports the base directory with the content:\n%s", block)
	}
	if !strings.Contains(block, "<name>composer-devops</name>") {
		t.Errorf("listing lost the skill name:\n%s", block)
	}
}

func TestRenderAvailableSkillsTruncatesDescriptions(t *testing.T) {
	long := "Trigger terms first. " + strings.Repeat("filler words that run on and on ", 40)
	skills := []skill.Info{{Name: "verbose-skill", Description: long}}

	block := renderAvailableSkills(skills, skillListingLimits{maxDescriptionChars: 80})

	if !strings.Contains(block, "Trigger terms first.") {
		t.Errorf("truncation dropped the head of the description, where trigger terms live:\n%s", block)
	}
	if strings.Contains(block, long) {
		t.Errorf("description was not truncated:\n%s", block)
	}
	if !strings.Contains(block, "…") {
		t.Errorf("truncated description carries no ellipsis, so the model cannot tell it is partial:\n%s", block)
	}
}

func TestTruncateDescription(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under the cap is untouched", "short and sweet", 100, "short and sweet"},
		{"zero means unbounded", strings.Repeat("x", 50), 0, strings.Repeat("x", 50)},
		{"cuts at a word boundary", "alpha beta gamma delta epsilon", 20, "alpha beta gamma…"},
		{"no boundary near the cut still fits the cap", strings.Repeat("x", 40), 10, strings.Repeat("x", 9) + "…"},
		{"the ellipsis fits inside a cap of one", "hello world", 1, "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateDescription(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateDescription(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			// The ellipsis is charged against the cap, so the cap is exact.
			if tc.max > 0 && utf8.RuneCountInString(got) > tc.max {
				t.Errorf("result is %d chars, over the %d cap", utf8.RuneCountInString(got), tc.max)
			}
		})
	}
}

func TestRenderAvailableSkillsRespectsTotalBudget(t *testing.T) {
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, fmt.Sprintf("skill-%02d", i))
	}
	skills := skillsFixture(many...)

	const budget = 1500
	block := renderAvailableSkills(skills, skillListingLimits{maxDescriptionChars: 200, maxTotalChars: budget})

	if got := utf8.RuneCountInString(block); got > budget {
		t.Errorf("listing is %d chars, over the %d budget:\n%s", got, budget, block)
	}
	if !strings.Contains(block, "total=\"60\"") {
		t.Errorf("truncated listing does not disclose the full inventory size:\n%s", block)
	}
	if !strings.Contains(block, "<note>") || !strings.Contains(block, "omitted") {
		t.Errorf("truncated listing carries no note, so the model cannot tell skills are missing:\n%s", block)
	}
	// Alphabetically first survive: the drop must be reproducible, not
	// filesystem-order dependent.
	if !strings.Contains(block, "<name>skill-00</name>") {
		t.Errorf("expected the alphabetically first skill to survive:\n%s", block)
	}
	if strings.Contains(block, "<name>skill-59</name>") {
		t.Errorf("expected the alphabetically last skill to be dropped first:\n%s", block)
	}
}

func TestRenderAvailableSkillsUnboundedByDefault(t *testing.T) {
	skills := skillsFixture("a-skill", "b-skill", "c-skill")
	block := renderAvailableSkills(skills, skillListingLimits{})

	if strings.Contains(block, "showing=") || strings.Contains(block, "<note>") {
		t.Errorf("a zero budget must list everything without a truncation notice:\n%s", block)
	}
	for _, s := range skills {
		if !strings.Contains(block, "<name>"+s.Name+"</name>") {
			t.Errorf("skill %q missing from an unbounded listing:\n%s", s.Name, block)
		}
	}
}

func TestRenderSkillEntryIncludesArgumentHint(t *testing.T) {
	sk := skill.Info{Name: "release", Description: "Ships a release.", ArgumentHint: "<version>"}
	if got := renderSkillEntry(sk, 0); !strings.Contains(got, "<args><version></args>") {
		t.Errorf("argument hint lost from the entry:\n%s", got)
	}
}

// Guard the interface assumption the scoped filter relies on.
var _ agentregistry.Registry = (*scopedRegistry)(nil)

// TestRenderAvailableSkillsKeepsWhatFits guards the case where the budget is
// large enough for the whole inventory. Reserving room for the omission note
// before knowing whether anything is omitted drops skills to make space for an
// explanation of a drop that only the reservation caused.
func TestRenderAvailableSkillsKeepsWhatFits(t *testing.T) {
	skills := skillsFixture("a-skill", "b-skill", "c-skill")
	complete := renderAvailableSkills(skills, skillListingLimits{})
	exact := utf8.RuneCountInString(complete)

	for _, budget := range []int{exact, exact + 1, exact + 100} {
		block := renderAvailableSkills(skills, skillListingLimits{maxTotalChars: budget})
		if block != complete {
			t.Errorf("budget %d fits the complete %d-char listing but it was truncated:\n%s",
				budget, exact, block)
		}
	}

	// One character short is the first budget that may truncate.
	if block := renderAvailableSkills(skills, skillListingLimits{maxTotalChars: exact - 1}); block == complete {
		t.Errorf("budget %d is under the %d-char listing yet nothing was dropped", exact-1, exact)
	}
}

// TestRenderAvailableSkillsNeverExceedsBudget sweeps the boundaries instead of
// sampling one comfortable budget: the block is only over the cap when the
// slack left after the last entry that fits is smaller than the wrapper, which
// a single fixture-sized budget will not hit.
func TestRenderAvailableSkillsNeverExceedsBudget(t *testing.T) {
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, fmt.Sprintf("skill-%02d", i))
	}
	skills := skillsFixture(many...)

	// Below the wrapper plus one entry the floor in renderAvailableSkills
	// deliberately wins (see TestRenderAvailableSkillsTinyBudgetStillListsASkill),
	// so the cap only binds from the first budget that can hold a real listing.
	floor := partialWrapperChars(len(skills)) +
		utf8.RuneCountInString(renderSkillEntry(skills[0], 200))

	for budget := floor; budget <= floor+4000; budget++ {
		block := renderAvailableSkills(skills, skillListingLimits{maxDescriptionChars: 200, maxTotalChars: budget})
		if got := utf8.RuneCountInString(block); got > budget {
			t.Fatalf("budget %d produced a %d-char listing (over by %d):\n%s",
				budget, got, got-budget, block)
		}
	}
}

// TestRenderAvailableSkillsTinyBudgetStillListsASkill covers a budget smaller
// than the wrapper itself. Emitting the note and nothing else spends ~300
// characters to say that no skill is visible, which is strictly worse than
// naming one.
func TestRenderAvailableSkillsTinyBudgetStillListsASkill(t *testing.T) {
	skills := skillsFixture("a-skill", "b-skill", "c-skill")
	block := renderAvailableSkills(skills, skillListingLimits{maxTotalChars: 10})

	if !strings.Contains(block, "<name>a-skill</name>") {
		t.Errorf("a budget below the wrapper cost listed no skill at all:\n%s", block)
	}
	if !strings.Contains(block, "showing=\"1\"") || !strings.Contains(block, "total=\"3\"") {
		t.Errorf("counters do not match the one skill actually shown:\n%s", block)
	}
	if !strings.Contains(block, "2 of 3 skills are omitted") {
		t.Errorf("note does not match the counters:\n%s", block)
	}
}
