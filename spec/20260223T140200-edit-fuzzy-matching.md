# Edit Tool — Fuzzy Matching Pipeline

**Date**: 2026-02-23
**Status**: Draft
**Author**: AI-assisted

## Overview

Implement a multi-strategy matching pipeline for `old_string` lookup in the `edit` tool so that minor whitespace and indentation differences no longer cause edit failures. Exact match remains the first and preferred strategy; fuzzy strategies are fallbacks of decreasing permissiveness.

## Motivation

### Current State

Both `edit` and `multiedit` resolve `old_string` with a single `strings.Index` call after CRLF normalization:

```go
// internal/llm/tools/edit.go — replaceContent()
oldContent := strings.ReplaceAll(string(content), "\r\n", "\n")
normalizedOldString := strings.ReplaceAll(oldString, "\r\n", "\n")

index := strings.Index(oldContent, normalizedOldString)
if index == -1 {
    return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
}
```

Uniqueness is checked via `strings.LastIndex`:

```go
lastIndex := strings.LastIndex(oldContent, normalizedOldString)
if index != lastIndex {
    count := strings.Count(oldContent, normalizedOldString)
    return NewTextErrorResponse(fmt.Sprintf("old_string appears %d times in the file...", count)), nil
}
```

`replace_all` bypasses the uniqueness check and uses `strings.ReplaceAll`.

This creates problems:

1. **Fragile on whitespace drift**: LLMs frequently produce `old_string` with slightly different indentation (tabs vs spaces, trailing spaces, mixed indent levels). Every such mismatch is a hard failure requiring a retry.
2. **No recovery path**: The error message tells the agent to "match exactly" but gives no hint about what differs, forcing a full re-read of the file.
3. **Retry cost**: Each failed edit consumes a tool call and a round-trip. In long sessions this compounds into significant latency and token waste.

### Desired State

When exact match fails, the tool tries a sequence of progressively looser strategies. The first strategy that finds a **unique** match wins. The response indicates which strategy was used so the agent can self-correct in future calls. If no strategy finds a unique match, the existing error is returned unchanged.

## Research Findings

### Reference Implementation (Anthropic's computer-use tools)

The reference uses 9 ordered strategies:

| # | Strategy | Description |
|---|----------|-------------|
| 1 | Exact | Byte-for-byte after CRLF normalization |
| 2 | Trimmed exact | `strings.TrimSpace` on both sides |
| 3 | Whitespace-normalized | Collapse all whitespace runs to single space |
| 4 | Line-trimmed | `strings.TrimSpace` each line, rejoin with `\n` |
| 5 | Block-aligned | Align `old_string` indent to match the file's indent at the candidate position |
| 6 | Indentation-flexible | Strip all leading whitespace per line, compare |
| 7 | Levenshtein distance | Accept if edit distance / max(len) < threshold (e.g. 0.2) |
| 8 | Subsequence match | Check if `old_string` lines appear as a subsequence |
| 9 | Best fuzzy across sliding window | Score all windows, pick highest similarity |

**Key finding**: Strategies 7–9 carry meaningful false-positive risk. The reference mitigates this with a similarity threshold but still occasionally matches the wrong block in files with repetitive structure.

**Implication**: Start with strategies 1–4 (deterministic, zero false-positive risk) and defer 5–9 until failure-rate data justifies the added complexity.

### Go Standard Library

No built-in Levenshtein. If added later, implement inline or use `golang.org/x/text` distance utilities — do not add a new dependency for the MVP.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Number of initial strategies | 4 (exact → whitespace-normalized → indentation-flexible → trimmed-boundary) | Covers the most common LLM drift patterns with zero false-positive risk |
| Strategy ordering | Strictest first | A looser strategy should never win when a stricter one would also match |
| First-match-wins vs scoring | First unique match wins | Simpler, deterministic, no threshold tuning needed for MVP |
| Fuzzy match with `replace_all` | Disabled — exact only | `replace_all` replaces every occurrence; fuzzy expansion of the match set is too risky |
| Uniqueness requirement | Required for fuzzy strategies too | If a fuzzy strategy finds multiple candidates, skip it and try the next |
| Response annotation | Append strategy name to success message | Gives the agent signal to improve future `old_string` quality |
| New file location | `internal/llm/tools/edit_match.go` | Keeps matching logic isolated and testable independently |
| `multiedit` support | Follow-up | `multiedit` has sequential edits where fuzzy matching on intermediate state is harder to reason about; address after `edit` is validated |

