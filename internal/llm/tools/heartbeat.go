package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/heartbeat"
)

// HeartbeatToolName is the agent tool that reads and changes the heartbeat
// of the chat the current session is bound to (openspec capability
// bridge-heartbeat). It is how `/heartbeat <natural language>` is carried
// out: the bridge hands the request to the agent, and the agent maps it to
// typed settings here.
const HeartbeatToolName = "heartbeat"

// ErrHeartbeatUnavailable is returned when no chat bridge schedules
// heartbeats in this process (anything but daemon mode).
var ErrHeartbeatUnavailable = errors.New("heartbeats are only available when opencode runs as a daemon with a chat bridge")

// HeartbeatConfigurer is implemented by the chat bridge. Both methods act
// on every chat binding of the given session and return the resulting
// status text.
type HeartbeatConfigurer interface {
	HeartbeatStatus(ctx context.Context, sessionID string) (string, error)
	ApplyHeartbeat(ctx context.Context, sessionID string, cmd heartbeat.Command) (string, error)
}

// HeartbeatParams are the tool's typed settings. Every field is optional;
// an empty call (or action "get") only reads the status.
type HeartbeatParams struct {
	Action string `json:"action,omitempty"`
	State  string `json:"state,omitempty"`
	Every  string `json:"every,omitempty"`
	Hours  string `json:"hours,omitempty"`
	Days   string `json:"days,omitempty"`
	Model  string `json:"model,omitempty"`
	File   string `json:"file,omitempty"`
	Now    bool   `json:"now,omitempty"`
}

// command renders the params in /heartbeat's exact grammar so validation
// lives in one place (heartbeat.ParseCommand).
func (p HeartbeatParams) command() (heartbeat.Command, error) {
	var words []string
	add := func(key, v string) {
		if v = strings.TrimSpace(v); v != "" {
			words = append(words, key, v)
		}
	}
	if s := strings.ToLower(strings.TrimSpace(p.State)); s != "" {
		if s != "on" && s != "off" {
			return heartbeat.Command{}, fmt.Errorf("state %q: want on or off", p.State)
		}
		words = append(words, s)
	}
	add("every", p.Every)
	add("hours", p.Hours)
	add("days", p.Days)
	add("model", p.Model)
	add("file", p.File)
	if p.Now {
		words = append(words, "now")
	}
	for _, w := range words {
		if strings.ContainsAny(w, " \t\n") {
			return heartbeat.Command{}, fmt.Errorf("%q: values may not contain spaces", w)
		}
	}
	return heartbeat.ParseCommand(strings.Join(words, " "))
}

type heartbeatTool struct {
	// configurer is resolved at call time: the bridge is wired after the
	// primary agents' tool sets are built, so a handle captured at
	// construction would always be nil for them.
	configurer func() HeartbeatConfigurer
}

// NewHeartbeatTool builds the tool around a late-bound configurer.
func NewHeartbeatTool(configurer func() HeartbeatConfigurer) BaseTool {
	return &heartbeatTool{configurer: configurer}
}

func (t *heartbeatTool) Info() ToolInfo {
	return ToolInfo{
		Name: HeartbeatToolName,
		Description: `Read or change the heartbeat of the chat you are talking in. A heartbeat wakes this session on a schedule; each beat you follow your agenda file and report only what is new (reply HEARTBEAT_OK when nothing needs your human).

Use it when your human asks to turn the heartbeat on or off, change how often or when it runs, or which model or agenda file it uses. Map what they say to these settings:
- state: "on" or "off".
- every: interval between beats, 10m to 24h (e.g. "30m", "1h", "2h30m"). Slots start at the beginning of the active hours.
- hours: active hours in UTC as "HH-HH" or "HH:MM-HH:MM", start inclusive, end exclusive, wrapping midnight when the end is earlier (e.g. "22-06"); "all" for the whole day. Convert times your human gives in another time zone to UTC, and say which UTC hours you set. Ask if you cannot tell the time zone.
- days: "weekdays" or "all".
- model: a model ID for beats, or "default" for your own. Warn that another model cannot reuse this session's prompt cache.
- file: agenda path relative to the working directory, or "default" (HEARTBEAT.md).
- now: true to queue one beat. It runs as soon as this turn ends.

Omit what your human did not ask to change. A daily beat at a fixed time is every "24h" with hours starting at that time (e.g. hours "07-08"). What the heartbeat should check is not a setting: write it into the agenda file with your file tools. A beat with an empty agenda is skipped.

Returns the resulting status.`,
		Parameters: map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"get", "set"},
				"description": `"get" reads the status; "set" (default when any setting is given) applies the settings`,
			},
			"state": map[string]any{"type": "string", "enum": []string{"on", "off"}, "description": "Turn the heartbeat on or off"},
			"every": map[string]any{"type": "string", "description": "Interval between beats, 10m to 24h, e.g. 30m, 1h"},
			"hours": map[string]any{"type": "string", "description": `Active hours in UTC, "HH-HH" or "HH:MM-HH:MM", or "all"`},
			"days":  map[string]any{"type": "string", "enum": []string{"weekdays", "all"}, "description": "Weekdays only, or every day"},
			"model": map[string]any{"type": "string", "description": `Model ID for beats, or "default"`},
			"file":  map[string]any{"type": "string", "description": `Agenda file relative to the working directory, or "default"`},
			"now":   map[string]any{"type": "boolean", "description": "Queue one beat; it runs when this turn ends"},
		},
	}
}

func (t *heartbeatTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params HeartbeatParams
	if strings.TrimSpace(call.Input) != "" {
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
		}
	}
	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewTextErrorResponse("session context required"), nil
	}
	var c HeartbeatConfigurer
	if t.configurer != nil {
		c = t.configurer()
	}
	if c == nil {
		return NewTextErrorResponse(ErrHeartbeatUnavailable.Error()), nil
	}

	cmd, err := params.command()
	if err != nil {
		return NewTextErrorResponse(err.Error() + "\nNothing was changed."), nil
	}
	if params.Action == "get" || (!cmd.ChangesSettings() && !cmd.Now) {
		status, err := c.HeartbeatStatus(ctx, sessionID)
		if err != nil {
			return NewTextErrorResponse(err.Error()), nil
		}
		return NewTextResponse(status), nil
	}
	status, err := c.ApplyHeartbeat(ctx, sessionID, cmd)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	return NewTextResponse(status), nil
}

func (t *heartbeatTool) AllowParallelism(ToolCall, []ToolCall) bool { return false }

func (t *heartbeatTool) IsBaseline() bool { return true }
