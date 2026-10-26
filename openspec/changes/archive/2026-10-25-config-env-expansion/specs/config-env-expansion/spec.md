## Purpose

Allow deployment configuration to reference process environment variables in JSON string values without persisting resolved secrets or changing configuration precedence.

## ADDED Requirements

### Requirement: Expand environment references in every JSON string value
The loader SHALL expand `${NAME}` and `${NAME:-fallback}` in JSON string values at every depth in global and project configuration, including arrays and maps. Names SHALL match `[A-Za-z_][A-Za-z0-9_]*` case-sensitively. Fallback text SHALL be literal and apply when the variable is unset or empty. Expansion SHALL be one pass; substitutions SHALL remain data, never shell commands or injected JSON. Numeric and boolean fields SHALL accept expanded scalar strings through the existing weakly typed decoding.

#### Scenario: Nested values and scalar types
- **WHEN** provider keys, MCP URLs and headers, array entries, model IDs, numeric limits, and booleans use environment references
- **THEN** each resolves before provider defaults and validation with JSON special characters preserved

#### Scenario: Missing required variable
- **WHEN** a reference names an unset variable without a fallback
- **THEN** loading fails and the diagnostic names the variable without echoing the configuration value or resolved secrets

#### Scenario: Empty variable and fallback
- **WHEN** a variable is explicitly empty
- **THEN** a plain reference resolves to empty and a reference with a fallback resolves to the fallback

### Requirement: Preserve configuration precedence and literal syntax
Project values SHALL override global values through the existing merge behavior. Explicit environment overrides and defaults SHALL retain their established priority. Expansion SHALL apply only to file values, never map keys. `$${NAME}` SHALL yield literal `${NAME}`; bare `$NAME`, unsupported expressions, and dotted `${agent}`, `${flow.step}`, `${env.NAME}` context expressions SHALL remain unchanged including the reserved `${agent}` context expression.

#### Scenario: Global and local values
- **WHEN** both files define a provider URL using references
- **THEN** the project value wins and defaults still apply to omitted fields

#### Scenario: Literal and nonrecursive values
- **WHEN** file values use escaped references, bare shell variables, dotted context expressions, or environment values containing another reference
- **THEN** only the supported unescaped file references expand once and keys remain unchanged

### Requirement: IDE schema accepts environment references
The published JSON schema SHALL accept supported references in scalar configuration positions while retaining validation of ordinary literal values.

#### Scenario: Enum and typed fields
- **WHEN** a model enum, boolean, or numeric setting contains an environment reference
- **THEN** IDE validation accepts the placeholder and runtime validation checks its resolved value