## Architecture

### Matching Pipeline

```
findMatch(content, oldString) → (index int, matchedSpan string, strategy string, ok bool)
```

```
STRATEGY 1: Exact
─────────────────
strings.Index(content, oldString)
→ found unique? → return (index, oldString, "exact", true)
→ found multiple? → skip (not unique)
→ not found? → next strategy

STRATEGY 2: Whitespace-normalized
──────────────────────────────────
normalizeWS(content) vs normalizeWS(oldString)
Map normalized index back to original content index
→ found unique? → return (originalIndex, originalSpan, "whitespace-normalized", true)
→ not unique / not found? → next strategy

STRATEGY 3: Indentation-flexible
─────────────────────────────────
stripLeadingWS(each line of content) vs stripLeadingWS(each line of oldString)
→ found unique? → return (originalIndex, originalSpan, "indentation-flexible", true)
→ not unique / not found? → next strategy

STRATEGY 4: Trimmed-boundary
─────────────────────────────
strings.TrimSpace(oldString) — remove leading/trailing blank lines
Match trimmed pattern against content
→ found unique? → return (originalIndex, originalSpan, "trimmed-boundary", true)
→ not unique / not found? → FAIL

FAIL: return existing error message (unchanged behavior)
```

### Integration Point in `replaceContent` / `deleteContent`

```
// Before (single call):
index := strings.Index(oldContent, normalizedOldString)
if index == -1 { return error }

// After:
index, matchedSpan, strategy, ok := findMatch(oldContent, normalizedOldString)
if !ok { return error }
// use matchedSpan (not normalizedOldString) for the replacement bounds
// append strategy hint to success response if strategy != "exact"
```

The `matchedSpan` is the actual substring in `oldContent` that was matched. This is important for strategies that alter whitespace: the replacement must excise the original bytes, not the normalized pattern.

### File Structure

```
internal/llm/tools/
├── edit.go          — calls findMatch, uses matchedSpan for replacement
├── edit_match.go    — findMatch, all strategy implementations, unit tests
└── edit_test.go     — integration tests for the full edit flow
```

## Implementation Plan

### Phase 1: Core matching module

- [ ] **1.1** Create `internal/llm/tools/edit_match.go` with the `matchResult` struct and `findMatch` function signature.
- [ ] **1.2** Implement Strategy 1 (exact) inside `findMatch` — identical to current behavior.
- [ ] **1.3** Implement Strategy 2 (whitespace-normalized): collapse `\s+` runs to a single space on both sides, find match in normalized content, map index back to original via a rune-offset table.
- [ ] **1.4** Implement Strategy 3 (indentation-flexible): strip leading whitespace from each line of both sides, join with `\n`, find match, map back to original.
- [ ] **1.5** Implement Strategy 4 (trimmed-boundary): `strings.TrimSpace` the `oldString`, match against content.
- [ ] **1.6** Write table-driven unit tests in `edit_match_test.go` covering: exact hit, exact miss → whitespace fallback, exact miss → indentation fallback, exact miss → trimmed fallback, all strategies miss, multiple candidates (non-unique) per strategy.

### Phase 2: Wire into edit tool

- [ ] **2.1** Replace the `strings.Index` call in `replaceContent` with `findMatch`. Use `matchedSpan` length (not `len(normalizedOldString)`) when slicing `oldContent` for replacement.
- [ ] **2.2** Replace the `strings.Index` call in `deleteContent` with `findMatch` (same pattern).
- [ ] **2.3** When `strategy != "exact"`, append a note to the success response: `"(matched via <strategy> — consider updating old_string to match exactly)"`.
- [ ] **2.4** Ensure `replace_all=true` path bypasses `findMatch` entirely and continues to use `strings.ReplaceAll` on the exact normalized string.
- [ ] **2.5** Run `go test ./internal/llm/tools/...` and fix any regressions.

