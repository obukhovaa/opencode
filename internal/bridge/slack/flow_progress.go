package slack

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	slackgo "github.com/slack-go/slack"
	nethtml "golang.org/x/net/html"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/markdown"
)

var _ bridge.FlowProgressEditor = (*Adapter)(nil)

func flowRichText(text string, structured bool) *slackgo.RichTextBlock {
	elem := slackgo.NewRichTextSectionTextElement(text, nil)
	if structured {
		return slackgo.NewRichTextBlock("", &slackgo.RichTextPreformatted{Type: slackgo.RTEPreformatted, Elements: []slackgo.RichTextSectionElement{elem}, Language: "json"})
	}
	elements := []slackgo.RichTextSectionElement{}
	tokenizer := nethtml.NewTokenizer(strings.NewReader(markdown.ToTelegramHTML(text)))
	style := slackgo.RichTextSectionTextStyle{}
	stack := []slackgo.RichTextSectionTextStyle{}
	links := []string{}
	link := ""
	linked := map[string]bool{}
	for {
		token := tokenizer.Next()
		if token == nethtml.ErrorToken {
			if tokenizer.Err() == io.EOF {
				break
			}
			break
		}
		if token == nethtml.TextToken {
			copy := style
			value := string(tokenizer.Text())
			if link != "" && !linked[link] {
				linked[link] = true
				elements = append(elements, slackgo.NewRichTextSectionLinkElement(link, value, &copy))
			} else {
				elements = append(elements, slackgo.NewRichTextSectionTextElement(value, &copy))
			}
		}
		if token == nethtml.StartTagToken {
			tag := tokenizer.Token()
			name := tag.Data
			stack = append(stack, style)
			links = append(links, link)
			if name == "a" {
				for _, attribute := range tag.Attr {
					if attribute.Key == "href" {
						parsed, err := url.Parse(attribute.Val)
						if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
							link = attribute.Val
						}
					}
				}
			}
			switch name {
			case "b", "strong":
				style.Bold = true
			case "i", "em":
				style.Italic = true
			case "s", "del":
				style.Strike = true
			case "code", "pre":
				style.Code = true
			}
		}
		if token == nethtml.EndTagToken && len(stack) > 0 {
			style = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			link = links[len(links)-1]
			links = links[:len(links)-1]
		}
	}
	return slackgo.NewRichTextBlock("", slackgo.NewRichTextSection(elements...))
}

func flowBlocks(p bridge.FlowProgress) []slackgo.Block {
	steps, omitted := bridge.FlowVisibleSteps(p, 24)
	tasks := make([]*slackgo.TaskCardBlock, 0, len(steps))
	budget := 6000
	for i, step := range steps {
		status := slackgo.TaskCardStatusPending
		switch step.Status {
		case "running":
			status = slackgo.TaskCardStatusInProgress
		case "completed", "skipped":
			status = slackgo.TaskCardStatusComplete
		case "failed", "interrupted":
			status = slackgo.TaskCardStatusError
		}
		card := slackgo.NewTaskCardBlock(fmt.Sprintf("step-%d-%s", i, bridge.LimitFlowText(step.ID, 64)), bridge.FlowStepLabel(step)).WithStatus(status)
		if details := bridge.FlowStepDetails(step, p.UpdatedAt); details != "" {
			card.WithDetails(flowRichText(details, false))
		}
		if p.Full && strings.TrimSpace(step.Output) != "" && budget > 0 {
			output, structured := bridge.FlowStepOutput(step, min(budget, 1500))
			budget -= bridge.FlowTextLen(output)
			card.WithOutput(flowRichText(output, structured))
		}
		if p.Full && strings.TrimSpace(step.Output) != "" && budget <= 0 && card.Output == nil {
			card.WithDetails(flowRichText(bridge.FlowStepDetails(step, p.UpdatedAt)+" · output omitted (message limit)", false))
		}
		tasks = append(tasks, card)
	}
	// Slack derives the collapsed plan's icon from its task statuses; changing
	// only the title cannot settle its spinner or show a run-level failure.
	if p.Status == "failed" || p.Status == "terminated" {
		hasError := false
		for _, task := range tasks {
			hasError = hasError || task.Status == slackgo.TaskCardStatusError
		}
		if !hasError {
			tasks = append(tasks, slackgo.NewTaskCardBlock("flow-outcome", "Workflow "+p.Status).WithStatus(slackgo.TaskCardStatusError))
		}
	}
	title := bridge.FlowProgressTitle(p)
	if omitted > 0 {
		title += fmt.Sprintf(" · %d steps omitted", omitted)
	}
	blocks := []slackgo.Block{}
	summary := p.Error
	if summary == "" {
		summary = p.Summary
	}
	if len(tasks) > 0 {
		blocks = append(blocks, slackgo.NewPlanBlock(title).WithTasks(tasks...))
	} else {
		blocks = append(blocks, slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, title, false, false), nil, nil))
	}
	if summary != "" {
		blocks = append(blocks, slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, bridge.LimitFlowText(bridge.FlowSafeText(summary), 650), false, false), nil, nil))
	}
	return blocks
}

