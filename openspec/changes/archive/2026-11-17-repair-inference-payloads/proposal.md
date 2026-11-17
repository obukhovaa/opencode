## Why

GENAI-671 reproduced two sources of avoidable inference work: supported OpenAI-compatible reasoning is omitted from subsequent requests, and a large single-line Bash result bypasses the intended byte limit. Correcting these transport/output contracts comes before evaluating additional cost-saving features.

## What Changes

- Retain supported reasoning fields through streaming and non-streaming responses, persistence and subsequent compatible requests, with provider/model-aware replay.
- Enforce Bash output byte and line bounds while keeping the original output retrievable.
- Add wire-level, persistence and output-boundary regression coverage, including native Claude and ordinary GPT compatibility checks.
- Document the measured defects and limits of the upstream cost comparison.

## Capabilities

### New Capabilities

- `reasoning-replay`: Preserve supported provider reasoning across tool-use requests without crossing incompatible provider/model boundaries.
- `shell-output-limits`: Bound Bash stdout/stderr previews by bytes and lines while preserving recoverable output.

### Modified Capabilities

None.

## Impact

Provider conversion, persisted message reasoning metadata if needed, shell output presentation, tests and documentation. No new model selection, warming, background compaction, or production configuration changes. MCP composition is a separate follow-up under GENAI-671.
