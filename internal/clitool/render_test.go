package clitool

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const structuredManifest = `
name: snow_dcs
description: Query the DCS tenant.
command: /bin/echo
mode: structured
args:
  deny: ["-f"]
parameters:
  query: {type: string, minLength: 1}
  format: {type: string, enum: [JSON, CSV]}
  limit: {type: integer, minimum: 1, maximum: 1000}
  tags: {type: array, items: {type: string}}
  verbose: {type: boolean}
required: [query]
argv:
  - sql
  - ["-c", "dcs"]
  - ["--format", "{format}"]
  - ["--limit={limit}"]
  - ["--tag", "{tags}"]
  - ["--verbose", "{verbose}"]
  - "-q"
  - "{query}"
`

// A group with no placeholder is rejected at load: nothing would guard it.
func TestRender_LiteralOnlyGroupRejected(t *testing.T) {
	wd := t.TempDir()
	if _, err := Parse([]byte(structuredManifest), filepath.Join(wd, "snow_dcs.yaml"), wd); err == nil || !strings.Contains(err.Error(), "references no parameter") {
		t.Fatalf("expected literal-only group rejection, got %v", err)
	}
}

func TestRender_Groups(t *testing.T) {
	wd := t.TempDir()
	// A literal-only group is rejected at load (nothing guards it); use a
	// template whose groups are all guarded.
	body := strings.Replace(structuredManifest, `  - ["-c", "dcs"]`, `  - "-c"
  - "dcs"`, 1)
	m := mustParse(t, wd, "snow_dcs.yaml", body)

	cases := []struct {
		name  string
		input map[string]any
		want  []string
		err   string
	}{
		{"required only", map[string]any{"query": "select 1"},
			[]string{"sql", "-c", "dcs", "-q", "select 1"}, ""},
		{"optional flag group", map[string]any{"query": "q", "format": "CSV"},
			[]string{"sql", "-c", "dcs", "--format", "CSV", "-q", "q"}, ""},
		{"embedded placeholder", map[string]any{"query": "q", "limit": float64(10)},
			[]string{"sql", "-c", "dcs", "--limit=10", "-q", "q"}, ""},
		{"array expands", map[string]any{"query": "q", "tags": []any{"a", "b"}},
			[]string{"sql", "-c", "dcs", "--tag", "a", "b", "-q", "q"}, ""},
		{"boolean true guards", map[string]any{"query": "q", "verbose": true},
			[]string{"sql", "-c", "dcs", "--verbose", "-q", "q"}, ""},
		{"boolean false drops group", map[string]any{"query": "q", "verbose": false},
			[]string{"sql", "-c", "dcs", "-q", "q"}, ""},
		{"value cannot escape its slot", map[string]any{"query": "select 1; -x --password p"},
			[]string{"sql", "-c", "dcs", "-q", "select 1; -x --password p"}, ""},
		{"enum violation", map[string]any{"query": "q", "format": "TABLE"}, nil, "must be one of [JSON, CSV]"},
		{"missing required", map[string]any{"format": "JSON"}, nil, `missing required parameter "query"`},
		{"unknown parameter", map[string]any{"query": "q", "conn": "x"}, nil, `unknown parameter "conn"`},
		{"type violation", map[string]any{"query": 5.0}, nil, "must be a string"},
		{"integer check", map[string]any{"query": "q", "limit": 1.5}, nil, "must be an integer"},
		{"maximum", map[string]any{"query": "q", "limit": 5000.0}, nil, "above maximum"},
		{"minLength", map[string]any{"query": ""}, nil, "shorter than minLength"},
		{"array element type", map[string]any{"query": "q", "tags": []any{1}}, nil, "element 0 must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Render(tc.input)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v / %v, want error containing %q", got, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRender_PolicyAppliesToRenderedArgv(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "t.yaml", `
name: t
description: d
command: /bin/echo
mode: structured
args:
  deny: ["-f"]
parameters:
  file: {type: string}
argv: ["-f", "{file}"]
`)
	_, err := m.Prepare(map[string]any{"file": "x"})
	if err == nil || !strings.Contains(err.Error(), `deny pattern "-f"`) {
		t.Fatalf("rendered argv must go through the policy, got %v", err)
	}
}

func TestStructuredInputSchema(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "snow_dcs.yaml", strings.Replace(structuredManifest, `  - ["-c", "dcs"]`, `  - "-c"`, 1))
	props, req := m.InputSchema()
	if len(props) != 5 || !reflect.DeepEqual(req, []string{"query"}) {
		t.Errorf("schema = %v / %v", props, req)
	}
	if refs := m.TemplateRefs(); !reflect.DeepEqual(refs, []string{"format", "limit", "query", "tags", "verbose"}) {
		t.Errorf("template refs = %v", refs)
	}
}

func TestRender_SchemaDefaultFillsOptionalParameter(t *testing.T) {
	wd := t.TempDir()
	m := mustParse(t, wd, "q.yaml", `
name: q
description: d
command: /bin/echo
mode: structured
parameters:
  query: {type: string}
  format: {type: string, enum: [JSON, CSV], default: JSON}
required: [query]
argv: ["sql", ["--format", "{format}"], "-q", "{query}"]
`)
	got, err := m.Render(map[string]any{"query": "select 1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"sql", "--format", "JSON", "-q", "select 1"}) {
		t.Errorf("default not applied: %q", got)
	}
	got, err = m.Render(map[string]any{"query": "select 1", "format": "CSV"})
	if err != nil || got[2] != "CSV" {
		t.Errorf("explicit value should override the default: %q %v", got, err)
	}
}

