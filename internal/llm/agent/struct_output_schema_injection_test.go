package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingMsgService captures the synthetic messages the agent writes.
type recordingMsgService struct {
	message.Service
	created []message.CreateMessageParams
}

func (r *recordingMsgService) Create(_ context.Context, _ string, params message.CreateMessageParams) (message.Message, error) {
	r.created = append(r.created, params)
	return message.Message{ID: "m", Role: params.Role, Parts: params.Parts, Synthetic: params.Synthetic}, nil
}

func schemaFor(field string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{field: map[string]any{"type": "string"}},
		"required":   []any{field},
	}
}

func agentWithSchema(msgs message.Service, schema map[string]any) *agent {
	return &agent{
		messages:                msgs,
		structOutputEnvelope:    tools.RenderSchemaEnvelope(schema),
		structOutputFingerprint: tools.SchemaFingerprint(schema),
	}
}

func userMsg(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func TestInjectStructOutputSchemaWritesSyntheticUserMessage(t *testing.T) {
	msgs := &recordingMsgService{}
	a := agentWithSchema(msgs, schemaFor("summary"))

	got, ok := a.injectStructOutputSchema(context.Background(), "S1", []message.Message{userMsg("do the thing")})

	require.True(t, ok)
	require.Len(t, msgs.created, 1)
	assert.Equal(t, message.User, msgs.created[0].Role)
	assert.True(t, msgs.created[0].Synthetic, "the envelope is machinery, not something the user said")
	assert.Equal(t, message.User, got.Role)

	require.Len(t, msgs.created[0].Parts, 1)
	text, isText := msgs.created[0].Parts[0].(message.TextContent)
	require.True(t, isText)
	assert.Contains(t, text.Text, tools.EnvelopeFingerprintToken(a.structOutputFingerprint))
	assert.Contains(t, text.Text, `"summary"`)
}

// Once per session per schema, not once per turn. Re-injecting every turn would
// trade the cache saving for a token cost paid on every single request.
func TestInjectStructOutputSchemaSkipsWhenFingerprintPresent(t *testing.T) {
	msgs := &recordingMsgService{}
	a := agentWithSchema(msgs, schemaFor("summary"))
	history := []message.Message{
		userMsg("do the thing"),
		userMsg(a.structOutputEnvelope),
	}

	_, ok := a.injectStructOutputSchema(context.Background(), "S1", history)

	assert.False(t, ok)
	assert.Empty(t, msgs.created, "a session already holding this schema must write nothing")
}

// A forked step inherits the PREVIOUS step's envelope. Its fingerprint differs,
// so this step's schema is injected — and the stale one is left where it is,
// with the envelope's own supersession line telling the model which governs.
func TestInjectStructOutputSchemaReinjectsForForkedStepWithNewSchema(t *testing.T) {
	msgs := &recordingMsgService{}
	previous := tools.RenderSchemaEnvelope(schemaFor("plan"))
	a := agentWithSchema(msgs, schemaFor("verdict"))

	history := []message.Message{
		userMsg("step one prompt"),
		userMsg(previous),
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}},
		userMsg("step two prompt"),
	}

	_, ok := a.injectStructOutputSchema(context.Background(), "S2", history)

	require.True(t, ok, "a forked history carrying a different schema must not suppress injection")
	require.Len(t, msgs.created, 1)
}

// The presence check runs against the history actually being sent, which is
// post-summary. A compaction that drops the envelope must therefore re-arm
// injection: a schema-bearing step that compacts must not lose the shape it is
// being graded against.
func TestInjectStructOutputSchemaReinjectsAfterCompactionDropsIt(t *testing.T) {
	msgs := &recordingMsgService{}
	a := agentWithSchema(msgs, schemaFor("summary"))

	// What filterMessagesFromSummary leaves behind: the summary turn onwards,
	// with the original envelope gone.
	postSummary := []message.Message{
		userMsg("summary of the conversation so far"),
		userMsg("keep going"),
	}

	_, ok := a.injectStructOutputSchema(context.Background(), "S1", postSummary)

	assert.True(t, ok)
	assert.Len(t, msgs.created, 1)
}

// An auto-resume turn (background task completion) creates no user message, so
// injection cannot ride along with one. It still has to happen.
func TestInjectStructOutputSchemaInjectsWithoutAUserTurn(t *testing.T) {
	msgs := &recordingMsgService{}
	a := agentWithSchema(msgs, schemaFor("summary"))

	history := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "spawned a task"}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.TextContent{Text: "task done"}}},
	}

	_, ok := a.injectStructOutputSchema(context.Background(), "S1", history)

	assert.True(t, ok)
	assert.Len(t, msgs.created, 1)
}

func TestInjectStructOutputSchemaNoopWithoutSchema(t *testing.T) {
	msgs := &recordingMsgService{}
	a := &agent{messages: msgs}

	_, ok := a.injectStructOutputSchema(context.Background(), "S1", []message.Message{userMsg("hi")})

	assert.False(t, ok)
	assert.Empty(t, msgs.created, "an agent with no output schema must leave the history untouched")
}

// Only user-role messages carry the envelope, so an assistant turn that happens
// to quote the fingerprint (a model echoing its instructions back) must not be
// mistaken for the injection itself.
func TestInjectStructOutputSchemaIgnoresNonUserMessages(t *testing.T) {
	msgs := &recordingMsgService{}
	a := agentWithSchema(msgs, schemaFor("summary"))

	history := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: a.structOutputEnvelope}}},
	}

	_, ok := a.injectStructOutputSchema(context.Background(), "S1", history)

	assert.True(t, ok, "an assistant echo is not the injected envelope")
	assert.Len(t, msgs.created, 1)
}

func TestResolveSchemaDeliveryPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		agent, global string
		want          tools.SchemaDelivery
	}{
		{"both unset", "", "", tools.SchemaDeliveryMessage},
		{"global only", "", "tool", tools.SchemaDeliveryTool},
		{"agent wins over global", "message", "tool", tools.SchemaDeliveryMessage},
		{"agent wins the other way", "tool", "message", tools.SchemaDeliveryTool},
		{"bad agent value fails safe", "inline", "tool", tools.SchemaDeliveryMessage},
		{"bad global value fails safe", "", "inline", tools.SchemaDeliveryMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveSchemaDelivery(tc.agent, tc.global, "coder"))
		})
	}
}
