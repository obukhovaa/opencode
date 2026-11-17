package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
)

var errFlowCardDeferred = errors.New("flow card update deferred")

func flowCardRecipient(peer bridge.PeerRef) string {
	peer.Mention = ""
	return peerReceiptKey(peer)
}

func (s *Service) canEditFlowProgress(peer bridge.PeerRef) bool {
	if peer.Channel == "external" || s.app == nil || s.app.FlowRuns == nil {
		return false
	}
	adapter := s.Adapter(peer.Channel, peer.Identity)
	if _, ok := adapter.(bridge.FlowProgressEditor); ok {
		return true
	}
	_, ok := adapter.(bridge.MessageEditor)
	return ok
}

func (s *Service) flushFlowCards(ctx context.Context) {
	s.flowCardMu.Lock()
	pending := s.flowCardPending
	s.flowCardPending = nil
	s.flowCardMu.Unlock()
	for _, event := range pending {
		s.deliverSessionFlowEvent(ctx, event)
	}
}

func (s *Service) loadFlowCard(ctx context.Context, peer bridge.PeerRef, event app.FlowRunEvent, lease flowrun.Lease) (flowrun.Card, error) {
	recipient := flowCardRecipient(peer)
	card, err := s.app.FlowRuns.GetCard(ctx, event.RunID, recipient)
	if !errors.Is(err, flowrun.ErrNotFound) {
		return card, err
	}
	// A normal first reply can resolve a channel binding to a thread. Move only
	// an unambiguous abandoned channel card; independent thread bindings stay separate.
	base, _, threaded := strings.Cut(peer.PeerID, "|")
	if threaded && (peer.Channel == "slack" || peer.Channel == "mattermost") {
		_, baseErr := s.store.GetBinding(ctx, s.projectID, peer.Channel, peer.Identity, base)
		if errors.Is(baseErr, store.ErrNotFound) {
			bindings, listErr := s.store.ListBindingsBySession(ctx, s.projectID, event.ConversationID)
			if listErr != nil {
				return card, listErr
			}
			matches := 0
			for _, b := range bindings {
				if b.Channel == peer.Channel && b.IdentityID == peer.Identity && strings.HasPrefix(b.PeerID, base+"|") {
					matches++
				}
			}
			if matches == 1 {
				old := peer
				old.PeerID = base
				moved, moveErr := s.app.FlowRuns.MoveCard(ctx, lease, flowCardRecipient(old), recipient)
				if moveErr == nil {
					return moved, nil
				}
				if !errors.Is(moveErr, flowrun.ErrNotFound) {
					return card, moveErr
				}
			}
		} else if baseErr != nil {
			return card, baseErr
		}
	}
	return flowrun.Card{RunID: event.RunID, Recipient: recipient}, nil
}