func flowMessageOptions(p bridge.FlowProgress, rich bool) []slackgo.MsgOption {
	text := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(bridge.FlowProgressText(p))
	opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false), slackgo.MsgOptionDisableMarkdown(), slackgo.MsgOptionParse(false), slackgo.MsgOptionLinkNames(false), slackgo.MsgOptionDisableLinkUnfurl(), slackgo.MsgOptionDisableMediaUnfurl()}
	if rich {
		opts = append(opts, slackgo.MsgOptionBlocks(flowBlocks(p)...))
	}
	return opts
}

func (a *Adapter) SendFlowProgress(ctx context.Context, peer bridge.PeerRef, p bridge.FlowProgress) (bridge.EditableMessageToken, error) {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return "", ErrInvalidPeerID
	}
	post := func(rich bool) (string, error) {
		opts := flowMessageOptions(p, rich)
		if parsed.ThreadTS != "" {
			opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
		}
		_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
		return ts, err
	}
	ts, err := post(!a.flowBlocksUnsupported.Load())
	if err != nil && isBlockCapabilityError(err) {
		a.flowBlocksUnsupported.Store(true)
		ts, err = post(false)
	}
	if err != nil {
		a.recordFailure(err)
		return "", fmt.Errorf("slack: flow progress: %w", err)
	}
	if ts == "" {
		return "", fmt.Errorf("slack: flow progress: missing message timestamp")
	}
	return parsed.ChannelID + "\x00" + ts, nil
}

func (a *Adapter) EditFlowProgress(ctx context.Context, peer bridge.PeerRef, token bridge.EditableMessageToken, p bridge.FlowProgress) error {
	parts := strings.SplitN(token, "\x00", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] != ParsePeerID(peer.PeerID).ChannelID {
		return fmt.Errorf("slack: invalid flow progress token")
	}
	edit := func(rich bool) error {
		opts := flowMessageOptions(p, rich)
		if !rich {
			opts = append(opts, slackgo.MsgOptionBlocks([]slackgo.Block{}...))
		}
		_, _, _, err := a.api.UpdateMessageContext(ctx, parts[0], parts[1], opts...)
		return err
	}
	err := edit(!a.flowBlocksUnsupported.Load())
	if err != nil && isBlockCapabilityError(err) {
		a.flowBlocksUnsupported.Store(true)
		err = edit(false)
	}
	if err != nil && err.Error() == "message_not_found" {
		return bridge.ErrFlowMessageNotFound
	}
	return err
}

func (a *Adapter) ResolveFlowProgressPeer(peer bridge.PeerRef, token bridge.EditableMessageToken) string {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" || parsed.ThreadTS != "" || IsDM(parsed.ChannelID) {
		return peer.PeerID
	}
	parts := strings.SplitN(token, "\x00", 2)
	if len(parts) != 2 || parts[0] != parsed.ChannelID || parts[1] == "" {
		return ""
	}
	return FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: parts[1]})
}
