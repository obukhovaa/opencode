## Context

The user corrected the initial tool-based design: Claude Code uses ordinary file tools for auto memory. The shared store and stable project identity remain useful. Long-lived OpenCode agent instances serve multiple sessions, so an index captured only when constructing the provider would become stale across sessions.

## Goals / Non-Goals

Goals: keep context small, use existing tools, expose project scope and freshness, retain accumulated knowledge across sessions.

Non-goals: a new memory tool, automatic loading of topic bodies, raw transcript ingestion, automatic deletion based only on age, or a filesystem quota that can constrain arbitrary Bash commands.

## Decisions

1. Retain opt-in memory.enabled/directory/projectID (a non-empty AGENTS_MEMORY_HOME selects the root per launch, ahead of memory.directory) and the shared projects/<sha256-id>/memory layout. Remove every memory tool/provider/grant change. Instructions name real filesystem paths and only tools available to the agent.
2. A short system reminder explains relevance-based reading, durable concise facts, links, correction/deduplication and index upkeep. There are two capability levels: read-only agents receive reading guidance; writers always also read and receive both. File editing tools count as writers only alongside the read tool (they require a recorded read); permitted Bash reads and writes on its own. Write-only agents are deliberately unsupported. Disabled/no-file-tool agents receive nothing. Denied or approval-required index reads are not performed automatically.
3. Inject one synthetic, clearly delimited project-index snapshot per active session history after the compaction gate, alongside existing context envelopes. Do not duplicate it on subsequent turns; reload for another session or after compaction removes the snapshot. This preserves the static system prefix and counts the snapshot in normal message token accounting. No topic body is read by the startup loader.
4. Index context has hard limits of 200 lines and 25 KiB, whichever is reached first, including an explicit truncation notice. Topic authoring budget is 8 KiB. Successful ordinary write/edit/multiedit/patch results report oversize indexes/notes and missing/invalid frontmatter; they do not destroy or silently truncate saved content. Bash receives the same authoring instructions, but cannot be given a new tool-specific filesystem quota. Existing read/write permissions and concurrency behavior remain authoritative.
5. New topic frontmatter requires only description (one-line retrieval cue) and updatedAt (RFC3339 UTC last factual update). Index entries carry a link, retrieval cue and updatedAt. Preserve other legacy fields. Old timestamps prompt re-verification for volatile facts; age alone does not invalidate stable knowledge. Reading, copying or importing must not rejuvenate a note.
6. Ordinary file writers do not acquire mem-sync's internal lock. Keep narrow edits, read-before-write checks and conservative sync conflict handling; document concurrent external-writer limits rather than claiming all writes are serialized.

## Research basis

- Claude Code memory: https://code.claude.com/docs/en/memory — bounded startup index; topic files on demand; write feedback rather than destructive truncation.
- Karpathy LLM Wiki: https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f — linked retrieval cues, maintained synthesis and contradiction/staleness checks. Adopt the small index/notes pattern, without raw-source archives or a separate chronological log in model context.
- Anthropic context engineering: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents — keep compact references and use existing tools for just-in-time retrieval.

## Risks / Trade-offs

- Index snapshot can age during a session → label it as a snapshot and re-read before edits or when freshness matters.
- Imported legacy notes can exceed the budget → preserve data, report warnings and ask the agent to split/summarize; hard-cap only automatic context.
- Index files are untrusted evidence → delimit/escape snapshot text and never treat it as policy or authorization.
- Ordinary Bash/file tools can bypass suggested budgets → hard bound startup context and provide authoring feedback without claiming storage enforcement.

## Migration Plan

Update the existing unmerged MRs, remove memory grants, deploy the revised runtime and keep the same aggregate path. Existing Markdown remains readable. Disable memory.enabled to remove reminders and automatic index loading.
