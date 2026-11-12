package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

// sessionFlowController keeps bridge routing independent of the engine's turns.
// A question or a later inbound belongs to the conversation even though a flow
// currently runs a distinct child session.
type sessionFlowController interface {
	CurrentFlow(context.Context, string) (flowrun.Run, error)
	QueueFlowMessages(context.Context, string, []app.FlowSteeringMessage) error
	ControlFlow(context.Context, string, string, string) error
	SessionFlowConversation(context.Context, string) (string, bool, error)
	SubscribeFlowEvents(context.Context) <-chan pubsub.Event[app.FlowRunEvent]
	AckFlowEvent(context.Context, string, string, string) error
	FlowLease(string) (flowrun.Lease, bool)
}

func (s *Service) flowController() sessionFlowController {
	if s.sessionFlows != nil {
		return s.sessionFlows
	}
	if s.app == nil || s.app.FlowRuns == nil {
		return nil
	}
	controller, _ := any(s.app).(sessionFlowController)
	return controller
}
func flowIsTerminal(status string) bool {
	switch status {
	case "completed", "terminated":
		return true
	}
	return false
}
func (s *Service) conversationFlow(ctx context.Context, id string) (flowrun.Run, bool) {
	c := s.flowController()
	if c == nil {
		return flowrun.Run{}, false
	}
	run, err := c.CurrentFlow(ctx, id)
	return run, err == nil && !flowIsTerminal(run.Status)
}
func peerReceiptKey(peer bridge.PeerRef) string {
	b, _ := json.Marshal(peer)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *Service) routeSessionFlowInput(ctx context.Context, conversation string, in bridge.Inbound) bool {
	_, active := s.conversationFlow(ctx, conversation)
	if !active {
		return false
	}
	if s.app != nil && s.app.Questions != nil {
		for _, req := range s.app.Questions.List() {
			if req.SessionID == conversation && req.FlowRunID == "" {
				return false
			}
		}
	}

	if len(in.Attachments) > 0 {
		s.replyToPeer(ctx, in.Peer, "Flow steering currently accepts text. Send attachments after the flow completes.", false, conversation)
		return true
	}
	origin, _ := json.Marshal(in.Peer)
	messages := make([]app.FlowSteeringMessage, 0, max(1, len(in.Messages)))
	if len(in.Messages) > 0 {
		for _, m := range in.Messages {
			messages = append(messages, app.FlowSteeringMessage{ID: m.ID, Text: PrependAttributionIfMultiPeer(in.Peer, m.Text, s.sessionPeerCount(ctx, conversation)), Origin: origin})
		}
	} else {
		messages = append(messages, app.FlowSteeringMessage{Text: PrependAttributionIfMultiPeer(in.Peer, in.Text, s.sessionPeerCount(ctx, conversation)), Origin: origin})
	}
	if s.questionRouter != nil {
		s.questionRouter.mu.Lock()
		visible := s.questionRouter.flowVisible[conversation]
		s.questionRouter.mu.Unlock()
		ctx = question.WithSteeringQuestion(ctx, visible)
	}
	if err := s.flowController().QueueFlowMessages(ctx, conversation, messages); err != nil {
		s.replyToPeer(ctx, in.Peer, "Could not queue flow input: "+err.Error(), false, conversation)
	}
	return true
}

