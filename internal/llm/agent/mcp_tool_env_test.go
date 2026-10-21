package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

// A stdio MCP server inherits opencode's environment minus the tool
// environment deny list, and still receives every value from its own
// configured env, denied names included: writing one there grants it to that
// server.
func TestMCPProcCommand_FiltersInheritedEnv(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_PASSWORD", "api-s3cret")
	t.Setenv("MCP_TEST_DENIED", "op-s3cret")
	t.Setenv(toolenv.EnvVar, "MCP_TEST_DENIED")
	t.Setenv("MCP_TEST_KEPT", "kept")

	cmd, err := (&mcpProc{}).command(context.Background(), "true", []string{"MCP_TEST_DENIED=granted"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cmd.Env, "OPENCODE_SERVER_PASSWORD=api-s3cret") {
		t.Error("inherited a built-in denied variable")
	}
	if slices.Contains(cmd.Env, "MCP_TEST_DENIED=op-s3cret") {
		t.Error("inherited an operator-denied variable")
	}
	if !slices.Contains(cmd.Env, "MCP_TEST_DENIED=granted") {
		t.Error("the server's own configured env value was not passed")
	}
	if !slices.Contains(cmd.Env, "MCP_TEST_KEPT=kept") {
		t.Error("an undenied variable was dropped")
	}
}
