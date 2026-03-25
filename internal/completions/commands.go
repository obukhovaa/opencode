package completions

import (
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
	ranks := fuzzy.RankFind(q, labels)

	matched := make(map[int]bool, len(ranks))
	for _, r := range ranks {
		matched[r.OriginalIndex] = true
		e := entries[r.OriginalIndex]
		items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
			Title: e.title,
			Value: e.value,
		}))
	}

	// Also include exact substring matches that fuzzy may have missed
	for i, e := range entries {
		if matched[i] {
			continue
		}
		if strings.Contains(strings.ToLower(e.value), q) || strings.Contains(strings.ToLower(e.title), q) {
			items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
				Title: e.title,
				Value: e.value,
			}))
		}
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
