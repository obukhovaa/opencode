## Why

Sessions need durable project knowledge without adding a memory-specific tool schema or preloading detailed notes. A compact index and tool-aware system reminder let agents use the ordinary file tools they already have.

## What Changes

- Replace the dedicated memory tool and provider-specific serialization with file-based project memory guidance.
- Load one bounded project MEMORY.md index into session context; retrieve topic files only on demand.
- Tailor instructions to effective read/write/Bash capabilities and honor permissions before preloading data.
- Standardize concise topic notes with description and updatedAt metadata; give size/metadata feedback through existing write tools.
- Preserve the shared aggregate, Git identities/aliases and mem-sync interoperability.

## Capabilities

### New Capabilities
- `project-memory`: Bounded index context, capability-aware reminders, freshness metadata and ordinary file-tool feedback.

### Modified Capabilities

None.

## Impact

Changes prompt/session context assembly, existing file-tool result feedback, configuration/schema, docs and tests. Removes the memory tool, its grants, reserved name and native-provider branches. No session database migration or new external service.

Tracking: [GENAI-518](https://tinypass.atlassian.net/browse/GENAI-518).
