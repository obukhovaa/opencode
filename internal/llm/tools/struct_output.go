package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
)

// SchemaDelivery selects where a flow step's output JSON Schema is placed in
// the request the provider builds.
//
// The choice is a prompt-caching decision, not a behavioral one. Anthropic
// renders the cacheable prefix as tools → system → messages and invalidates
// everything after the first changed byte, so a `struct_output` tool whose
// input_schema is derived from the step's schema puts a per-step payload at
// position 0 of the prefix: two consecutive steps of the same agent then share
// no cache at all — not the tool list, not the system prompt, not even the
// message history a forked session just copied verbatim.
type SchemaDelivery string

const (
	// SchemaDeliveryMessage keeps the tool definition invariant and ships the
	// schema in a message at the tail, past the last cache breakpoint. Default.
	SchemaDeliveryMessage SchemaDelivery = "message"
	// SchemaDeliveryTool splays the schema into the tool's parameters — the
	// behavior before this was configurable. Kept as an escape hatch so a
	// deployment can revert without a rebuild.
	SchemaDeliveryTool SchemaDelivery = "tool"
)

// ParseSchemaDelivery maps a config string onto a delivery mode, failing safe
// to the default for anything unrecognized. ok=false signals the caller to warn.
//
// Deliberately case-SENSITIVE. Accepting "Message" would make the runtime take
// a value the published JSON Schema's enum rejects, which is the exact
// divergence cmd/schema's enum test exists to prevent: the config works, and
// every IDE reports it as an error. Surrounding whitespace is still forgiven —
// that is an invisible typo, not an alias.
func ParseSchemaDelivery(s string) (mode SchemaDelivery, ok bool) {
	switch SchemaDelivery(strings.TrimSpace(s)) {
	case "":
		return SchemaDeliveryMessage, true
	case SchemaDeliveryMessage:
		return SchemaDeliveryMessage, true
	case SchemaDeliveryTool:
		return SchemaDeliveryTool, true
	default:
		return SchemaDeliveryMessage, false
	}
}

const (
	StructOutputToolName = "struct_output"

	// structOutputDescription is the schema-delivery description: it must stay
	// byte-identical across every agent, step and schema, because it sits in
	// the cached prefix. Everything step-specific lives in the
	// <struct_output_schema> envelope instead (see RenderSchemaEnvelope).
	structOutputDescription = `Emit your final answer as structured JSON, as the value of the "output" argument.

The required shape is the JSON Schema in the <struct_output_schema> block of this conversation; read it before calling. Call this tool exactly once, as your final action, populating every required field; the JSON is validated against that schema and returned as the agent's output.`

	// structOutputToolSchemaDescription is the description of the single
	// `output` parameter. Same constraint: invariant across steps.
	structOutputToolSchemaDescription = "The complete JSON document conforming to the schema in the <struct_output_schema> block of this conversation."

	// structOutputLegacyDescription is the pre-change description, used when
	// the schema rides in the tool parameters and needs no envelope pointer.
	structOutputLegacyDescription = `Emit your final answer as structured JSON conforming to the schema defined in this tool's parameters.

Call it exactly once, as your final action, populating every required field; the JSON is validated and returned as the agent's output.`

	// structOutputWrapperKey is the single parameter name in message-delivery
	// mode.
	structOutputWrapperKey = "output"

	// SchemaEnvelopeOpenTag prefixes the envelope's opening tag. Exported so
	// the agent's presence check can scan history for it without duplicating
	// the literal.
	SchemaEnvelopeOpenTag = "<struct_output_schema"
)

type structOutputTool struct {
	schema       map[string]any
	structParams map[string]any
	required     []string
	delivery     SchemaDelivery
	description  string
}

// NewStructOutputTool builds the tool with the schema in its parameters.
// Retained for callers that want the legacy surface explicitly; the agent
// layer goes through NewStructOutputToolWithDelivery.
func NewStructOutputTool(schema map[string]any) BaseTool {
	return NewStructOutputToolWithDelivery(schema, SchemaDeliveryTool)
}

// NewStructOutputToolWithDelivery builds the tool for a given delivery mode.
// The full schema is retained in both modes — validation in Run never depends
// on how the model was shown the schema.
func NewStructOutputToolWithDelivery(schema map[string]any, delivery SchemaDelivery) BaseTool {
	if delivery == SchemaDeliveryMessage {
		return &structOutputTool{
			schema:   schema,
			delivery: SchemaDeliveryMessage,
			structParams: map[string]any{
				structOutputWrapperKey: map[string]any{
					"type":        "object",
					"description": structOutputToolSchemaDescription,
				},
			},
			required:    []string{structOutputWrapperKey},
			description: structOutputDescription,
		}
	}
	params, required := buildParamsFromSchema(schema)
	return &structOutputTool{
		schema:       schema,
		delivery:     SchemaDeliveryTool,
		structParams: params,
		required:     required,
		description:  structOutputLegacyDescription,
	}
}

func (s *structOutputTool) Info() ToolInfo {
	return ToolInfo{
		Name:        StructOutputToolName,
		Description: s.description,
		Parameters:  s.structParams,
		Required:    s.required,
	}
}

