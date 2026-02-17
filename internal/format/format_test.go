package format

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input    string
		expected OutputFormat
		wantErr  bool
	}{
		{"text", Text, false},
		{"json", JSON, false},
		{"json_schema", JSONSchema, false},
		{"TEXT", Text, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("Parse(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseWithSchema(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantFormat OutputFormat
		wantSchema bool
		wantErr    bool
	}{
		{
			name:       "plain text",
			input:      "text",
			wantFormat: Text,
			wantSchema: false,
		},
		{
			name:       "plain json",
			input:      "json",
			wantFormat: JSON,
			wantSchema: false,
		},
		{
			name:       "json_schema with schema",
			input:      `json_schema={"type":"object","properties":{"name":{"type":"string"}}}`,
			wantFormat: JSONSchema,
			wantSchema: true,
		},
		{
			name:       "json_schema with quoted schema",
			input:      `json_schema='{"type":"object","properties":{"name":{"type":"string"}}}'`,
			wantFormat: JSONSchema,
			wantSchema: true,
		},
		{
			name:    "json_schema with invalid JSON",
			input:   `json_schema=not json`,
			wantErr: true,
		},
		{
			name:    "json_schema missing type",
			input:   `json_schema={"properties":{}}`,
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "xml",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, schema, err := ParseWithSchema(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseWithSchema(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if format != tt.wantFormat {
				t.Errorf("format = %q, want %q", format, tt.wantFormat)
			}
			if tt.wantSchema && schema == nil {
				t.Error("expected schema to be non-nil")
			}
			if !tt.wantSchema && schema != nil {
				t.Error("expected schema to be nil")
			}
		})
	}
}

func TestValidateJSONSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  map[string]any
		wantErr bool
	}{
		{
			name:    "nil schema",
			schema:  nil,
			wantErr: true,
		},
		{
			name:    "missing type",
			schema:  map[string]any{"properties": map[string]any{}},
			wantErr: true,
		},
		{
			name:    "non-string type",
			schema:  map[string]any{"type": 42},
			wantErr: true,
		},
		{
			name:   "valid schema",
			schema: map[string]any{"type": "object"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJSONSchema(tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateJSONSchema() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFormatOutput(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		outputFormat OutputFormat
		want         string
	}{
		{
			name:         "text format returns content as-is",
			content:      "hello",
			outputFormat: Text,
			want:         "hello",
		},
		{
			name:         "json_schema returns content as-is",
			content:      `{"summary":"test"}`,
			outputFormat: JSONSchema,
			want:         `{"summary":"test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatOutput(tt.content, tt.outputFormat)
			if got != tt.want {
				t.Errorf("FormatOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"text", true},
		{"json", true},
		{"json_schema", true},
		{`json_schema={"type":"object"}`, true},
		{"xml", false},
		{`json_schema=invalid`, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsValid(tt.input); got != tt.valid {
				t.Errorf("IsValid(%q) = %v, want %v", tt.input, got, tt.valid)
			}
		})
	}
}
