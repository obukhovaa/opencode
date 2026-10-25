## Why

Operators must currently put secrets and deployment-specific URLs directly in many configuration fields. Uniform environment interpolation makes the same JSON reusable without storing resolved secrets on disk.

## What Changes

- Expand `${VAR}` and `${VAR:-default}` in every JSON string value at Viper decode time; unset required variables fail with a value-free diagnostic.
- Support `$${VAR}` escaping and one-pass expansion; preserve bare shell variables, map keys and existing dotted context tokens.
- Preserve Viper merge precedence, typed scalar decoding, and raw placeholders during config writeback.
- Document semantics, extend the JSON schema, and exercise real file loading and writeback in a hermetic process test.

## Capabilities

### New Capabilities

- `config-env-expansion`: interpolation of JSON values before defaults and validation.

### Modified Capabilities

- `config-atomic-writeback`: retain unresolved values and unknown fields when applying typed configuration edits.

## Impact

Viper JSON decoder, local/global config loading, UpdateCfgFile, JSON schema generation, README and docs, unit and process integration tests. No shell evaluation and no new configuration options.