func (s *Service) deliverFlowCard(ctx context.Context, peer bridge.PeerRef, event app.FlowRunEvent, lease flowrun.Lease) error {
	s.flowCardMu.Lock()
	defer s.flowCardMu.Unlock()
	card, err := s.loadFlowCard(ctx, peer, event, lease)
	if err != nil {
		return err
	}
	peer, card, err = s.finalizeFlowCardBinding(ctx, peer, lease, card)
	if err != nil {
		return err
	}
	progress := bridge.FlowProgress{RunID: event.RunID}
	if len(card.State) > 0 {
		if err = json.Unmarshal(card.State, &progress); err != nil {
			return fmt.Errorf("decode flow card: %w", err)
		}

	}
	record, err := s.app.FlowRuns.Get(ctx, event.RunID)
	if err != nil {
		return err
	}
	if progress.StartedAt == 0 {
		progress.StartedAt = record.CreatedAt
	}
	if progress.Status == "" {
		progress.Status = "planning"
	}
	if err = refreshFlowCardPlan(&progress, record); err != nil {
		return err
	}

	sequence := card.Sequence
	urgent := false
	for {
		events, readErr := s.app.FlowRuns.Events(ctx, event.RunID, sequence, 100)
		if readErr != nil {
			return readErr
		}
		for _, item := range events {
			var next app.FlowRunEvent
			if err = json.Unmarshal(item.Payload, &next); err != nil {
				return err
			}
			next.Sequence = item.Sequence
			urgent = foldFlowProgress(&progress, next) || urgent
			sequence = item.Sequence
		}
		if len(events) < 100 {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if sequence < event.Sequence {
		return errors.New("flow event not yet persisted")
	}
	if err = refreshFlowCardPlan(&progress, record); err != nil {
		return err
	}
	progress.Full = s.ToolVerbosity() == bridge.ToolUpdateVerbosityFull
	s.cfgMu.RLock()
	enabled := s.cfg != nil && s.cfg.ToolUpdatesEnabled
	s.cfgMu.RUnlock()
	if !enabled && !flowCardSignificant(progress) {
		// Persist the folded state even while muted; enabling updates later must
		// not lose the successful steps hidden by this preference.
		card.Sequence = sequence
		card.State, err = json.Marshal(progress)
		if err != nil {
			return err
		}
		return s.app.FlowRuns.SaveCard(ctx, lease, card)
	}
	rendered := flowCardPresentation(progress)
	payload, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	if card.MessageToken != "" && hash == card.RenderHash {
		if sequence == card.Sequence {
			return nil
		}
		card.Sequence = sequence
		card.State, err = json.Marshal(progress)
		if err != nil {
			return err
		}
		return s.app.FlowRuns.SaveCard(ctx, lease, card)
	}
	if card.MessageToken != "" && !urgent && time.Now().UnixMilli()-card.UpdatedAt < 2000 {
		if s.flowCardPending == nil {
			s.flowCardPending = map[string]app.FlowRunEvent{}
		}
		s.flowCardPending[event.RunID] = event
		return errFlowCardDeferred
	}
	if err = s.app.FlowRuns.CheckDeliveryLease(ctx, lease); err != nil {
		return err
	}
	adapter := s.Adapter(peer.Channel, peer.Identity)
	native, nativeOK := adapter.(bridge.FlowProgressEditor)
	editor, editableOK := adapter.(bridge.MessageEditor)
	if !nativeOK && !editableOK {
		return errors.New("flow progress adapter unavailable")
	}
	send := func() (string, error) {
		if nativeOK {
			return native.SendFlowProgress(ctx, peer, rendered)
		}
		return editor.SendEditable(ctx, peer, bridge.FlowProgressText(rendered))
	}
	if card.MessageToken == "" {
		card.MessageToken, err = send()
	} else {
		if nativeOK {
			err = native.EditFlowProgress(ctx, peer, card.MessageToken, rendered)
		} else {
			err = editor.EditMessage(ctx, peer, card.MessageToken, bridge.FlowProgressText(rendered))
		}
		if errors.Is(err, bridge.ErrFlowMessageNotFound) {
			if err = s.app.FlowRuns.CheckDeliveryLease(ctx, lease); err != nil {
				return err
			}
			card.MessageToken, err = send()
		}
	}
	if err != nil {
		return err
	}
	if card.MessageToken == "" {
		return errors.New("flow progress send returned no message identity")
	}
	card.Sequence = sequence
	card.RenderHash = hash
	card.State, err = json.Marshal(progress)
	if err != nil {
		return err
	}
	if err = s.app.FlowRuns.SaveCard(ctx, lease, card); err != nil {
		return err
	}
	_, _, err = s.finalizeFlowCardBinding(ctx, peer, lease, card)
	return err
}

func (s *Service) finalizeFlowCardBinding(ctx context.Context, peer bridge.PeerRef, lease flowrun.Lease, card flowrun.Card) (bridge.PeerRef, flowrun.Card, error) {
	if card.MessageToken == "" {
		return peer, card, nil
	}
	resolver, ok := s.Adapter(peer.Channel, peer.Identity).(bridge.FlowProgressPeerResolver)
	if !ok {
		return peer, card, nil
	}
	resolved := resolver.ResolveFlowProgressPeer(peer, card.MessageToken)
	if resolved == "" || resolved == peer.PeerID {
		return peer, card, nil
	}
	if err := s.app.FlowRuns.CheckDeliveryLease(ctx, lease); err != nil {
		return peer, card, err
	}
	if err := s.store.UpdateBindingPeerID(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID, resolved); err != nil {
		return peer, card, err
	}
	next := peer
	next.PeerID = resolved
	moved, err := s.app.FlowRuns.MoveCard(ctx, lease, card.Recipient, flowCardRecipient(next))
	return next, moved, err
}

func flowCardSignificant(p bridge.FlowProgress) bool {
	switch p.Status {
	case "completed", "failed", "terminated", "suspended", "postponed", "waiting_for_input":
		return true
	}
	for _, step := range p.Steps {
		if step.Status == "failed" || step.Status == "waiting_for_input" || step.Status == "postponed" {
			return true
		}
	}
	return false
}

func foldFlowProgress(p *bridge.FlowProgress, e app.FlowRunEvent) bool {
	if e.Type == "session.flow.messages.delivered" {
		return false
	}
	at := e.OccurredAt
	if at == 0 {
		at = p.UpdatedAt
		if at == 0 {
			at = p.StartedAt
		}
	}
	p.UpdatedAt = max(p.UpdatedAt, at)
	if e.FlowID != "" {
		p.FlowID = e.FlowID
	}
	switch e.Type {
	case "session.flow.planning":
		p.Status = "planning"
	case "session.flow.started":
		p.Status = "running"
		p.Error = ""
	case "session.flow.step":
		index := -1
		for i := range p.Steps {
			if p.Steps[i].ID == e.StepID {
				index = i
				break
			}
		}
		if index < 0 {
			if len(p.Steps) >= 256 {
				index = len(p.Steps) - 1
				for i := len(p.Steps) - 1; i >= 0; i-- {
					if flowCardPriority(p.Steps[i].Status) < flowCardPriority(p.Steps[index].Status) {
						index = i
					}
				}
				p.Steps[index] = bridge.FlowProgressStep{ID: e.StepID}
			} else {
				p.Steps = append(p.Steps, bridge.FlowProgressStep{ID: e.StepID})
				index = len(p.Steps) - 1
			}
			p.TotalSteps = max(p.TotalSteps, len(p.Steps))
		}
		step := &p.Steps[index]
		if e.Status == "running" && (step.Status != "running" || e.Iteration != step.Iteration) {
			step.StartedAt = e.OccurredAt
			step.FinishedAt = 0
			step.Output = ""
		}
		step.Status = e.Status
		step.Iteration = e.Iteration
		if e.Agent != "" {
			step.Agent = e.Agent
		}
		if e.Status == "completed" || e.Status == "failed" || e.Status == "postponed" {
			step.FinishedAt = e.OccurredAt
		}
		if e.Output != "" {
			step.Output = flowCardOutput(e.Output)
			step.IsStructOutput = e.IsStructOutput || json.Valid([]byte(e.Output))
		}
		if e.Status == "completed" {
			p.Summary = flowCardSummary(e.Output)
		}
		if e.Status == "failed" {
			p.Error = flowCardLimit(e.Output, 800)
			if p.Error == "" {
				p.Error = "Step " + e.StepID + " failed."
			}
		}
		if e.Status == "running" {
			p.Status = "running"
		}
		if e.Status == "waiting_for_input" || e.Status == "postponed" {
			p.Status = e.Status
		}
	case "session.flow.completed":
		p.Status = "completed"
	case "session.flow.failed":
		p.Status = "failed"
		if e.Output != "" {
			p.Error = flowCardLimit(e.Output, 800)
		} else if p.Error == "" {
			p.Error = "Workflow failed; inspect its session for details."
		}
	case "session.flow.suspended":
		p.Status = "suspended"
		p.Error = flowCardLimit(e.Output, 800)
	case "session.flow.terminated":
		p.Status = "terminated"
	default:
		return false
	}
	switch e.Type {
	case "session.flow.suspended", "session.flow.terminated", "session.flow.failed":
		for i := range p.Steps {
			if p.Steps[i].Status == "running" || p.Steps[i].Status == "waiting_for_input" {
				if e.Type == "session.flow.suspended" {
					p.Steps[i].Status = "suspended"
				} else {
					p.Steps[i].Status = "interrupted"
				}
				p.Steps[i].FinishedAt = e.OccurredAt
			}
		}
	}
	// Keep the newest outputs within a total storage budget. Full transcripts
	// remain in the flow's sessions; the card is only a bounded presentation.
	remaining := 24000
	for i := len(p.Steps) - 1; i >= 0; i-- {
		n := len([]rune(p.Steps[i].Output))
		if n > remaining {
			if remaining < 50 {
				p.Steps[i].Output = "[Output omitted; full output in session]"
				p.Steps[i].IsStructOutput = false
			} else {
				p.Steps[i].Output = flowCardLimit(p.Steps[i].Output, remaining)
			}
			n = len([]rune(p.Steps[i].Output))
		}
		remaining = max(0, remaining-n)
	}
	return flowCardSignificant(*p)
}

func flowCardLimit(text string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	const suffix = "… [truncated; full output in session]"
	tail := []rune(suffix)
	if n < len(tail) {
		return string(tail[:n])
	}
	return string(r[:n-len(tail)]) + suffix
}

func flowCardPresentation(p bridge.FlowProgress) bridge.FlowProgress {
	p.Steps = append([]bridge.FlowProgressStep(nil), p.Steps...)
	if p.Status != "completed" {
		p.Summary = ""
	}
	if !p.Full {
		for i := range p.Steps {
			p.Steps[i].Output = ""
		}
	}
	return p
}

func flowCardOutput(text string) string {
	if len([]rune(text)) > 3000 && json.Valid([]byte(text)) {
		var formatted bytes.Buffer
		if json.Indent(&formatted, []byte(text), "", "  ") == nil {
			text = formatted.String()
		}
	}
	return flowCardLimit(text, 3000)
}

func refreshFlowCardPlan(p *bridge.FlowProgress, record flowrun.Run) error {
	var definition struct {
		Manifest flow.DynamicManifest `json:"manifest"`
	}
	if err := json.Unmarshal(record.Snapshot, &definition); err != nil {
		return err
	}
	steps := definition.Manifest.Definition.Spec.Steps
	if len(steps) == 0 {
		return nil
	}
	p.FlowID = definition.Manifest.Definition.ID
	p.TotalSteps = len(steps)
	prior := map[string]bridge.FlowProgressStep{}
	for _, step := range p.Steps {
		prior[step.ID] = step
	}
	p.Steps = nil
	for _, step := range steps {
		item, ok := prior[step.ID]
		if !ok {
			item = bridge.FlowProgressStep{ID: step.ID, Status: "pending"}
		}
		item.Agent = step.Agent
		if len(p.Steps) < 256 {
			p.Steps = append(p.Steps, item)
		} else if item.Status != "pending" {
			candidate := -1
			for i := len(p.Steps) - 1; i >= 0; i-- {
				if flowCardPriority(p.Steps[i].Status) < flowCardPriority(item.Status) && (candidate < 0 || flowCardPriority(p.Steps[i].Status) < flowCardPriority(p.Steps[candidate].Status)) {
					candidate = i
				}
			}
			if candidate >= 0 {
				p.Steps[candidate] = item
			}
		}
	}
	order := map[string]int{}
	for i, step := range steps {
		order[step.ID] = i
	}
	sort.SliceStable(p.Steps, func(i, j int) bool { return order[p.Steps[i].ID] < order[p.Steps[j].ID] })
	return nil
}

func flowCardPriority(status string) int {
	switch status {
	case "failed":
		return 4
	case "running", "waiting_for_input", "postponed":
		return 3
	case "completed":
		return 2
	default:
		return 1
	}
}
func flowCardSummary(output string) string {
	if json.Valid([]byte(output)) {
		var fields map[string]any
		if json.Unmarshal([]byte(output), &fields) == nil {
			for _, key := range []string{"summary", "message", "result"} {
				if text, ok := fields[key].(string); ok {
					return flowCardLimit(text, 500)
				}
			}
		}
		return ""
	}
	return flowCardLimit(output, 500)
}
