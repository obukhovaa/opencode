package flow

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests in this file cover shell markup in step prompts: only the template's
// own !`cmd` spans run, ${args.*} substitution is applied to the template text
// around them, and neither substituted values nor command output are rescanned.

// onlyPrompt returns the single prompt the stub agent received.
func onlyPrompt(t *testing.T, agent *stubAgent) string {
	t.Helper()
	prompts := agent.snapshotPrompts()
	if len(prompts) != 1 {
		t.Fatalf("agent received %d prompts, want 1: %q", len(prompts), prompts)
	}
	return prompts[0]
}

func assertNotCreated(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s exists: a command ran that must not have", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func singleStepFlow(id string, step Step) Flow {
	return Flow{ID: id, Name: id, Spec: FlowSpec{Steps: []Step{step}}}
}

// TestShellMarkup_SubstitutedValuesStayLiteral: values that form a marker on
// their own or together with the template text are rendered as written.
func TestShellMarkup_SubstitutedValuesStayLiteral(t *testing.T) {
	setIncludeWorkspace(t, t.TempDir())
	agent := newStubAgent()
	runOverrideFlow(t,
		singleStepFlow("test-markup-values", Step{ID: "s", Prompt: "v=${args.x} w=${args.y}`code`"}),
		map[string]any{"x": "a!`echo MARKER-RAN`b", "y": "x!"}, agent)

	want := "v=a!`echo MARKER-RAN`b w=x!`code`"
	if got := onlyPrompt(t, agent); got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// TestShellMarkup_TemplateMarkerRunsValuesDoNot: the template's own marker
// runs, while a value shaped like a marker is inserted verbatim and never runs.
func TestShellMarkup_TemplateMarkerRunsValuesDoNot(t *testing.T) {
	setIncludeWorkspace(t, t.TempDir())
	sentinel := filepath.Join(t.TempDir(), "value-ran")
	x := "!`printf MARKER-RAN > " + sentinel + "`"

	agent := newStubAgent()
	runOverrideFlow(t, singleStepFlow("test-markup-template", Step{
		ID:     "s",
		Prompt: "!`printf %s hello` v=${args.x} w=${args.y}`code`",
	}), map[string]any{"x": x, "y": "x!"}, agent)

	want := "hello v=" + x + " w=x!`code`"
	if got := onlyPrompt(t, agent); got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	assertNotCreated(t, sentinel)
}

// TestShellMarkup_CommandOutputIsNotSubstituted: output that looks like a
// placeholder is inserted as-is, and a placeholder inside a marker reaches the
// shell literally.
func TestShellMarkup_CommandOutputIsNotSubstituted(t *testing.T) {
	setIncludeWorkspace(t, t.TempDir())
	agent := newStubAgent()
	runOverrideFlow(t, singleStepFlow("test-markup-output", Step{
		ID:     "s",
		Prompt: "!`printf '%s' '${args.x}'` v=${args.x}",
	}), map[string]any{"x": "value-of-x"}, agent)

	want := "${args.x} v=value-of-x"
	if got := onlyPrompt(t, agent); got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}
