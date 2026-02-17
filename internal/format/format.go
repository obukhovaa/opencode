package format

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OutputFormat represents the output format type for non-interactive mode
type OutputFormat string

const (
	// Text format outputs the AI response as plain text.
	Text OutputFormat = "text"

	// JSON format outputs the AI response wrapped in a JSON object.
	JSON OutputFormat = "json"

	// JSONSchema format outputs the AI response validated against a JSON schema.
	JSONSchema OutputFormat = "json_schema"
)

// String returns the string representation of the OutputFormat
func (f OutputFormat) String() string {
	return string(f)
}

// SupportedFormats is a list of all supported output formats as strings
var SupportedFormats = []string{
	string(Text),
	string(JSON),
	string(JSONSchema),
}

// Parse converts a string to an OutputFormat
func Parse(s string) (OutputFormat, error) {
	s = strings.ToLower(strings.TrimSpace(s))

	switch s {
	case string(Text):
		return Text, nil
	case string(JSON):
		return JSON, nil
	case string(JSONSchema):
		return JSONSchema, nil
	default:
		return "", fmt.Errorf("invalid format: %s", s)
	}
}

// ParseWithSchema parses an output format string that may contain an embedded
// JSON schema. For json_schema format, the input should be: json_schema='{...}'.
// Returns the format, optional schema, and any error.
func ParseWithSchema(s string) (OutputFormat, map[string]any, error) {
	s = strings.TrimSpace(s)

	if strings.HasPrefix(strings.ToLower(s), "json_schema=") {
		schemaStr := s[len("json_schema="):]
		schemaStr = strings.TrimSpace(schemaStr)
		// Strip surrounding quotes if present
		if len(schemaStr) >= 2 && (schemaStr[0] == '\'' || schemaStr[0] == '"') {
			schemaStr = schemaStr[1 : len(schemaStr)-1]
		}

		var schema map[string]any
		if err := json.Unmarshal([]byte(schemaStr), &schema); err != nil {
			return "", nil, fmt.Errorf("invalid JSON schema: %w", err)
		}
		if err := ValidateJSONSchema(schema); err != nil {
			return "", nil, err
		}
		return JSONSchema, schema, nil
	}

	format, err := Parse(s)
	if err != nil {
		return "", nil, err
	}
	return format, nil, nil
}

// ValidateJSONSchema performs basic validation of a JSON schema.
func ValidateJSONSchema(schema map[string]any) error {
	if schema == nil {
		return fmt.Errorf("schema cannot be nil")
	}
	t, ok := schema["type"]
	if !ok {
		return fmt.Errorf("schema must have a \"type\" field")
	}
	if _, ok := t.(string); !ok {
		return fmt.Errorf("schema \"type\" must be a string")
	}
	return nil
}

// IsValid checks if the provided format string is supported
func IsValid(s string) bool {
	_, _, err := ParseWithSchema(s)
	return err == nil
}

// GetHelpText returns a formatted string describing all supported formats
func GetHelpText() string {
	return fmt.Sprintf(`Supported output formats:
- %s: Plain text output (default)
- %s: Output wrapped in a JSON object
- %s: Output validated against a JSON schema (json_schema='{"type":"object",...}')`,
		Text, JSON, JSONSchema)
}

// FormatOutput formats the AI response according to the specified format
func FormatOutput(content string, format OutputFormat) string {
	switch format {
	case JSON:
		return formatAsJSON(content)
	case JSONSchema:
		return content
	case Text:
		fallthrough
	default:
		return content
	}
}

// formatAsJSON wraps the content in a simple JSON object
func formatAsJSON(content string) string {
	// Use the JSON package to properly escape the content
	response := struct {
		Response string `json:"response"`
	}{
		Response: content,
	}

	jsonBytes, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		// In case of an error, return a manually formatted JSON
		jsonEscaped := strings.ReplaceAll(content, "\\", "\\\\")
		jsonEscaped = strings.ReplaceAll(jsonEscaped, "\"", "\\\"")
		jsonEscaped = strings.ReplaceAll(jsonEscaped, "\n", "\\n")
		jsonEscaped = strings.ReplaceAll(jsonEscaped, "\r", "\\r")
		jsonEscaped = strings.ReplaceAll(jsonEscaped, "\t", "\\t")

		return fmt.Sprintf("{\n  \"response\": \"%s\"\n}", jsonEscaped)
	}

	return string(jsonBytes)
}
