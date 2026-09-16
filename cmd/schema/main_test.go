package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// schemaPath is the committed artifact this generator produces. Tests read
// the committed file rather than regenerating so they assert what users
// actually get from their IDE.
const schemaPath = "../../opencode-schema.json"

func loadSchema(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(schemaPath))
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", schemaPath, err)
	}
	return doc
}

// enumAt walks properties.<seg>.properties.<seg>... and returns the enum
// declared at the leaf.
func enumAt(t *testing.T, doc map[string]any, path ...string) []string {
	t.Helper()
	node := doc
	for i, seg := range path {
		props, ok := node["properties"].(map[string]any)
		if !ok {
			t.Fatalf("no properties at %v", path[:i])
		}
		child, ok := props[seg].(map[string]any)
		if !ok {
			t.Fatalf("no property %q at %v", seg, path[:i])
		}
		node = child
	}
	rawEnum, ok := node["enum"].([]any)
	if !ok {
		t.Fatalf("no enum at %v", path)
	}
	out := make([]string, 0, len(rawEnum))
	for _, v := range rawEnum {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("non-string enum entry %#v at %v", v, path)
		}
		out = append(out, s)
	}
	return out
}

// TestSchemaEnumsMatchRuntimeValidators is the gate that `make schema-check`
// cannot be: regenerating catches only an artifact that lags its generator,
// and both can be stale together against internal/config. That is exactly
// what shipped once — NormalizeToolUpdateVerbosity gained the "verbose" and
// "debug" aliases while cmd/schema/main.go kept enum ["compact","full"], so
// regeneration was a no-op and a valid .opencode.json was flagged invalid by
// every IDE.
//
// For each enum-bearing field whose runtime acceptance is decided by a Go
// validator, the schema's enum MUST equal the set of values that validator
// accepts. Both directions are checked:
//
//   - enum ⊆ accepted: the schema must not advertise a value the loader
//     rejects (users write it, it silently does nothing).
//   - accepted ⊆ enum: the loader must not accept a value the schema
//     rejects (users get a false-positive error on a working config).
//
// The second direction is probed over the enum plus a corpus of aliases a
// person might plausibly reach for. A brand-new alias outside that corpus
// would still slip through, so extend `probe` when adding one.
func TestSchemaEnumsMatchRuntimeValidators(t *testing.T) {
	doc := loadSchema(t)

	cases := []struct {
		name string
		path []string
		// accepts reports whether the runtime loader takes this value.
		accepts func(string) bool
		// probe holds non-enum values to test the accepts ⊆ enum
		// direction. Words someone might reasonably write.
		probe []string
	}{
		{
			name: "router.toolUpdateVerbosity",
			path: []string{"router", "toolUpdateVerbosity"},
			accepts: func(v string) bool {
				_, ok := bridge.NormalizeToolUpdateVerbosity(v)
				return ok
			},
			probe: []string{
				"verbose", "debug", "quiet", "silent", "off", "on",
				"all", "none", "minimal", "detailed", "compact", "full",
				"chatty", "trace", "info", "brief", "terse",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enum := enumAt(t, doc, tc.path...)
			inEnum := make(map[string]bool, len(enum))
			for _, v := range enum {
				inEnum[v] = true
			}

			// enum ⊆ accepted
			for _, v := range enum {
				if !tc.accepts(v) {
					t.Errorf("schema enum advertises %q but the runtime validator rejects it; "+
						"a user writing it gets no validation error and no effect", v)
				}
			}

			// accepted ⊆ enum
			var missing []string
			for _, v := range tc.probe {
				if tc.accepts(v) && !inEnum[v] {
					missing = append(missing, v)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("the runtime validator accepts %q but the schema enum %q omits them; "+
					"IDEs will flag a valid .opencode.json. Add them in cmd/schema/main.go "+
					"and run 'make schema'", missing, enum)
			}
		})
	}
}

// TestSchemaIsUpToDate is the in-test twin of `make schema-check`, so a
// plain `go test ./...` catches artifact drift even when CI is not the one
// running. It compares the committed file against a fresh generation.
func TestSchemaIsUpToDate(t *testing.T) {
	committed, err := os.ReadFile(filepath.Clean(schemaPath))
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	generated, err := json.MarshalIndent(generateSchema(), "", "  ")
	if err != nil {
		t.Fatalf("marshal generated schema: %v", err)
	}
	generated = append(generated, '\n')
	if string(committed) != string(generated) {
		t.Errorf("opencode-schema.json is out of date with cmd/schema/main.go. "+
			"Run 'make schema' and commit the result. (committed %d bytes, generated %d)",
			len(committed), len(generated))
	}
}
