---
name: feature-guide
description: Load implementation guidance for a specific product feature. Pass the feature name as an argument (e.g., /feature-guide cron-tool).
user-invocable: true
argument-hint: "<feature-name>"
---

# Feature: $0

!`cat ${SKILL_DIR}/features/$0.md 2>/dev/null || (echo "Feature '$0' not found. Available features:" && ls ${SKILL_DIR}/features/ 2>/dev/null | sed 's/\.md$//' || echo "(none)")`

## Instructions

- Follow the implementation guidance from the feature document above
- Do NOT load other feature files unless explicitly asked — this keeps context clean
- Ask clarifying questions if the spec is ambiguous