func (s *Service) runSessionFlowOutput(ctx context.Context) {
	controller := s.flowController()
	if controller == nil {
		return
	}
	events := controller.SubscribeFlowEvents(ctx)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	s.replaySessionFlowEvents(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.questionRouter != nil {
				s.questionRouter.advanceFlowQuestions(ctx)
			}
			s.replaySessionFlowEvents(ctx)
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Payload.Type == "session.flow.messages.delivered" && s.questionRouter != nil {
				s.questionRouter.advanceFlowQuestions(ctx)
			}
			s.deliverSessionFlowEvent(ctx, event.Payload)
		}
	}
}
func (s *Service) deliverSessionFlowEvent(ctx context.Context, event app.FlowRunEvent) {
	if s.store == nil || event.ConversationID == "" {
		return
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, event.ConversationID)
	if err != nil {
		logging.Warn("bridge: session flow binding lookup", "err", err)
		return
	}
	if s.app != nil && s.app.FlowRuns != nil {
		pending := bindings[:0]
		for _, binding := range bindings {
			done, e := s.app.FlowRuns.Delivered(ctx, event.RunID, event.ID, peerReceiptKey(binding.AsPeerRef()))
			if e != nil {
				return
			}
			if !done {
				pending = append(pending, binding)
			}
		}
		bindings = pending
	}
	if len(bindings) == 0 {
		return
	}
	var deliveryLease flowrun.Lease
	if s.app != nil && s.app.FlowRuns != nil {
		var local bool
		deliveryLease, local = s.flowController().FlowLease(event.RunID)
		if !local {
			run, getErr := s.app.FlowRuns.Get(ctx, event.RunID)
			if getErr != nil {
				return
			}
			deliveryLease, err = s.app.FlowRuns.ClaimDelivery(ctx, event.RunID, "bridge-delivery-"+uuid.NewString(), time.Minute)
			if err != nil {
				return
			}
			defer s.app.FlowRuns.Release(context.WithoutCancel(ctx), deliveryLease, run.Status)
		}
	}
	var body string
	switch event.Type {
	case "session.flow.planning":
		body = "Planning a workflow…"
	case "session.flow.started":
		body = "Workflow started."
	case "session.flow.step":
		body = fmt.Sprintf("%s · %s", event.StepID, event.Status)
		if event.Status == "completed" && event.Output != "" {
			body += "\n\n" + event.Output
		}
	case "session.flow.completed":
		body = "Workflow completed."
	case "session.flow.failed":
		body = "Workflow failed."
	case "session.flow.suspended":
		body = "Workflow paused. Use /flow resume to continue or /flow terminate to end it."
	case "session.flow.terminated":
		body = "Workflow terminated."
	case "session.flow.messages.delivered":
	default:
		return
	}
	if event.Type != "session.flow.step" && event.Output != "" {
		body += "\n\n" + event.Output
	}
	for _, binding := range bindings {
		peer := binding.AsPeerRef()
		recipient := peerReceiptKey(peer)
		if s.app != nil && s.app.FlowRuns != nil {
			delivered, checkErr := s.app.FlowRuns.Delivered(ctx, event.RunID, event.ID, recipient)
			if checkErr != nil || delivered {
				continue
			}
		}
		if s.app != nil && s.app.FlowRuns != nil {
			if checkErr := s.app.FlowRuns.CheckDeliveryLease(ctx, deliveryLease); checkErr != nil {
				return
			}
		}
		deliveryCtx, cancelDelivery := context.WithTimeout(ctx, 20*time.Second)
		sendCtx := bridge.ContextWithSessionID(deliveryCtx, event.ConversationID)
		sendCtx = bridge.ContextWithSessionFlowEvent(sendCtx, bridge.SessionFlowEventContext{ID: event.ID, RunID: event.RunID, Type: event.Type, Sequence: event.Sequence, Fence: func() int64 {
			if deliveryLease.Token > 0 {
				return deliveryLease.Token
			}
			return event.Fence
		}()})
		if event.Type == "session.flow.messages.delivered" {
			ids := []string{}
			for _, m := range event.Messages {
				var sender bridge.PeerRef
				if json.Unmarshal(m.Origin, &sender) == nil && sender.Channel == peer.Channel && sender.Identity == peer.Identity && sender.PeerID == peer.PeerID && m.ID != "" {
					ids = append(ids, m.ID)
				}
			}
			if len(ids) == 0 {
				cancelDelivery()
				continue
			}
			if adapter, ok := s.Adapter(peer.Channel, peer.Identity).(bridge.DeliveryAcker); ok {
				err = adapter.SendDelivered(sendCtx, peer, ids)
			} else {
				err = nil
			}
		} else {
			_, err = s.Send(sendCtx, peer, body, "", nil)
		}
		cancelDelivery()
		if err != nil {
			logging.Warn("bridge: session flow delivery failed", "run", event.RunID, "event", event.ID, "err", err)
			continue
		}
		if s.app != nil && s.app.FlowRuns != nil {
			err = s.app.FlowRuns.AckEvent(ctx, deliveryLease, event.ID, recipient)
		} else {
			err = s.flowController().AckFlowEvent(ctx, event.RunID, event.ID, recipient)
		}
		if err != nil {
			logging.Warn("bridge: session flow delivery receipt failed", "run", event.RunID, "err", err)
		}
	}
}

