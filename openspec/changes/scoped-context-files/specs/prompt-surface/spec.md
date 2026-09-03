# prompt-surface (delta)

Delta spec for the `scoped-context-files` change. Restates only the requirements
that change; unchanged requirements are not repeated here. For the full specification
see `openspec/specs/prompt-surface/spec.md`.

## MODIFIED Requirements

### Requirement: Builtin base prompts are lean and within budget

*(Existing requirement heading preserved.)*

The static byte budgets and content rules are unchanged. The following addendum is
added:

The `# Project-Specific Context` section of the assembled system prompt now renders
the **scoped-resolved** context block returned by `internal/contextfile.Resolve()`
rather than the process-global string from `getContextFromPaths()`. The content and
format of the rendered block are identical when no agent or step override is in effect
(see `context-resolution` spec, "No config yields byte-identical behavior" scenario).

When `contextDiscovery.enabled` is true and ≥1 nested file was discovered, the system
prompt additionally contains a **manifest section** (see `progressive-context-disclosure`
spec, "Manifest block" requirement). The manifest section is dynamic: its content and
byte count depend on the discovery walk result and the current config, and may be
empty (zero-byte delta) for repos with no nested context files. The manifest MUST NOT
cause the static byte-budget tests for `coder`, `workhorse`, `hivemind`, or `explorer`
to fail — the budget tests operate on the base prompt and its static appendices only.

The budget test file (`internal/llm/prompt/prompt_test.go`) SHALL be updated to
acknowledge the manifest section: a comment SHALL note that the manifest may add bytes
when discovery is enabled and nested context files exist, and that this is expected and
exempt from the static budget assertions.

#### Scenario: Budget tests remain green with discovery enabled

- **WHEN** `go test ./internal/llm/prompt` runs in a workspace that happens to contain
  nested context files
- **THEN** the budget tests for `coder`, `workhorse`, `hivemind`, and `explorer` pass
  because they measure only the static base prompt and its static appendices, not the
  discovery-dependent manifest section

#### Scenario: No-nested-files workspace yields byte-identical prompt

- **WHEN** a workspace has no files strictly below `workDir` matching any `contextPaths`
  basename (i.e. the discovery walk finds nothing)
- **THEN** the assembled system prompt is byte-identical to the pre-feature prompt for
  the same agent and `contextPaths` config, with no manifest section added