func (s *structOutputTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(call.Input), &payload); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Invalid JSON: %s", err.Error())), nil
	}

	result, unwrapErr := s.unwrap(payload)
	if unwrapErr != "" {
		return NewTextErrorResponse(unwrapErr), nil
	}

	// Make the emitted output schema-conformant BEFORE it is stored and threaded
	// into downstream flow routing args. Models routinely omit empty-valued
	// fields that carry a `default` — an empty array like `blockers: []` is the
	// most common. Left absent, any flow routing rule that keys off
	// `${args.<field>}` sees a MISSING key: every predicate evaluates false, no
	// rule matches, and the flow silently strands (observed on
	// developer-react-on-jira's plan-to-implement → implement transition).
	// Materializing declared defaults keeps that routing contract whole. A
	// required field that has no default and is still missing is a genuinely
	// incomplete answer — reject it so the model retries (the agent loop
	// re-enters on an error struct_output result) instead of persisting a
	// half-filled output.
	//
	// This runs against s.schema — the FULL schema — in both delivery modes. How
	// the model was shown the schema never changes what is enforced here.
	if _, ok := objectProperties(s.schema); ok {
		applyDefaults(s.schema, result)
		if missing := missingRequiredFields(s.schemaRequired(), result); len(missing) > 0 {
			return NewTextErrorResponse(fmt.Sprintf(
				"struct_output is missing required field(s): %s. Return the complete JSON with every required field populated (an empty array/object/string is a valid value when a field has no content) and call struct_output again.",
				strings.Join(missing, ", "),
			)), nil
		}
	}

	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to format output: %s", err.Error())), nil
	}
	return NewTextResponse(string(output)), nil
}

// unwrap extracts the emitted document from the raw tool input. In
// message-delivery mode the declared shape is {"output": {...}}, but the model
// is shown the document's schema rather than the wrapper's, so it sometimes
// emits the document flat instead. Accepting both is deliberate: the wrapper is
// a transport detail we introduced for cache stability, and failing a
// well-formed answer over it would be the change paying for itself with the
// very regression it exists to avoid. Only a payload that is neither shape is
// rejected — with an error result, which re-enters the agent loop for a retry
// rather than ending the step.
//
// Tool-delivery mode passes the payload through: the declared parameters ARE
// the document's properties there, so there is no wrapper to remove.
func (s *structOutputTool) unwrap(payload map[string]any) (map[string]any, string) {
	if s.delivery != SchemaDeliveryMessage {
		return payload, ""
	}
	if raw, present := payload[structOutputWrapperKey]; present {
		if doc, ok := raw.(map[string]any); ok {
			return doc, ""
		}
		// A non-object `output` alongside nothing else is unusable; alongside
		// other keys it is more likely a real property of a flat document that
		// happens to be named "output", so fall through to the flat reading.
		if len(payload) == 1 {
			return nil, fmt.Sprintf(
				"struct_output expects %q to be a JSON object matching the schema in the <struct_output_schema> block, but received %T. Call struct_output again with the complete document as the value of %q.",
				structOutputWrapperKey, raw, structOutputWrapperKey,
			)
		}
	}
	// No wrapper: treat the payload as the document itself when it looks like
	// one — it declares at least one property the schema knows about, or the
	// schema declares no properties for us to check against.
	if s.looksLikeDocument(payload) {
		return payload, ""
	}
	return nil, fmt.Sprintf(
		"struct_output received a payload matching neither the expected shape nor the schema. Put the complete JSON document — conforming to the schema in the <struct_output_schema> block — in the %q argument and call struct_output again.",
		structOutputWrapperKey,
	)
}

// looksLikeDocument reports whether a wrapper-less payload can be read as the
// document. An empty payload never qualifies: it carries no answer, and letting
// it through would trade a retryable error for a silently empty step output.
func (s *structOutputTool) looksLikeDocument(payload map[string]any) bool {
	if len(payload) == 0 {
		return false
	}
	props, ok := objectProperties(s.schema)
	if !ok || len(props) == 0 {
		return true
	}
	for key := range payload {
		if _, known := props[key]; known {
			return true
		}
	}
	return false
}

// schemaRequired returns the schema's own required list. In tool-delivery mode
// s.required already is that list; in message-delivery mode s.required names the
// wrapper, so the real list has to come back off the schema.
func (s *structOutputTool) schemaRequired() []string {
	if s.delivery != SchemaDeliveryMessage {
		return s.required
	}
	return requiredFromSchema(s.schema)
}

func (s *structOutputTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return false
}

func (s *structOutputTool) IsBaseline() bool { return true }

// buildParamsFromSchema converts a JSON schema into the ToolInfo.Parameters format.
// If the schema is an object type with properties, those properties are used directly.
// Otherwise, the entire schema is wrapped as a single "output" parameter.
func buildParamsFromSchema(schema map[string]any) (map[string]any, []string) {
	schemaType, _ := schema["type"].(string)
	if schemaType == "object" {
		if props, ok := schema["properties"].(map[string]any); ok {
			params := make(map[string]any, len(props))
			maps.Copy(params, props)
			return params, requiredFromSchema(schema)
		}
	}

	// Fallback: wrap entire schema as a single "output" parameter
	return map[string]any{
		"output": schema,
	}, []string{"output"}
}