// ValidateSessionFlowOrigin checks API-supplied origin against an existing
// binding. API authentication alone does not authorize a different tenant peer.
func (s *Service) ValidateSessionFlowOrigin(ctx context.Context, conversation string, peer bridge.PeerRef, jobID string) error {
	if s == nil || s.store == nil {
		return errors.New("bridge unavailable")
	}
	b, err := s.store.GetBinding(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	if err != nil {
		return fmt.Errorf("flow origin binding unavailable: %w", err)
	}
	if b.SessionID != conversation {
		return errors.New("flow origin does not own conversation")
	}
	if jobID != "" && jobID != s.RemoteJobID() {
		return errors.New("flow origin job does not own this worker")
	}
	return nil
}
func (s *Service) cmdFlow(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		return replyText(err.Error())
	}
	action := strings.TrimSpace(in.CommandArgs)
	if action == "resume" || action == "terminate" || action == "cancel" {
		c := s.flowController()
		if c == nil {
			return replyText("Session flows are unavailable.")
		}
		lifetime := s.ctx
		if lifetime == nil {
			lifetime = context.Background()
		}
		commandCtx := tools.WithRequester(lifetime, s.requesterFor(ctx, in))
		commandCtx = tools.WithPeer(commandCtx, tools.Peer{Channel: in.Peer.Channel, Identity: in.Peer.Identity, PeerID: in.Peer.PeerID})
		commandCtx = tools.WithJobArgs(commandCtx, s.jobArgsForTurn(binding.SessionID, in.Peer))
		commandCtx, releaseAdmission := s.flowAdmissionTurn(commandCtx, binding.SessionID)
		go func() {
			defer releaseAdmission()
			callCtx, cancel := context.WithTimeout(commandCtx, 30*time.Second)
			defer cancel()
			if callCtx.Err() != nil {
				return
			}
			if err := c.ControlFlow(callCtx, binding.SessionID, "", action); err != nil {
				s.replyToPeer(callCtx, in.Peer, err.Error(), false, binding.SessionID)
			}
		}()
		return replyText("Flow control requested: " + action)
	}
	controller := s.flowController()
	if controller == nil {
		return replyText("Session flows are unavailable.")
	}
	run, err := controller.CurrentFlow(ctx, binding.SessionID)
	if err != nil {
		return replyText("No flow in this conversation. Ask the agent to plan a workflow or run an existing flow.")
	}
	return replyText(fmt.Sprintf("Flow %s · %s · %s\n/flow resume · /flow terminate", run.ID, run.Kind, run.Status))
}

type sessionFlowAdmission struct {
	ctx       context.Context
	cancel    context.CancelFunc
	jobID     string
	peer      bridge.PeerRef
	requester string
	args      map[string]any
	deadline  time.Time
}

