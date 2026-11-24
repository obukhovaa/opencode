package tools

import (
	"context"
	"path/filepath"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
)

func argsLookup(args map[string]any) func(string) (any, bool) {
	return func(path string) (any, bool) {
		v, ok := args[path]
		return v, ok
	}
}

func TestResolveListingScope(t *testing.T) {
	cfg := &config.ListingScopeConfig{
		Skills:    []string{".agents/skills/**", "${args.team}/skills/**"},
		Subagents: []string{".agents/types/**"},
	}

	t.Run("resolved team", func(t *testing.T) {
		got := ResolveListingScope(cfg, argsLookup(map[string]any{"team": "composer"}))
		if len(got.Skills) != 2 || got.Skills[1] != "composer/skills/**" {
			t.Fatalf("skills = %q", got.Skills)
		}
		if len(got.Subagents) != 1 || got.Subagents[0] != ".agents/types/**" {
			t.Fatalf("subagents = %q", got.Subagents)
		}
	})

	for name, args := range map[string]map[string]any{
		"absent":     {},
		"empty":      {"team": ""},
		"blank":      {"team": "  "},
		"non-scalar": {"team": map[string]any{"x": 1}},
		"null":       {"team": nil},
	} {
		t.Run("unresolved "+name+" leaves only the token list unscoped", func(t *testing.T) {
			got := ResolveListingScope(cfg, argsLookup(args))
			if got.Skills != nil {
				t.Fatalf("skills should not apply, got %q", got.Skills)
			}
			if got.Subagents == nil {
				t.Fatal("a token-free list applies independently")
			}
		})
	}

	t.Run("unsupported token", func(t *testing.T) {
		got := ResolveListingScope(&config.ListingScopeConfig{Skills: []string{"${flow.id}/skills/**"}},
			argsLookup(map[string]any{"team": "composer"}))
		if got.Skills != nil {
			t.Fatalf("skills = %q, want nil", got.Skills)
		}
	})

	t.Run("scalars", func(t *testing.T) {
		got := ResolveListingScope(&config.ListingScopeConfig{Skills: []string{"t${args.n}/${args.b}"}},
			argsLookup(map[string]any{"n": float64(7), "b": true}))
		if len(got.Skills) != 1 || got.Skills[0] != "t7/true" {
			t.Fatalf("skills = %q", got.Skills)
		}
	})

	t.Run("nil config and empty lists", func(t *testing.T) {
		if got := ResolveListingScope(nil, argsLookup(nil)); got.Skills != nil || got.Subagents != nil {
			t.Fatalf("nil config = %+v", got)
		}
		if got := ResolveListingScope(&config.ListingScopeConfig{Skills: []string{}}, argsLookup(nil)); got.Skills != nil {
			t.Fatalf("empty list = %q", got.Skills)
		}
	})
}

func TestInListingScope(t *testing.T) {
	wd := t.TempDir()
	at := func(rel string) string { return filepath.Join(wd, filepath.FromSlash(rel)) }
	patterns := []string{"./.agents/skills/**", "composer/skills/**"}

	cases := []struct {
		location string
		want     bool
	}{
		{at(".agents/skills/test-feature/SKILL.md"), true},
		{at("composer/skills/composer-devops/SKILL.md"), true},
		{at("composer/skills/group/nested/SKILL.md"), true},
		{at("bdata/skills/dcs-events/SKILL.md"), false},
		{"composer/skills/relative/SKILL.md", true},
		{filepath.Join(filepath.Dir(wd), "elsewhere", "SKILL.md"), false},
		{"", true},
	}
	for _, c := range cases {
		if got := inListingScope(wd, patterns, c.location); got != c.want {
			t.Errorf("inListingScope(%q) = %v, want %v", c.location, got, c.want)
		}
	}
}

func TestListingScopeValueIsLiteral(t *testing.T) {
	wd := t.TempDir()
	scope := ResolveListingScope(&config.ListingScopeConfig{Skills: []string{"${args.team}/skills/**"}},
		argsLookup(map[string]any{"team": "*"}))
	if inListingScope(wd, scope.Skills, filepath.Join(wd, "composer", "skills", "x", "SKILL.md")) {
		t.Fatal("a `*` team value must not match every directory")
	}
	if !inListingScope(wd, scope.Skills, filepath.Join(wd, "*", "skills", "x", "SKILL.md")) {
		t.Fatal("a `*` team value must match a directory literally named `*`")
	}
}

func TestListingScopeContext(t *testing.T) {
	ctx := context.Background()
	scope := agentregistry.ListingScope{Skills: []string{"a/**"}}
	scoped := WithListingScope(ctx, scope)
	if got := ListingScopeFrom(scoped); len(got.Skills) != 1 || got.Subagents != nil {
		t.Fatalf("round trip = %+v", got)
	}
	if got := ListingScopeFrom(WithListingScope(scoped, agentregistry.ListingScope{})); got.Skills != nil {
		t.Fatalf("the zero scope must override an inherited one, got %+v", got)
	}
	if ListingScopeKey(agentregistry.ListingScope{}) != "" {
		t.Fatal("zero scope key must be empty")
	}
	a := ListingScopeKey(agentregistry.ListingScope{Skills: []string{}})
	b := ListingScopeKey(agentregistry.ListingScope{Subagents: []string{}})
	if a == b || a == "" {
		t.Fatalf("keys must tell the lists apart: %q vs %q", a, b)
	}
}
