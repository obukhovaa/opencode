## Why

Kimi K3.1 Pro and Pro Highspeed are available on the native Kimi API but cannot yet be selected in OpenCode. Register both with official USD pricing and the documented context and reasoning capabilities.

## What Changes

- Add both native Kimi model IDs, flat pricing, vision, and default maximum reasoning effort.
- Retain the existing Anthropic-compatible transport and verify live requests without recording credentials.
- Update the model schema and README. AWS currently lists only K3 and K2 variants, so omit Bedrock mirrors.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `kimi-provider`: register K3.1 Pro variants and their native request behavior.

## Impact

Model registry, Kimi config defaults, provider tests, generated schema, README. No new dependencies or provider credentials.
