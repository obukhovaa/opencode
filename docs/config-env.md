# Environment references in configuration

Global and project `.opencode.json` files can reference process environment
variables in every string value, including provider credentials and endpoints,
MCP headers and arguments, router tokens, prompts, arrays and map values.

```json
{
  "providers": {
    "kimi": {
      "apiKey": "${MY_MOONSHOT_KEY}",
      "baseURL": "${MOONSHOT_URL:-https://api.moonshot.ai/anthropic}"
    }
  },
  "agents": { "coder": { "model": "${CODER_MODEL:-kimi.kimi-k3.1-pro}" } },
  "maxTurns": "${MAX_TURNS:-100}",
  "autoCompact": "${COMPACT_ENABLED:-true}"
}
```

Names match `[A-Za-z_][A-Za-z0-9_]*` and are case sensitive. `${NAME}` requires
the variable to exist; an explicitly empty value is allowed. `${NAME:-fallback}`
uses its literal fallback when the variable is unset or empty. Missing required
variables fail configuration loading; diagnostics identify the variable without
printing its surrounding value. Export variables before starting OpenCode.

Use `$${NAME}` to retain `${NAME}` for a later shell or template. Bare `$NAME`
is unchanged. Existing context expressions `${agent}`, `${flow.step}`,
`${flow.id}` and `${env.NAME}` remain available to context resolution. The name
`agent` is reserved for that existing expression; use another environment name
when interpolating configuration. Unsupported and unterminated expressions stay
literal. Nested expressions in fallbacks are unsupported and stay literal.
Existing field-specific `env:NAME` support is unchanged.

The JSON decoder expands parsed string values before Viper merges files,
selects provider defaults and unmarshals the typed configuration. Quotes,
newlines, backslashes, dollar signs and shell syntax in an environment value are
data: no shell runs, JSON is not injected, and inserted text is not expanded
again. Viper converts expanded scalar strings to numbers and booleans where the
configuration field requires them. A reference cannot replace a whole object or
array with serialized JSON. Map keys never expand.

Project values still override global file values. Existing explicit environment
overrides and application defaults keep their priority. Each file is expanded
independently, so required global references must exist even if a project value
would override them. Files are read once at startup; restart to pick up changes
to the environment.

Settings edits apply only changed fields to the original document before atomic
replacement. Unchanged references, unknown fields, map key spelling and large
integers remain on disk. A changed field stores its new literal value. Editing
one router identity preserves sibling tokens; deleting or moving identities
retains references on the remaining identities. Existing file permission rules
remain in effect. Resolved credentials exist in process memory and are used in
requests normally.

The generated JSON schema permits references in constrained scalar fields
(including model enums, numbers and booleans) and retains ordinary literal
validation. The resolved value is checked at runtime.
