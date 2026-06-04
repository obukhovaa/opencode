package completions

import (
	"sort"
	"strings"

	"github.com/lithammer/fuzzysearch/fuzzy"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

const CommandCompletionProviderID = "command"

type commandCompletionProvider struct {
	commands []dialog.Command
}

func (p *commandCompletionProvider) GetId() string {
	return CommandCompletionProviderID
}

func (p *commandCompletionProvider) GetEntry() dialog.CompletionItemI {
	return dialog.NewCompletionItem(dialog.CompletionItem{
		Title: "Commands",
		Value: "commands",
	})
}

func (p *commandCompletionProvider) GetChildEntries(query string) ([]dialog.CompletionItemI, error) {
	items := make([]dialog.CompletionItemI, 0, len(p.commands))

	// Include user-invocable skills
	skillItems := userInvocableSkillItems()

	if query == "" {
		for _, cmd := range p.commands {
			items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
				Title: cmd.Title,
				Value: cmd.ID,
			}))
		}
		items = append(items, skillItems...)
		return items, nil
	}

	// Build combined search list
	type searchEntry struct {
		title string
		value string
		label string
	}

	var entries []searchEntry
	for _, cmd := range p.commands {
		entries = append(entries, searchEntry{
			title: cmd.Title,
			value: cmd.ID,
			label: cmd.ID + " " + cmd.Title,
		})
	}
	for _, item := range skillItems {
		entries = append(entries, searchEntry{
			title: item.DisplayValue(),
			value: item.GetValue(),
			label: item.GetValue() + " " + item.DisplayValue(),
		})
	}

	labels := make([]string, len(entries))
	for i, e := range entries {
		labels[i] = e.label
	}

	q := strings.ToLower(query)
	// RankFindFold is case-insensitive. RankFind returns matches in target-slice
	// order, not by distance — sort the returned ranks ourselves.
	fuzzyRanks := fuzzy.RankFindFold(q, labels)
	sort.Sort(fuzzyRanks)
	fuzzyOrder := make(map[int]int, len(fuzzyRanks))
	for i, r := range fuzzyRanks {
		fuzzyOrder[r.OriginalIndex] = i
	}

	// Score every candidate. Lower score = better. Literal substring hits beat
	// fuzzy hits; prefix-of-value beats substring-of-value beats substring-of-title.
	const (
		tierValuePrefix = iota
		tierValueSubstr
		tierTitleSubstr
		tierFuzzy
	)
	type scored struct {
		idx       int
		tier      int
		secondary int // tie-breaker within tier (e.g. fuzzy distance, match position)
	}
	scoredEntries := make([]scored, 0, len(entries))
	for i, e := range entries {
		valueLC := strings.ToLower(e.value)
		titleLC := strings.ToLower(e.title)
		switch {
		case strings.HasPrefix(valueLC, q):
			scoredEntries = append(scoredEntries, scored{i, tierValuePrefix, len(valueLC)})
		case strings.Contains(valueLC, q):
			scoredEntries = append(scoredEntries, scored{i, tierValueSubstr, strings.Index(valueLC, q)})
		case strings.Contains(titleLC, q):
			scoredEntries = append(scoredEntries, scored{i, tierTitleSubstr, strings.Index(titleLC, q)})
		default:
			if pos, ok := fuzzyOrder[i]; ok {
				scoredEntries = append(scoredEntries, scored{i, tierFuzzy, pos})
			}
		}
	}

	sort.SliceStable(scoredEntries, func(i, j int) bool {
		if scoredEntries[i].tier != scoredEntries[j].tier {
			return scoredEntries[i].tier < scoredEntries[j].tier
		}
		return scoredEntries[i].secondary < scoredEntries[j].secondary
	})

	for _, s := range scoredEntries {
		e := entries[s.idx]
		items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
			Title: e.title,
			Value: e.value,
		}))
	}

	return items, nil
}

func userInvocableSkillItems() []dialog.CompletionItemI {
	skills := skill.All()
	var items []dialog.CompletionItemI
	for _, s := range skills {
		if !s.IsUserInvocable() {
			continue
		}
		items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
			Title: "skill:" + s.Name + " — " + s.Description,
			Value: "skill:" + s.Name,
		}))
	}
	return items
}

func NewCommandCompletionProvider(commands []dialog.Command) dialog.CompletionProvider {
	return &commandCompletionProvider{
		commands: commands,
	}
}
