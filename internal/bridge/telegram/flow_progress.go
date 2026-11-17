package telegram

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	nethtml "golang.org/x/net/html"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/markdown"
)

var _ bridge.FlowProgressEditor = (*Adapter)(nil)

// Code, links and quotations are flattened before nesting prose in an expandable quote.
func flowInlineProse(text string, limit int) (string, int) {
	tokenizer := nethtml.NewTokenizer(strings.NewReader(markdown.ToTelegramHTML(text)))
	var b strings.Builder
	stack := []string{}
	used := 0
	link := ""
	allowed := func(tag string) bool {
		switch tag {
		case "b", "i", "s", "u", "tg-spoiler":
			return true
		}
		return false
	}
	for {
		kind := tokenizer.Next()
		if kind == nethtml.ErrorToken {
			break
		}
		if kind == nethtml.TextToken {
			value := bridge.FlowSafeText(string(tokenizer.Text()))
			clipped := bridge.LimitFlowText(value, limit-used)
			b.WriteString(html.EscapeString(clipped))
			used += bridge.FlowTextLen(clipped)
			if clipped != value {
				break
			}
		}
		if kind == nethtml.StartTagToken {
			tag := tokenizer.Token()
			name := tag.Data
			if name == "a" {
				for _, attr := range tag.Attr {
					if attr.Key == "href" {
						parsed, err := url.Parse(attr.Val)
						if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
							link = bridge.FlowSafeText(attr.Val)
						}
					}
				}
			}
			if allowed(name) {
				stack = append(stack, name)
				b.WriteString("<" + name + ">")
			}
		}
		if kind == nethtml.EndTagToken {
			tag, _ := tokenizer.TagName()
			name := string(tag)
			if name == "a" && link != "" {
				value := bridge.LimitFlowText(" ("+link+")", limit-used)
				b.WriteString(html.EscapeString(value))
				used += bridge.FlowTextLen(value)
				link = ""
			}
			if allowed(name) && len(stack) > 0 && stack[len(stack)-1] == name {
				stack = stack[:len(stack)-1]
				b.WriteString("</" + name + ">")
			}
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteString("</" + stack[i] + ">")
	}
	return b.String(), used
}

func flowHTML(p bridge.FlowProgress) string {
	// Budget visible UTF-16 units before escaping so tags/entities remain complete.
	summary := bridge.LimitFlowText(bridge.FlowProgressText(p), 2000)
	var b strings.Builder
	b.WriteString(html.EscapeString(summary))
	budget := 3850 - bridge.FlowTextLen(summary)
	shown, total := 0, 0
	for _, step := range p.Steps {
		if strings.TrimSpace(step.Output) != "" {
			total++
		}
	}
	if p.Full {
		steps, _ := bridge.FlowVisibleSteps(p, 8)
		for _, s := range steps {
			if strings.TrimSpace(s.Output) == "" {
				continue
			}
			heading := "\n\n" + bridge.LimitFlowText(bridge.FlowSafeText(s.ID), 80) + " output\n"
			if budget <= bridge.FlowTextLen(heading)+30 {
				b.WriteString("\n… More output omitted")
				break
			}
			limit := min(1000, budget-bridge.FlowTextLen(heading)-30)
			output, structured := bridge.FlowStepOutput(s, limit)
			outputHTML, visible := html.EscapeString(output), bridge.FlowTextLen(output)
			if !structured {
				outputHTML, visible = flowInlineProse(output, limit)
			}
			b.WriteString(html.EscapeString(heading))
			b.WriteString("<blockquote expandable>" + outputHTML + "</blockquote>")
			budget -= bridge.FlowTextLen(heading) + visible
			shown++
		}
	}
	if p.Full && total > shown {
		b.WriteString(fmt.Sprintf("\n… %d step outputs omitted", total-shown))
	}
	return b.String()
}

func (a *Adapter) SendFlowProgress(ctx context.Context, peer bridge.PeerRef, p bridge.FlowProgress) (bridge.EditableMessageToken, error) {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return "", err
	}
	disablePreview := true
	params := &tgbot.SendMessageParams{ChatID: chatID, Text: flowHTML(p), ParseMode: models.ParseModeHTML, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disablePreview}}
	msg, err := a.bot.SendMessage(ctx, params)
	if isParseError(err) {
		params.Text = bridge.FlowProgressText(p)
		params.ParseMode = ""
		msg, err = a.bot.SendMessage(ctx, params)
	}
	if err != nil {
		a.recordFailure(err)
		return "", err
	}
	if msg == nil {
		return "", fmt.Errorf("telegram: missing flow progress response")
	}
	return strconv.Itoa(msg.ID), nil
}

func (a *Adapter) EditFlowProgress(ctx context.Context, peer bridge.PeerRef, token bridge.EditableMessageToken, p bridge.FlowProgress) error {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(token)
	if err != nil || id <= 0 {
		return fmt.Errorf("telegram: invalid flow progress token")
	}
	disablePreview := true
	params := &tgbot.EditMessageTextParams{ChatID: chatID, MessageID: id, Text: flowHTML(p), ParseMode: models.ParseModeHTML, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disablePreview}}
	_, err = a.bot.EditMessageText(ctx, params)
	if isParseError(err) {
		params.Text = bridge.FlowProgressText(p)
		params.ParseMode = ""
		_, err = a.bot.EditMessageText(ctx, params)
	}
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "message is not modified") {
			return nil
		}
		if strings.Contains(message, "message to edit not found") {
			return bridge.ErrFlowMessageNotFound
		}
	}
	return err
}
