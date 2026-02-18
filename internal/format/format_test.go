package format

import (
	"os"
	"path/filepath"
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
			name:    "json_schema with invalid JSON and non-existent file",
			input:   `json_schema=not_a_file_or_json`,
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

func TestParseWithSchema_FilePath(t *testing.T) {
	dir := t.TempDir()
	schemaFile := filepath.Join(dir, "schema.json")
	os.WriteFile(schemaFile, []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`), 0o644)

	format, schema, err := ParseWithSchema("json_schema=" + schemaFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != JSONSchema {
		t.Errorf("format = %q, want %q", format, JSONSchema)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want 'object'", schema["type"])
	}
}

func TestParseWithSchema_Ref(t *testing.T) {
	dir := t.TempDir()
	schemaFile := filepath.Join(dir, "real-schema.json")
	os.WriteFile(schemaFile, []byte(`{"type":"object","properties":{"score":{"type":"number"}},"required":["score"]}`), 0o644)

	input := `json_schema={"$ref":"` + schemaFile + `"}`
	format, schema, err := ParseWithSchema(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != JSONSchema {
		t.Errorf("format = %q, want %q", format, JSONSchema)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want 'object'", schema["type"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	if _, ok := props["score"]; !ok {
		t.Error("expected 'score' property from referenced file")
	}
}

func TestParseWithSchema_RefInvalidPath(t *testing.T) {
	input := `json_schema={"$ref":"/nonexistent/path.json"}`
	_, _, err := ParseWithSchema(input)
	if err == nil {
		t.Error("expected error for non-existent $ref path")
	}
}

func TestParseWithSchema_RefNonString(t *testing.T) {
	input := `json_schema={"$ref":42}`
	_, _, err := ParseWithSchema(input)
	if err == nil {
		t.Error("expected error for non-string $ref")
	}
}

func TestParseWithSchema_FilePathInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	schemaFile := filepath.Join(dir, "bad.json")
	os.WriteFile(schemaFile, []byte(`not json`), 0o644)

	_, _, err := ParseWithSchema("json_schema=" + schemaFile)
	if err == nil {
		t.Error("expected error for file with invalid JSON")
	}
}

func TestParseWithSchema_FilePathNonExistent(t *testing.T) {
	_, _, err := ParseWithSchema("json_schema=/tmp/nonexistent-schema-12345.json")
	if err == nil {
		t.Error("expected error for non-existent file path")
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
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsValid(tt.input); got != tt.valid {
				t.Errorf("IsValid(%q) = %v, want %v", tt.input, got, tt.valid)
			}
		})
	}
}

func TestResolveSchemaString(t *testing.T) {
	dir := t.TempDir()

	// Create a schema file for $ref and file path tests
	schemaFile := filepath.Join(dir, "schema.json")
	os.WriteFile(schemaFile, []byte(`{"type":"object","properties":{"x":{"type":"number"}}}`), 0o644)

	t.Run("inline JSON", func(t *testing.T) {
		schema, err := resolveSchemaString(`{"type":"string"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if schema["type"] != "string" {
			t.Errorf("type = %v, want 'string'", schema["type"])
		}
	})

	t.Run("file path", func(t *testing.T) {
		schema, err := resolveSchemaString(schemaFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if schema["type"] != "object" {
			t.Errorf("type = %v, want 'object'", schema["type"])
		}
	})

	t.Run("$ref to file", func(t *testing.T) {
		schema, err := resolveSchemaString(`{"$ref":"` + schemaFile + `"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if schema["type"] != "object" {
			t.Errorf("type = %v, want 'object'", schema["type"])
		}
	})

	t.Run("$ref ignores other fields", func(t *testing.T) {
		schema, err := resolveSchemaString(`{"$ref":"` + schemaFile + `","description":"ignored"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should have properties from the referenced file, not "description"
		if _, ok := schema["description"]; ok {
			t.Error("expected 'description' from inline to be ignored when $ref is present")
		}
	})

	t.Run("$ref empty string", func(t *testing.T) {
		_, err := resolveSchemaString(`{"$ref":""}`)
		if err == nil {
			t.Error("expected error for empty $ref")
		}
	})
}

func TestResolveSchemaRef(t *testing.T) {
	dir := t.TempDir()

	schemaFile := filepath.Join(dir, "real-schema.json")
	os.WriteFile(schemaFile, []byte(`{"type":"object","properties":{"score":{"type":"number"}}}`), 0o644)

	t.Run("no $ref returns schema unchanged", func(t *testing.T) {
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		result, err := ResolveSchemaRef(schema, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["type"] != "object" {
			t.Errorf("type = %v, want 'object'", result["type"])
		}
	})

	t.Run("nil schema returns nil", func(t *testing.T) {
		result, err := ResolveSchemaRef(nil, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Error("expected nil result for nil schema")
		}
	})

	t.Run("absolute $ref", func(t *testing.T) {
		schema := map[string]any{"$ref": schemaFile}
		result, err := ResolveSchemaRef(schema, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["type"] != "object" {
			t.Errorf("type = %v, want 'object'", result["type"])
		}
	})

	t.Run("relative $ref resolved against baseDir", func(t *testing.T) {
		schema := map[string]any{"$ref": "real-schema.json"}
		result, err := ResolveSchemaRef(schema, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["type"] != "object" {
			t.Errorf("type = %v, want 'object'", result["type"])
		}
		props, ok := result["properties"].(map[string]any)
		if !ok {
			t.Fatal("expected properties map")
		}
		if _, ok := props["score"]; !ok {
			t.Error("expected 'score' property from file")
		}
	})

	t.Run("relative $ref with ../ resolved against baseDir", func(t *testing.T) {
		subDir := filepath.Join(dir, "sub")
		os.MkdirAll(subDir, 0o755)

		schema := map[string]any{"$ref": "../real-schema.json"}
		result, err := ResolveSchemaRef(schema, subDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result["type"] != "object" {
			t.Errorf("type = %v, want 'object'", result["type"])
		}
	})

	t.Run("relative $ref without baseDir uses cwd", func(t *testing.T) {
		schema := map[string]any{"$ref": "/nonexistent/path.json"}
		_, err := ResolveSchemaRef(schema, "")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("$ref non-string value", func(t *testing.T) {
		schema := map[string]any{"$ref": 42}
		_, err := ResolveSchemaRef(schema, "")
		if err == nil {
			t.Error("expected error for non-string $ref")
		}
	})

	t.Run("$ref empty string", func(t *testing.T) {
		schema := map[string]any{"$ref": ""}
		_, err := ResolveSchemaRef(schema, "")
		if err == nil {
			t.Error("expected error for empty $ref")
		}
	})
}
