package completions

import (
	"strings"

	"github.com/lithammer/fuzzysearch/fuzzy"
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

	if query == "" {
		for _, cmd := range p.commands {
			items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
				Title: cmd.Title,
				Value: cmd.ID,
			}))
		}
		return items, nil
	}

	titles := make([]string, len(p.commands))
	for i, cmd := range p.commands {
		titles[i] = cmd.ID + " " + cmd.Title
	}

	q := strings.ToLower(query)
	ranks := fuzzy.RankFind(q, titles)

	matched := make(map[int]bool, len(ranks))
	for _, r := range ranks {
		matched[r.OriginalIndex] = true
		cmd := p.commands[r.OriginalIndex]
		items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
			Title: cmd.Title,
			Value: cmd.ID,
		}))
	}

	// Also include exact substring matches on ID that fuzzy may have missed
	for i, cmd := range p.commands {
		if matched[i] {
			continue
		}
		if strings.Contains(strings.ToLower(cmd.ID), q) || strings.Contains(strings.ToLower(cmd.Title), q) {
			items = append(items, dialog.NewCompletionItem(dialog.CompletionItem{
				Title: cmd.Title,
				Value: cmd.ID,
			}))
		}
	}

	return items, nil
}

func NewCommandCompletionProvider(commands []dialog.Command) dialog.CompletionProvider {
	return &commandCompletionProvider{
		commands: commands,
	}
}