### Phase 3: Observability (deferred)

- [ ] **3.1** Log strategy used at `DEBUG` level via `logging.Debug` for offline analysis of failure rates.
- [ ] **3.2** After collecting data, evaluate whether strategies 5–9 from the reference are worth adding.
- [ ] **3.3** Apply the same `findMatch` pipeline to `multiedit` once the `edit` implementation is stable.

## Edge Cases

### Index mapping after normalization

1. Strategy 2 normalizes whitespace in both content and pattern.
2. The match index is in the normalized string, not the original.
3. Must build a forward mapping `normalizedOffset → originalOffset` to find the correct splice point and span in the original content.
4. Off-by-one errors here corrupt the file — test with content that has multi-byte whitespace sequences.

### Pattern longer than file

1. `oldString` is longer than `content` after normalization.
2. All strategies return no match immediately.
3. Return the standard "not found" error.

### Fuzzy match finds multiple candidates

1. Strategy N finds 2+ positions in the file.
2. Skip strategy N (non-unique), try strategy N+1.
3. If all strategies find multiple candidates, return the standard multiple-match error (with count from the exact match, or from the loosest strategy that found matches).

### Empty lines in `old_string`

1. Strategy 4 (trimmed-boundary) strips leading/trailing blank lines from `old_string`.
2. If `old_string` is entirely blank lines, trimming produces an empty string — treat as no-op and skip this strategy.

### `replace_all` with fuzzy

1. `replace_all=true` is set.
2. `findMatch` is NOT called. Exact `strings.ReplaceAll` is used as today.
3. If exact match finds zero occurrences, return the standard "not found" error without attempting fuzzy.

## Open Questions

1. **Should the strategy name appear in the error path too?**
   - If all strategies fail, should the error message list which strategies were tried?
   - Options: (a) yes — helps the agent understand what was attempted, (b) no — keeps error messages short.
   - **Recommendation**: No for MVP. The existing error message is already actionable. Add only if agent logs show confusion.

2. **Similarity threshold for future Levenshtein strategy**
   - What edit-distance ratio constitutes "close enough"? The reference uses ~0.2 (80% similarity).
   - **Recommendation**: Defer entirely. Collect real failure data from strategies 1–4 first. If >10% of edit failures would have been caught by Levenshtein, add it with a conservative threshold (0.1).

3. **Should `multiedit` get fuzzy matching in the same PR?**
   - `multiedit` applies edits sequentially; fuzzy matching on intermediate state (after edit N, before edit N+1) is harder to reason about.
   - **Recommendation**: Defer to a follow-up. Validate `edit` first, then port `findMatch` to `multiedit` with the same interface.

4. **Index mapping complexity for Strategy 2**
   - Building a full offset map for whitespace normalization is non-trivial. An alternative is to use a sliding-window search on the original content using the normalized pattern as a fingerprint.
   - **Recommendation**: Use the sliding-window approach — simpler to implement correctly and avoids the mapping table entirely. For each candidate window of the same line count as `old_string`, normalize both and compare.

## Success Criteria

- [ ] `findMatch` returns the correct span for all four strategies in unit tests.
- [ ] An `old_string` with wrong indentation (tabs vs spaces) succeeds via Strategy 3 instead of failing.
- [ ] An `old_string` with a leading/trailing blank line succeeds via Strategy 4 instead of failing.
- [ ] `replace_all=true` is unaffected — still uses exact `strings.ReplaceAll`.
- [ ] Non-unique fuzzy matches are rejected (uniqueness check applies to all strategies).
- [ ] Success response includes strategy annotation when a fuzzy strategy was used.
- [ ] All existing `edit` and `multiedit` tests pass: `go test ./internal/llm/tools/...`
- [ ] `make test` passes.

## References

- `internal/llm/tools/edit.go` — `replaceContent`, `deleteContent` — primary integration points
- `internal/llm/tools/multiedit.go` — same matching pattern, follow-up target
- `internal/llm/tools/edit_test.go` — existing tests to preserve
- `internal/llm/tools/file.go` — shared file tracking utilities
- `spec/20260223T133437-tools-imrovements.md` — parent spec, item 3.3 (this feature)
