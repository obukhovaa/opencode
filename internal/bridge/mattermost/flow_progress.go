package mattermost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
)

var _ bridge.FlowProgressEditor = (*Adapter)(nil)

var flowActionLink = regexp.MustCompile(`(?i)mmaction:`)

func flowMarkdown(text string) string {
	// Action links need a registry and must never be interpreted from model output.
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(flowActionLink.ReplaceAllString(bridge.FlowSafeText(text), "mmaction∶"))
}

func flowProps(p bridge.FlowProgress) map[string]any {
	steps, omitted := bridge.FlowVisibleSteps(p, 24)
	blocks := []any{}
	budget := 6000
	for _, s := range steps {
		label := flowMarkdown(bridge.FlowStepLabel(s))
		if details := bridge.FlowStepDetails(s, p.UpdatedAt); details != "" {
			label += " · " + flowMarkdown(details)
		}
		label = bridge.LimitFlowText(label, 220)
		header := map[string]any{"type": "text", "text": label}
		if p.Full && strings.TrimSpace(s.Output) != "" && budget > 0 {
			output, structured := bridge.FlowStepOutput(s, min(1500, budget))
			if structured {
				output = bridge.LimitFlowText(strings.ReplaceAll(output, "`", `\u0060`), min(1400, budget))
			} else {
				output = bridge.LimitFlowText(flowMarkdown(output), min(1400, budget))
			}
			budget -= bridge.FlowTextLen(output)
			if structured {
				output = "```json\n" + output + "\n```"
			}
			blocks = append(blocks, map[string]any{"type": "collapsible", "collapsed": true, "header": []any{header}, "content": []any{map[string]any{"type": "text", "text": output}}})
		} else {
			if p.Full && strings.TrimSpace(s.Output) != "" {
				header["text"] = label + " · output omitted (message limit)"
			}
			blocks = append(blocks, header)
		}
	}
	if omitted > 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": fmt.Sprintf("… %d more steps", omitted), "is_subtle": true})
	}
	return map[string]any{"mm_blocks": blocks}
}

func (a *Adapter) SendFlowProgress(ctx context.Context, peer bridge.PeerRef, p bridge.FlowProgress) (bridge.EditableMessageToken, error) {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return "", fmt.Errorf("mattermost: invalid flow peer")
	}
	post, err := a.client.CreatePost(ctx, CreatePostInput{ChannelID: parsed.ChannelID, RootID: parsed.RootPostID, Message: bridge.LimitFlowText(flowMarkdown(bridge.FlowProgressText(p)), 3000), Props: flowProps(p)})
	if err != nil {
		a.recordFailure(err)
		return "", err
	}
	if post.ID == "" {
		return "", fmt.Errorf("mattermost: missing flow progress post id")
	}
	return post.ID, nil
}

func (a *Adapter) EditFlowProgress(ctx context.Context, _ bridge.PeerRef, token bridge.EditableMessageToken, p bridge.FlowProgress) error {
	if token == "" {
		return fmt.Errorf("mattermost: empty flow progress token")
	}
	_, err := a.client.UpdatePost(ctx, UpdatePostInput{PostID: token, Message: bridge.LimitFlowText(flowMarkdown(bridge.FlowProgressText(p)), 3000), Props: flowProps(p)})
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
		return bridge.ErrFlowMessageNotFound
	}
	return err
}

func (a *Adapter) ResolveFlowProgressPeer(peer bridge.PeerRef, token bridge.EditableMessageToken) string {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" || parsed.RootPostID != "" {
		return peer.PeerID
	}
	if token == "" {
		return ""
	}
	return parsed.ChannelID + "|" + token
}
