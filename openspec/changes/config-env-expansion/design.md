## Context

Viper 1.20 supports pluggable decoders. Global config is read by the singleton and project config by another Viper instance. Defaults inspect provider keys before final Unmarshal. UpdateCfgFile currently unmarshals raw JSON into Config, which cannot read numeric references and discards unknown fields. See proposal.md for motivation.

## Goals / Non-Goals

Goals: safe interpolation before Viper merges/defaults, preserving typed config, precedence, placeholders and atomic writeback. No shell execution, JSON object injection, key expansion, or recursive expansion.

## Decisions

- Install a JSON decoder on both Viper instances. Parse JSON first, then recursively expand string values, so quotes/newlines/backslashes in secrets cannot alter JSON structure. Avoid raw-text replacement, post-Unmarshal reflection and Set overrides, which break escaping, provider detection or precedence.
- Use braced ASCII names, literal fallbacks, and doubled-dollar escaping. Missing required references fail early. Keep bare shell variables and dotted context tokens intact. Preserve reserved `${agent}` context token to avoid breaking scoped-context configurations.
- Decode a typed file snapshot for writeback, compare its JSON representation before/after the callback, and apply only changed fields to the original JSON tree. Recurse through maps and equal-length arrays so edits to one identity preserve sibling references. Use mapstructure with JSON tags and weak scalar coercion to match Viper while preserving raw map key case. Existing atomic writer and permissions stay in place.
- Wrap constrained scalar schemas with an interpolation alternative while retaining literal constraints. Keep enum consistency tests inspecting the literal branch.
- Return diagnostics without expanded values; do not dump configs, environment values, or upstream request headers in tests.

## Risks / Trade-offs

- Existing braced shell expressions could be interpreted → document escaping and test bare shell/dotted context syntax.
- Typed writeback could serialize secrets → patch raw JSON using the typed diff and test nested arrays/maps, unknown fields and zero values.
- Invalid project JSON was previously ignored → return project read errors so unresolved references cannot silently fall back to a global secret.

## Migration Plan

Users can replace values with references after upgrade. No files are migrated automatically; placeholders remain portable. Rollback requires substituting literal values for references first.

## Verification notes

`LC_ALL=C make test` passed with race detection, formatting and vet. The first run under the inherited unsupported `C.UTF-8` locale failed three unrelated hook stderr assertions because Bash prepended locale warnings. The full e2e target passed ACP and image/PDF attachment cases, then hung in the attachment script's existing server-shutdown wait; it was stopped without modifying that unrelated script. The new process e2e passed for both Kimi models. The repository-wide hidden-character script reports six existing zero-width spaces in generated `internal/lsp/protocol/tsprotocol.go`, byte-identical to main; changed Go files contain none.
