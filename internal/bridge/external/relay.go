package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// relayFrame is the wire shape POSTed to the orchestrator's
// /router/external/outbound endpoint. Field names and shapes are pinned
// by the openspec change multi-platform-bridge-providers — reproduce
// exactly, do not renegotiate on the Go side alone; the orchestrator's
// ingestion handler must match field-for-field.
type relayFrame struct {
	JobID       string                `json:"jobId"`
	Peer        bridge.PeerRef        `json:"peer"`
	SessionID   string                `json:"sessionId"`
	Kind        string                `json:"kind"`
	Text        string                `json:"text,omitempty"`
	Attachments []relayAttachmentMeta `json:"attachments,omitempty"`
	Question    *relayQuestion        `json:"question,omitempty"`
	Progress    *relayProgress        `json:"progress,omitempty"`
	Delivered   *relayDelivered       `json:"delivered,omitempty"`
	TS          int64                 `json:"ts"`
}

// relayAttachmentMeta is METADATA ONLY — it has no field for file
// content at all (not merely an omitted/empty one). Reusing
// bridge.Attachment (which carries Content []byte) here would risk
// marshalling an empty/null content key; this type structurally cannot.
type relayAttachmentMeta struct {
	FileName string `json:"fileName"`
	MimeType string `json:"mimeType"`
	Size     int    `json:"size"`
}

// relayChoice is one option in a relayed interactive question.
type relayChoice struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Custom bool   `json:"custom"`
}

// relayQuestion is the "question" sub-object of a relayFrame with
// kind == "question".
type relayQuestion struct {
	RequestID string        `json:"requestId"`
	Prompt    string        `json:"prompt"`
	Choices   []relayChoice `json:"choices"`
	Multiple  bool          `json:"multiple"`
	Custom    bool          `json:"custom"`
}

// relayProgress is the "progress" sub-object of a relayFrame with
// kind == "progress": one paced state of the run's progress card (see
// bridge.RunProgress). Every key is always present so a consumer never
// has to tell "zero" from "absent".
type relayProgress struct {
	Status            string `json:"status"`
	ToolsDone         int    `json:"toolsDone"`
	Failed            int    `json:"failed"`
	Running           int    `json:"running"`
	CurrentTool       string `json:"currentTool"`
	LastFailedTool    string `json:"lastFailedTool"`
	LastFailureReason string `json:"lastFailureReason"`
	ElapsedMs         int64  `json:"elapsedMs"`
}

// relayDelivered is the "delivered" sub-object of a relayFrame with
// kind == "delivered": the ids of inbound messages (Inbound.Messages) that
// reached the agent, in delivery order. Never empty.
type relayDelivered struct {
	MessageIDs []string `json:"messageIds"`
}

// relayKind values.
const (
	relayKindMessage   = "message"
	relayKindAck       = "ack"
	relayKindQuestion  = "question"
	relayKindProgress  = "progress"
	relayKindDelivered = "delivered"
)

// postRelay POSTs frame to the identity's relay endpoint with Basic auth
// (mirrors internal/bridge/registrar.go's HTTPRegistrar.applyAuth —
// arbitrary username, the shared credential as password). 202 is the
// only success status; anything else — including a 2xx that isn't 202 —
// is treated as a failed send per the pinned contract.
func (a *Adapter) postRelay(ctx context.Context, frame relayFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("bridge/external: encode relay frame: %w", err)
	}
	target := strings.TrimRight(a.relayBaseURL, "/") + "/router/external/outbound"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("bridge/external: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("opencode-external", a.relayCredential)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("bridge/external: POST %s: %w", target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("bridge/external: relay POST returned status %d (want 202)", resp.StatusCode)
	}
	return nil
}

func nowUnixMilli() int64 {
	return time.Now().UnixMilli()
}