// objectProperties returns the property schemas of an object-type schema node.
// It keys off the presence of a `properties` map rather than the `type` field,
// so it also handles union types such as ["object","null"]. When the node is
// not an object-with-properties (e.g. the non-object fallback schema wrapped as
// a single "output" parameter) it returns ok=false and callers skip
// default/required processing, preserving the prior pass-through behavior.
func objectProperties(schema map[string]any) (props map[string]any, ok bool) {
	props, ok = schema["properties"].(map[string]any)
	return props, ok
}

// applyDefaults recursively materializes JSON-schema `default` values for
// properties omitted from data. An absent property that declares a default is
// filled with a deep copy of that default; a property that is present and is
// itself an object is descended into so nested defaults are filled too. Absent
// properties without a default are left absent — required-ness is enforced
// separately, after defaults have had a chance to satisfy it.
func applyDefaults(schema map[string]any, data map[string]any) {
	props, ok := objectProperties(schema)
	if !ok {
		return
	}
	for key, raw := range props {
		propSchema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, present := data[key]; !present {
			if def, hasDefault := propSchema["default"]; hasDefault {
				data[key] = cloneJSONValue(def)
			}
			continue
		}
		if child, ok := data[key].(map[string]any); ok {
			applyDefaults(propSchema, child)
		}
	}
}

// missingRequiredFields returns the required keys absent from data, sorted for a
// stable error message. Defaults are expected to have been applied already, so
// a key only surfaces here when it has neither a value nor a declared default.
func missingRequiredFields(required []string, data map[string]any) []string {
	var missing []string
	for _, key := range required {
		if _, ok := data[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// cloneJSONValue deep-copies a JSON-decoded value (map/slice/scalar) so a
// materialized default is never an alias into the shared, reused schema map.
func cloneJSONValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		cp := make(map[string]any, len(val))
		for k, vv := range val {
			cp[k] = cloneJSONValue(vv)
		}
		return cp
	case []any:
		cp := make([]any, len(val))
		for i, vv := range val {
			cp[i] = cloneJSONValue(vv)
		}
		return cp
	default:
		return val
	}
}

// requiredFromSchema reads an object schema's `required` list. Returns nil for
// a schema that declares none, which missingRequiredFields reads as "nothing to
// enforce".
func requiredFromSchema(schema map[string]any) []string {
	req, ok := schema["required"].([]any)
	if !ok {
		return nil
	}
	required := make([]string, 0, len(req))
	for _, r := range req {
		if s, ok := r.(string); ok {
			required = append(required, s)
		}
	}
	if len(required) == 0 {
		return nil
	}
	return required
}

// SchemaFingerprint is a short, stable digest of an output schema. It labels the
// <struct_output_schema> envelope so a run can tell "this session was already
// shown THIS schema" from "it was shown a different one" — the distinction that
// makes a forked step re-inject while a second turn of the same step does not.
//
// encoding/json sorts map keys, so the marshalled form is canonical for a given
// schema without a separate canonicalization pass.
func SchemaFingerprint(schema map[string]any) string {
	raw, err := json.Marshal(schema)
	if err != nil {
		// Unmarshalable schemas cannot reach here through any config path
		// (they arrive as decoded JSON/YAML), but a fingerprint that collides
		// with nothing is safer than one that collides with everything: an
		// empty string disables dedup and re-injects, which costs tokens
		// rather than correctness.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// RenderSchemaEnvelope builds the message-tail block carrying a step's output
// schema. It goes in a synthetic user message appended AFTER the step's prompt,
// which is to say after the last cache breakpoint of the prefix — the whole
// point of the exercise. It must never be written into the system prompt or a
// tool description, both of which sit in the cached prefix.
//
// The supersession sentence is load-bearing: a forked step's history still
// carries the PREVIOUS step's envelope, and the model has to know which one
// governs.
func RenderSchemaEnvelope(schema map[string]any) string {
	pretty, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	fmt.Fprintf(&sb, "%s fingerprint=%q>\n", SchemaEnvelopeOpenTag, SchemaFingerprint(schema))
	sb.Write(pretty)
	sb.WriteString("\n</struct_output_schema>\n\n")
	sb.WriteString("The JSON Schema above defines the exact shape your final ")
	sb.WriteString(StructOutputToolName)
	sb.WriteString(" call must produce. Pass that document as the ")
	fmt.Fprintf(&sb, "%q argument. ", structOutputWrapperKey)
	sb.WriteString("It supersedes any schema given earlier in this conversation. ")
	sb.WriteString("Populate every required field; a field with no content still needs its empty value.\n")
	sb.WriteString("</system-reminder>")
	return sb.String()
}

// EnvelopeFingerprintToken is the substring a history scan looks for to decide
// whether a session has already been shown a given schema.
func EnvelopeFingerprintToken(fingerprint string) string {
	return fmt.Sprintf("%s fingerprint=%q>", SchemaEnvelopeOpenTag, fingerprint)
}
