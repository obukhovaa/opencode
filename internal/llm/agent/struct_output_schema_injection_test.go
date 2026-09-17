package agent

import (
	"context"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
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

// withStructOutputSchema is what every rebuild site routes through: it appends
// when the envelope is missing and is free when it is not. The mid-run
// auto-compaction rebuild depends on both halves — it drops the envelope with
// filterMessagesFromSummary and must get it back, without duplicating it on the
// compactions that did not.
func TestWithStructOutputSchemaAppendsOnlyWhenMissing(t *testing.T) {
	msgs := &recordingMsgService{}
	a := agentWithSchema(msgs, schemaFor("summary"))

	// What a post-compaction rebuild looks like: summary onwards, envelope gone.
	postCompaction := []message.Message{userMsg("summary of the conversation so far")}
	rebuilt := a.withStructOutputSchema(context.Background(), "S1", postCompaction)

	require.Len(t, rebuilt, 2, "the envelope must come back after compaction drops it")
	assert.Len(t, msgs.created, 1)

	// Idempotent: a second pass over a history that already has it writes nothing.
	again := a.withStructOutputSchema(context.Background(), "S1", rebuilt)
	assert.Len(t, again, 2)
	assert.Len(t, msgs.created, 1, "re-running over an intact history must not duplicate the envelope")
}

func TestWithStructOutputSchemaIsANoopWithoutSchema(t *testing.T) {
	msgs := &recordingMsgService{}
	a := &agent{messages: msgs}

	in := []message.Message{userMsg("hi")}
	assert.Equal(t, in, a.withStructOutputSchema(context.Background(), "S1", in))
	assert.Empty(t, msgs.created)
}

// A flow step's `model:` override replaces the agent's configured model on the
// local provider config, so the delivery gate has to be evaluated against the
// model the agent ACTUALLY runs. Reading only the config would let a step that
// moves a Claude-configured agent onto Gemini keep message delivery — and
// Gemini rejects the invariant `output` parameter, because its function
// declarations cannot express an object with no declared properties.
func TestResolveSchemaDeliveryFollowsTheModelOverride(t *testing.T) {
	var gemini, anthropic models.ModelID
	for id, m := range models.SupportedModels {
		if gemini == "" && m.Provider == models.ProviderGemini {
			gemini = id
		}
		if anthropic == "" && m.Provider == models.ProviderAnthropic {
			anthropic = id
		}
	}
	require.NotEmpty(t, gemini, "catalogue must have a Gemini model for this test to mean anything")
	require.NotEmpty(t, anthropic)

	dir := t.TempDir()
	config.Reset()
	_, err := config.Load(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})

	info := &agentregistry.AgentInfo{ID: "prober"}

	config.Get().Agents["prober"] = config.Agent{Model: anthropic}
	agentregistry.InvalidateRegistry()
	assert.Equal(t, tools.SchemaDeliveryMessage, ResolveSchemaDelivery(info, ""),
		"an Anthropic agent on its configured model is the case this feature exists for")
	assert.Equal(t, tools.SchemaDeliveryTool, ResolveSchemaDelivery(info, gemini),
		"a step overriding onto Gemini must fall back, even though the config still says Anthropic")

	config.Get().Agents["prober"] = config.Agent{Model: gemini}
	agentregistry.InvalidateRegistry()
	assert.Equal(t, tools.SchemaDeliveryTool, ResolveSchemaDelivery(info, ""))
	assert.Equal(t, tools.SchemaDeliveryMessage, ResolveSchemaDelivery(info, anthropic),
		"and a step overriding a Gemini agent onto Anthropic should get the caching back")
}