func (s *Service) AdmitSessionFlow(ctx context.Context, sessionID string, peer bridge.PeerRef, jobID string) error {
	if jobID == "" || sessionID == "" {
		return errors.New("session and job identity required")
	}
	s.mu.Lock()
	if s.sessionFlowOwners == nil {
		s.sessionFlowOwners = map[string]sessionFlowAdmission{}
	}
	if prior, exists := s.sessionFlowOwners[sessionID]; exists && prior.jobID == jobID && prior.peer == peer {
		// Refresh trusted admission identity for a later top-level invocation;
		// an already-running flow keeps the immutable origin captured at creation.
		prior.requester = tools.RequesterFromContext(ctx)
		prior.args, _ = tools.JobArgsFromContext(ctx)
		if deadline, ok := ctx.Deadline(); ok {
			prior.deadline = deadline
		}
		s.sessionFlowOwners[sessionID] = prior
		s.mu.Unlock()
		return nil
	}
	for sid, owner := range s.sessionFlowOwners {
		if (s.poolMode && sid != sessionID) || (sid == sessionID && owner.jobID != jobID) {
			s.mu.Unlock()
			return errors.New("worker already owns another conversation")
		}
	}
	if s.sessionFlowBridge == nil {
		s.sessionFlowBridge = s.InteractiveHook()
	}
	hook := s.sessionFlowBridge
	s.mu.Unlock()
	// Caller holds the pool admission gate while this shared job identity changes.
	s.SetRemoteJobID(jobID)
	if s.HeartbeatsEnabled() {
		results, err := s.Bind(ctx, sessionID, []bridge.PeerRef{peer})
		if err != nil {
			return err
		}
		for _, result := range results {
			if result.Err != nil {
				return result.Err
			}
		}
	} else if err := hook.OnInteractiveStepStart(ctx, sessionID, []bridge.PeerRef{peer}); err != nil {
		return err
	}
	args, _ := tools.JobArgsFromContext(ctx)
	admission := sessionFlowAdmission{jobID: jobID, peer: peer, requester: tools.RequesterFromContext(ctx), args: args}
	admission.deadline, _ = ctx.Deadline()
	lifetime := s.ctx
	if lifetime == nil {
		lifetime = context.Background()
	}
	admission.ctx, admission.cancel = context.WithCancel(lifetime)
	s.mu.Lock()
	s.sessionFlowOwners[sessionID] = admission
	delete(s.sessionFlowRetired, sessionID)
	s.mu.Unlock()
	return nil
}
func (s *Service) ReleaseSessionFlow(ctx context.Context, sessionID, jobID string) error {
	return agent.WithIdleSession(sessionID, func() error {
		if run, active := s.conversationFlow(ctx, sessionID); active && run.Status != "suspended" && run.Status != "failed" {
			return errors.New("conversation still owns a nonterminal flow")
		}
		s.mu.Lock()
		owner, ok := s.sessionFlowOwners[sessionID]
		hook := s.sessionFlowBridge
		if !ok || owner.jobID != jobID {
			s.mu.Unlock()
			return errors.New("conversation admission not owned")
		}
		if s.sessionFlowRetired == nil {
			s.sessionFlowRetired = map[string]bool{}
		}
		s.sessionFlowRetired[sessionID] = true
		if owner.cancel != nil {
			owner.cancel()
		}
		s.mu.Unlock()
		if s.HeartbeatsEnabled() {
			if err := s.Unbind(ctx, sessionID); err != nil {
				return err
			}
		} else if hook != nil {
			if err := hook.OnInteractiveStepComplete(ctx, sessionID); err != nil {
				return err
			}
		}
		s.mu.Lock()
		delete(s.sessionFlowOwners, sessionID)
		s.mu.Unlock()
		return nil
	})
}
func (s *Service) BridgeSessionFlowContext(ctx context.Context, conversation string, peer bridge.PeerRef, jobID string) (context.Context, error) {
	if err := s.ValidateSessionFlowOrigin(ctx, conversation, peer, jobID); err != nil {
		return ctx, err
	}
	s.mu.Lock()
	owner, ok := s.sessionFlowOwners[conversation]
	s.mu.Unlock()
	ctx = tools.WithPeer(ctx, tools.Peer{Channel: peer.Channel, Identity: peer.Identity, PeerID: peer.PeerID})
	if ok {
		if owner.jobID != jobID {
			return ctx, errors.New("job does not own admitted conversation")
		}
		ctx = tools.WithRequester(ctx, owner.requester)
		ctx = tools.WithJobArgs(ctx, owner.args)
		// The API/controller owns cancellation; preserve the admission's deadline
		// as metadata instead of installing an unowned cancelable child context.
		ctx = tools.WithSessionFlowDeadline(ctx, owner.deadline)
	} else {
		ctx = tools.WithJobArgs(ctx, s.jobArgsForTurn(conversation, peer))
	}
	return s.flowAuthorizedPeerContext(ctx, conversation), nil
}

func (s *Service) admittedSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.sessionFlowOwners[id]
	return ok && owner.jobID == s.RemoteJobID()
}

