## 1. Reasoning transport

- [x] 1.1 Preserve supported compatible reasoning in stream/nonstream completion and persisted message parts with adequate replay provenance.
- [x] 1.2 Replay compatible reasoning on the next request while retaining native Claude and ordinary first-party behavior.
- [x] 1.3 Add persisted-message wire tests for tool loops, nonstream responses, incompatible switches and legacy data.

## 2. Bash output

- [x] 2.1 Enforce byte and line bounds together while preserving spill paths, UTF-8 and exit metadata.
- [x] 2.2 Add regressions for single-line JSON, many long lines, Unicode, under-limit output and persistence limitations.

## 3. Verification and review

- [x] 3.1 Document supported reasoning replay and Bash excerpt semantics.
- [x] 3.2 Run focused end-to-end fixtures, repository checks and independent final review; fix findings.
- [x] 3.3 Record verification evidence and a reviewer-facing summary for the GENAI-671 correctness wave.