func (s *Service) ResolveSessionFlowConversation(ctx context.Context, peer bridge.PeerRef, jobID string) (string, error) {
	if s == nil || s.store == nil {
		return "", errors.New("bridge unavailable")
	}
	if jobID == "" || (!s.poolMode && jobID != s.RemoteJobID()) {
		return "", errors.New("job does not own this bridge identity")
	}
	binding, err := s.store.GetBinding(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return binding.SessionID, nil
}

func (s *Service) replaySessionFlowEvents(ctx context.Context) {
	if s.app == nil || s.app.FlowRuns == nil {
		return
	}
	after := ""
	for ctx.Err() == nil {
		runs, err := s.app.FlowRuns.RunsWithEventsAfter(ctx, after, 50)
		if err != nil {
			return
		}
		if len(runs) == 0 {
			return
		}
		for _, run := range runs {
			after = run.ID
			bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, run.ConversationID)
			if err != nil || len(bindings) == 0 {
				continue
			}
			var cursor int64
			for ctx.Err() == nil {
				events, err := s.app.FlowRuns.Events(ctx, run.ID, cursor, 100)
				if err != nil || len(events) == 0 {
					break
				}
				for _, saved := range events {
					cursor = saved.Sequence
					var event app.FlowRunEvent
					if json.Unmarshal(saved.Payload, &event) != nil {
						continue
					}
					event.ID = saved.ID
					event.Sequence = saved.Sequence
					s.deliverSessionFlowEvent(ctx, event)
				}
			}
		}
	}
}
func (s *Service) flowAuthorizedPeerContext(ctx context.Context, conversation string) context.Context {
	if s.store == nil {
		return ctx
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, conversation)
	if err != nil {
		return tools.WithFlowAuthorizedPeers(ctx, nil)
	}
	peers := make([]tools.Peer, 0, len(bindings))
	for _, binding := range bindings {
		peers = append(peers, tools.Peer{Channel: binding.Channel, Identity: binding.IdentityID, PeerID: binding.PeerID})
	}
	return tools.WithFlowAuthorizedPeers(ctx, peers)
}

func (s *Service) flowAdmissionTurn(ctx context.Context, id string) (context.Context, func()) {
	s.mu.Lock()
	owner, admitted := s.sessionFlowOwners[id]
	retired := s.sessionFlowRetired[id]
	s.mu.Unlock()
	if !admitted && !retired {
		return ctx, func() {}
	}
	turn, cancel := context.WithCancel(ctx)
	if retired {
		cancel()
		return turn, func() {}
	}
	if owner.ctx == nil {
		return turn, cancel
	}
	stop := context.AfterFunc(owner.ctx, cancel)
	deadlineCancel := func() {}
	if !owner.deadline.IsZero() {
		turn, deadlineCancel = context.WithDeadline(turn, owner.deadline)
		turn = tools.WithSessionFlowDeadline(turn, owner.deadline)
	}
	return turn, func() { stop(); deadlineCancel(); cancel() }
}

func (s *Service) cmdFlowAnswer(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		return replyText(err.Error())
	}
	if strings.TrimSpace(in.CommandArgs) == "" {
		return replyText("Usage: /answer <text>")
	}
	if s.questionRouter == nil || s.app == nil || s.app.FlowRuns == nil {
		return replyText("No pending flow question.")
	}
	s.questionRouter.mu.Lock()
	requestID := s.questionRouter.flowVisible[binding.SessionID]
	s.questionRouter.mu.Unlock()
	if requestID == "" {
		return replyText("No pending flow question.")
	}
	record, err := s.app.FlowRuns.FindQuestionByRequest(ctx, requestID)
	if err != nil {
		return replyText("No pending flow question.")
	}
	text := in.CommandArgs
	handled, _, refusal := s.durableFlowReply(ctx, requestID, questionReplyInput{SessionID: record.SessionID, ConversationID: binding.SessionID, Peer: &in.Peer, JobID: s.RemoteJobID(), Text: &text})
	if !handled || refusal != "" {
		return replyText("That question is no longer waiting for this answer.")
	}
	return replyText("Answer recorded.")
}

// IsSessionFlowAdmitted distinguishes an idempotent local refresh from worker
// takeover. A status/control retry must not acquire an execution lease already
// used by this worker's pending terminal delivery.
func (s *Service) IsSessionFlowAdmitted(sessionID string, peer bridge.PeerRef, jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.sessionFlowOwners[sessionID]
	return ok && !s.sessionFlowRetired[sessionID] && owner.jobID == jobID && owner.peer == peer && owner.ctx != nil && owner.ctx.Err() == nil
}
