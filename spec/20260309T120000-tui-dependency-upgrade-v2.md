# TUI Dependency Upgrade to Charmbracelet v2 Ecosystem

**Date**: 2026-03-09
**Status**: Draft
**Author**: AI-assisted

## Overview

Upgrade all Charmbracelet TUI dependencies (bubbletea, bubbles, lipgloss, glamour) and related libraries (charmbracelet/x/ansi, bubblezone, chroma, html-to-markdown, go-udiff) to their latest major versions. The four core Charm libraries moved to v2 with new `charm.land` import paths and significant API changes. Other dependencies have patch/minor updates available.

## Motivation

### Current State

Current dependency versions in `go.mod`:

- `github.com/charmbracelet/bubbletea` v1.3.5
- `github.com/charmbracelet/bubbles` v0.21.0
- `github.com/charmbracelet/lipgloss` v1.1.0
- `github.com/charmbracelet/glamour` v0.9.1
- `github.com/charmbracelet/x/ansi` v0.8.0
- `github.com/lrstanley/bubblezone` v0.0.0-20250315020633-c249a3fe1231
- `github.com/alecthomas/chroma/v2` v2.15.0
- `github.com/JohannesKaufmann/html-to-markdown` v1.6.0
- `github.com/aymanbagabas/go-udiff` v0.2.0
- `github.com/muesli/reflow` v0.3.0
- `github.com/muesli/ansi` v0.0.0-20230316100256-276c6243b2f6
- `github.com/muesli/termenv` v0.16.0

### Problems

1. **Stale v1 APIs**: All four Charm libraries released v2 in Feb 2026 with new import paths (`charm.land/*`). Staying on v1 means no new features, no bug fixes, and increasing incompatibility with the ecosystem.
2. **bubblezone incompatibility**: bubblezone v2 (`github.com/lrstanley/bubblezone/v2`) already requires `charm.land/bubbletea/v2` and `charm.land/lipgloss/v2`. It cannot coexist with v1 Charm dependencies.
3. **html-to-markdown v1 is EOL**: The project ships a v2 rewrite (`github.com/JohannesKaufmann/html-to-markdown/v2`) with better accuracy and a cleaner API. Our usage is the dead-simple `md.NewConverter("", true, nil)` → `converter.ConvertString(html)` pattern which maps to v2's `htmltomarkdown.ConvertString(input)`.
4. **go-udiff v0.2 is old**: v0.4.0 is available with upstream improvements and new API methods.
5. **chroma v2.15 is stale**: v2.23.1 is available with many new lexers and style fixes.
6. **muesli/reflow and muesli/ansi are legacy**: These libraries are being superseded by `charmbracelet/x/ansi` functions in the v2 ecosystem. Our usage is small (only in `overlay.go`).

### Desired State

All dependencies on latest stable releases. The four Charm libraries on v2 with `charm.land` import paths. Legacy muesli libraries replaced by `charmbracelet/x/ansi` equivalents. New v2 features adopted where they provide clear value (viewport soft-wrap, declarative view, etc.).

## Research Findings

### Target Versions

| Dependency | Current | Target | Import Path Change |
|---|---|---|---|
| bubbletea | v1.3.5 | v2.0.2 | `charm.land/bubbletea/v2` |
| bubbles | v0.21.0 | v2.0.0 | `charm.land/bubbles/v2/*` |
| lipgloss | v1.1.0 | v2.0.1 | `charm.land/lipgloss/v2` |
| glamour | v0.9.1 | v2.0.0 | `charm.land/glamour/v2` |
| charmbracelet/x/ansi | v0.8.0 | latest | Same path, version bump |
| bubblezone | v0.0.0-... | v2 module | `github.com/lrstanley/bubblezone/v2` |
| chroma/v2 | v2.15.0 | v2.23.1 | Same path |
| html-to-markdown | v1.6.0 | v2.5.0 | `github.com/JohannesKaufmann/html-to-markdown/v2` |
| go-udiff | v0.2.0 | v0.4.0 | Same path |
| muesli/reflow | v0.3.0 | Remove | Replaced by x/ansi |
| muesli/ansi | v0.0.0-... | Remove | Replaced by x/ansi |
| muesli/termenv | v0.16.0 | Remove direct use | Superseded by lipgloss v2 |

### Codebase Usage Analysis

Comprehensive analysis of all usages across the codebase:

**bubbletea** (30+ files): `tea.NewProgram` with `tea.WithAltScreen()` (root.go), `tea.WindowSizeMsg` (15+ components), `tea.KeyMsg` (15+ components, all using string-based `key.Matches` — no `tea.KeyType` constants), `tea.Batch` (everywhere), `tea.Quit`, `tea.ExecProcess` (editor.go), `tea.Tick` (status.go), `View() string` (every component). No usage of `tea.MouseMsg`, `tea.EnterAltScreen`/`tea.ExitAltScreen` commands, `tea.WithInputTTY`, `tea.WithANSICompressor`, `tea.Sequentially`, `p.Start()`.

**bubbles** (components used):
- `viewport` (5 files): `viewport.New(0, 0)`, direct `.Width`/`.Height` field access, `.KeyMap` reassignment, `.GotoBottom()`, `.GotoTop()`, `.SetContent()`, `.TotalLineCount()`, `.YOffset`
- `textarea` (2 files): `textarea.New()`, `textarea.Blink`, direct `.FocusedStyle.*` and `.BlurredStyle.*` field access, `.Prompt`, `.ShowLineNumbers`, `.CharLimit`
- `spinner` (2 files): `spinner.New()`, `spinner.Points`, `spinner.Dot`, `.Tick`, `.Style`
- `key` (20+ files): `key.NewBinding()`, `key.Matches()`, `key.WithKeys()`, `key.WithHelp()`
- `table` (2 files): `table.New()`, `table.DefaultStyles()`, `table.WithColumns()`, `.KeyMap.*`, `.Focus()`
- `textinput` (2 files): `textinput.New()`, `textinput.Blink`, direct `.PlaceholderStyle`, `.PromptStyle`, `.TextStyle`, `.Width` field access, `.Cursor.Blink`
- NOT used: `help`, `list`, `paginator`, `cursor`, `progress`, `timer`, `stopwatch`

**lipgloss** (25+ files): `lipgloss.AdaptiveColor` (pervasive in theme system — every theme color), `lipgloss.TerminalColor` (3 files as parameter type), `lipgloss.Color()` (2 files), `lipgloss.NewStyle()` (everywhere), `lipgloss.JoinVertical/Horizontal` (everywhere), `lipgloss.Place` + `lipgloss.WithWhitespaceBackground` (help.go), `lipgloss.HasDarkBackground()` (2 files — markdown.go, diff.go), `lipgloss.Height/Width` (everywhere). No usage of `lipgloss.Renderer`, `lipgloss.DefaultRenderer`, `lipgloss.NewRenderer`, `lipgloss.CompleteColor`.

**glamour** (1 file — markdown.go): `glamour.NewTermRenderer()` with `glamour.WithStyles(customStyleConfig)` and `glamour.WithWordWrap(width)`. Uses a fully custom `ansi.StyleConfig`. Does NOT use `WithAutoStyle` or `WithColorProfile`.

**charmbracelet/x/ansi** (4 files): `ansi.Truncate()` (chat.go, message.go, diff.go), `ansi.Cut()` (overlay.go).

**bubblezone** (1 file — root.go): `zone.NewGlobal()` only. No `zone.Mark()` or `zone.Scan()` calls anywhere. Effectively a no-op initialization.

**muesli/reflow** (1 file — overlay.go): `truncate.String()` only.

**muesli/ansi** (1 file — overlay.go): `ansi.PrintableRuneWidth()` (6 call sites).

**muesli/termenv** (1 file — overlay.go): `termenv.Style` struct field, never populated, effectively dead code.

**chroma/v2** (2 files): `lexers.Match/Analyse`, `formatters.Get("terminal16m")`, `chroma.MustNewXMLStyle`, `chroma.NewColour`, `styles.Registry` deletion (theme manager).

**html-to-markdown** (1 file — webfetch.go): `md.NewConverter("", true, nil)` → `.ConvertString(html)`.

**go-udiff** (1 file — diff.go): `udiff.Unified("a/"+fileName, "b/"+fileName, beforeContent, afterContent)`.

**go-colorful** (1 file — images.go): `colorful.MakeColor()` → `.Hex()` for image preview rendering.

### Breaking Changes Impact Assessment

**HIGH IMPACT — lipgloss.AdaptiveColor removal** (affects ALL theme files):
The entire theme system is built on `lipgloss.AdaptiveColor{Dark: "...", Light: "..."}`. Lipgloss v2 removes this type. Options:
- Use `compat.AdaptiveColor{Light: lipgloss.Color("..."), Dark: lipgloss.Color("...")}` (quick migration)
- Use `lipgloss.LightDark(isDark)(light, dark)` (recommended, explicit)
- Use `tea.RequestBackgroundColor` + `tea.BackgroundColorMsg` to detect dark/light at runtime

Since the TUI runs inside bubbletea, the recommended approach is: detect dark/light via `tea.BackgroundColorMsg` in `Init()`, store `isDark bool` in the app model, and thread it through the theme system. This is also how all bubbles v2 default styles work (they take `isDark bool`).

**HIGH IMPACT — View() string → View() tea.View** (affects EVERY component):
Every component's `View() string` method must change to `View() tea.View`. The main app model's View becomes the single place where `view.AltScreen`, `view.MouseMode`, etc. are set declaratively.

**HIGH IMPACT — tea.KeyMsg struct → tea.KeyPressMsg** (affects 15+ components):
All `case tea.KeyMsg:` must become `case tea.KeyPressMsg:`. Since the codebase uses `key.Matches()` exclusively (no `msg.Type` or `msg.Runes` access), this is a safe mechanical replacement. No space-bar (`case " ":`) issues since the codebase doesn't match on raw strings.

**MEDIUM IMPACT — viewport.New() signature change** (5 files):
`viewport.New(0, 0)` → `viewport.New()` (options pattern). All `.Width`/`.Height` field access → `.SetWidth()`/`.SetHeight()`/`.Width()`/`.Height()` methods. `.YOffset` → `.YOffset()`/`.SetYOffset()`.

**MEDIUM IMPACT — textarea style restructuring** (2 files):
`ta.FocusedStyle.Base` → `ta.Styles.Focused.Base`. `ta.BlurredStyle.*` → `ta.Styles.Blurred.*`. Type name `textarea.Style` → `textarea.StyleState`. `textarea.Blink` → check if it still exists or became a method.

**MEDIUM IMPACT — textinput style restructuring** (2 files):
`.PromptStyle` → `StyleState.Prompt` via `SetStyles()`. `.TextStyle` → `StyleState.Text`. `.PlaceholderStyle` → `StyleState.Placeholder`. `.Width` field → `.SetWidth()`. `textinput.Blink` → check equivalent.

**MEDIUM IMPACT — table.DefaultStyles() gains isDark parameter** (2 files):
`table.DefaultStyles()` → `table.DefaultStyles(isDark)`.

**LOW IMPACT — lipgloss.TerminalColor → color.Color** (3 files):
Replace `lipgloss.TerminalColor` parameter types with `color.Color` from `image/color`. The `lipgloss.Color()` function now returns `color.Color` instead of being a string type.

**LOW IMPACT — lipgloss.HasDarkBackground() signature change** (2 files):
`lipgloss.HasDarkBackground()` → `lipgloss.HasDarkBackground(os.Stdin, os.Stdout)`.

**LOW IMPACT — lipgloss.WithWhitespaceBackground removal** (1 file — help.go):
`lipgloss.WithWhitespaceBackground(c)` → `lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(c))`.

**LOW IMPACT — glamour.WithStyles still exists** (1 file):
`glamour.WithStyles(config)` and `glamour.WithWordWrap(w)` remain in v2. Import path change only. The `ansi.StyleConfig` type also moves to `charm.land/glamour/v2/ansi`.

**LOW IMPACT — html-to-markdown v2 API simplification** (1 file):
`md.NewConverter("", true, nil)` → `htmltomarkdown.ConvertString(input)` (one-liner).

**LOW IMPACT — go-udiff v0.4 API** (1 file):
`udiff.Unified(oldName, newName, old, new)` — check if the v0.4 signature changed (v0.2 added a context-lines parameter — check if our callsite needs updating). Need to verify current callsite matches v0.4.

**ZERO IMPACT — chroma v2.23** (same API):
Pure version bump. No API changes. Just new lexers and style fixes.

**ZERO IMPACT — bubblezone** (stub usage):
Only `zone.NewGlobal()` is called. The v2 module path is `github.com/lrstanley/bubblezone/v2` and requires charm.land deps. The init call likely stays the same or can be removed entirely since zone marking is not used.

### New Features Worth Adopting

**Declarative View (bubbletea v2)** — LOW EFFORT, HIGH VALUE:
Move `tea.WithAltScreen()` from `tea.NewProgram()` into the main model's `View()` as `view.AltScreen = true`. This is a required migration change that also opens the door to dynamic alt-screen toggling.

**Viewport soft-wrap (bubbles v2)** — LOW EFFORT, MEDIUM VALUE:
`vp.SoftWrap = true` on viewports that display long content (chat messages, log details, agent details). Currently long lines are truncated or break the layout.

**Viewport left gutter for line numbers (bubbles v2)** — LOW EFFORT, MEDIUM VALUE:
`vp.LeftGutterFunc` can render line numbers in diff views and permission dialogs. Currently line numbers are baked into content strings.

**Viewport highlighting (bubbles v2)** — MEDIUM EFFORT, MEDIUM VALUE:
`vp.SetHighlights()`, `vp.HighlightNext()`, `vp.HighlightPrevious()` for search-in-viewport functionality. Could enable Ctrl+F search in chat history and diff views.

**Viewport horizontal scrolling (bubbles v2)** — ZERO EFFORT, LOW VALUE:
Comes for free. Arrow keys now scroll horizontally in viewports. Useful for wide code blocks.

**lipgloss.LightDark dynamic theming (lipgloss v2)** — MEDIUM EFFORT, HIGH VALUE:
Replace the current `lipgloss.AdaptiveColor` pattern with explicit `isDark bool` threading. Use `tea.RequestBackgroundColor` to detect terminal background at startup. This makes the theme system explicit and correct (the current `HasDarkBackground()` calls are blocking I/O outside the event loop).

**lipgloss underline styles (lipgloss v2)** — LOW EFFORT, LOW VALUE:
`UnderlineStyle(lipgloss.UnderlineCurly)` and `UnderlineColor()` for visual emphasis. Could enhance diff rendering or error highlighting in LSP diagnostics.

**lipgloss Layer/Compositor (lipgloss v2)** — HIGH EFFORT, HIGH VALUE:
`lipgloss.NewLayer()`, `lipgloss.NewCompositor()` — proper layered rendering. Could replace the custom overlay code in `layout/overlay.go` (which manually implements string-level overlay placement with muesli/ansi). This would also eliminate the muesli/reflow, muesli/ansi, and muesli/termenv dependencies.

**glamour new options (glamour v2)** — LOW EFFORT, LOW VALUE:
`WithChromaFormatter()`, `WithTableWrap()`, `WithInlineTableLinks()` — potential rendering improvements.

**lipgloss color utilities (lipgloss v2)** — LOW EFFORT, LOW VALUE:
`lipgloss.Darken()`, `lipgloss.Lighten()`, `lipgloss.Alpha()`, `lipgloss.Complementary()`, `lipgloss.Blend1D()` — useful for theme generation. Named ANSI constants (`lipgloss.Red`, etc.).

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Dark/light detection | Use `tea.RequestBackgroundColor` + `BackgroundColorMsg` | Replaces blocking `HasDarkBackground()` calls. Correct approach per all v2 migration guides. Thread `isDark bool` through theme/styles. |
| AdaptiveColor migration | Replace with `lipgloss.LightDark(isDark)` pattern | Cleaner than `compat.AdaptiveColor`. Requires `isDark` to be available at theme construction time. |
| Theme interface change | Add `isDark bool` parameter to theme constructors | All theme files already define both Dark/Light values. Change `BaseTheme` field types from `lipgloss.AdaptiveColor` to resolved `color.Color`. |
| Overlay code | Evaluate lipgloss Compositor as replacement | Current overlay.go is complex custom code using muesli/* libs. If Compositor fits, it eliminates 3 dependencies. If not, migrate muesli calls to x/ansi. |
| html-to-markdown | Upgrade to v2 | One-liner API. v1 is dead. |
| bubblezone | Keep but evaluate removal | Only `zone.NewGlobal()` is called. No zone marking. Investigate if bubbletea v2's `View.OnMouse` replaces need. |
| Viewport features | Adopt soft-wrap and horizontal scroll | Zero/low effort. Soft-wrap is a boolean flag per viewport. |

## Implementation Plan

### Phase 1: Core Library Migration (lipgloss → bubbletea → bubbles)

Order matters. lipgloss v2 is a dependency of bubbletea v2 which is a dependency of bubbles v2.

- [ ] **1.1** Update `go.mod`: add `charm.land/lipgloss/v2`, `charm.land/bubbletea/v2`, `charm.land/bubbles/v2/*`, `charm.land/glamour/v2` dependencies
- [ ] **1.2** Migrate lipgloss imports and API changes across all files:
  - Replace import path `github.com/charmbracelet/lipgloss` → `charm.land/lipgloss/v2`
  - Add `"image/color"` import where `lipgloss.TerminalColor` is used
  - Replace `lipgloss.TerminalColor` → `color.Color` (3 files: message.go, diff.go, background.go)
  - Replace `lipgloss.WithWhitespaceBackground(c)` → `lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(c))` (1 file: help.go)
- [ ] **1.3** Migrate theme system away from `lipgloss.AdaptiveColor`:
  - Add `isDark bool` field to the app model (tui.go)
  - Add `tea.RequestBackgroundColor` to `Init()` Cmd
  - Handle `tea.BackgroundColorMsg` in main model's `Update()`, store `isDark`
  - Refactor `BaseTheme` struct: replace `lipgloss.AdaptiveColor` fields with a resolved color approach (e.g., store both variants, resolve at access time based on isDark)
  - Update all theme files (opencode.go, catppuccin.go, dracula.go, flexoki.go, gruvbox.go, monokai.go, onedark.go, tokyonight.go, tron.go)
  - Update `lipgloss.HasDarkBackground()` calls in markdown.go and diff.go to use the stored `isDark` value instead
- [ ] **1.4** Migrate bubbletea imports and API changes:
  - Replace import path `github.com/charmbracelet/bubbletea` → `charm.land/bubbletea/v2`
  - Change all `View() string` signatures to `View() tea.View` (every component)
  - Move `tea.WithAltScreen()` from `tea.NewProgram()` to `view.AltScreen = true` in main model's `View()`
  - Replace all `case tea.KeyMsg:` with `case tea.KeyPressMsg:` (15+ files)
  - Remove `tea.WithAltScreen()` from `tea.NewProgram()` call in root.go
- [ ] **1.5** Migrate bubbles imports and API changes:
  - Replace import paths `github.com/charmbracelet/bubbles/*` → `charm.land/bubbles/v2/*`
  - **viewport** (5 files): `viewport.New(0, 0)` → `viewport.New()`, `.Width = x` → `.SetWidth(x)`, `.Height = y` → `.SetHeight(y)`, `.YOffset` access → `.YOffset()` / `.SetYOffset()`
  - **textarea** (2 files): `.FocusedStyle.*` → `.Styles.Focused.*`, `.BlurredStyle.*` → `.Styles.Blurred.*`, verify `textarea.Blink` API
  - **textinput** (2 files): `.PromptStyle` → styles struct pattern, `.TextStyle` → styles struct, `.PlaceholderStyle` → styles struct, `.Width = n` → `.SetWidth(n)`, `textinput.Blink` → verify equivalent
  - **table** (2 files): `table.DefaultStyles()` → `table.DefaultStyles(isDark)`
  - **spinner** (2 files): verify `spinner.New()` and `.Tick` still work (likely no change)
  - **key** (20+ files): no API changes expected, just import path
- [ ] **1.6** Migrate glamour imports:
  - Replace `github.com/charmbracelet/glamour` → `charm.land/glamour/v2`
  - Replace `github.com/charmbracelet/glamour/ansi` → `charm.land/glamour/v2/ansi`
  - Verify `glamour.WithStyles()` and `glamour.WithWordWrap()` still work (they do per v2 API)
- [ ] **1.7** Migrate charmbracelet/x/ansi:
  - Update to latest version in go.mod (the import path stays the same)
  - Verify `ansi.Truncate()` and `ansi.Cut()` signatures haven't changed

### Phase 2: Non-Charm Dependency Updates

- [ ] **2.1** Upgrade html-to-markdown to v2:
  - Replace `github.com/JohannesKaufmann/html-to-markdown` → `github.com/JohannesKaufmann/html-to-markdown/v2`
  - In webfetch.go: replace `md.NewConverter("", true, nil)` + `converter.ConvertString(html)` with `htmltomarkdown.ConvertString(html)` (import as `htmltomarkdown`)
- [ ] **2.2** Upgrade go-udiff to v0.4.0:
  - Update version in go.mod
  - Verify `udiff.Unified()` signature (v0.2 added context lines parameter — check if our callsite needs updating)
- [ ] **2.3** Upgrade chroma to v2.23.1:
  - Update version in go.mod. No API changes expected.
- [ ] **2.4** Upgrade bubblezone:
  - Replace `github.com/lrstanley/bubblezone` → `github.com/lrstanley/bubblezone/v2`
  - Verify `zone.NewGlobal()` still exists and works
  - Consider removing entirely if bubbletea v2's `View.OnMouse` covers the use case

### Phase 3: Remove Legacy muesli Dependencies

- [ ] **3.1** Replace `muesli/ansi.PrintableRuneWidth()` in overlay.go:
  - Use `lipgloss.Width()` (already used everywhere else in the codebase) or `ansi.StringWidth()` from charmbracelet/x/ansi
- [ ] **3.2** Replace `muesli/reflow/truncate.String()` in overlay.go:
  - Use `ansi.Truncate()` from charmbracelet/x/ansi (already used in 3 other files)
- [ ] **3.3** Remove `muesli/termenv` usage in overlay.go:
  - The `termenv.Style` field is never populated. Remove the struct field and use lipgloss styling directly.
- [ ] **3.4** Evaluate replacing entire overlay.go with lipgloss Compositor:
  - If `lipgloss.NewCompositor()` / `lipgloss.NewLayer()` can handle the overlay placement logic, replace the custom implementation
  - This would eliminate all muesli/* dependencies in one shot

### Phase 4: Adopt New Features

- [ ] **4.1** Enable viewport soft-wrap where appropriate:
  - Set `vp.SoftWrap = true` on chat message viewports, log detail viewports, agent detail viewports
  - Test with long lines to verify behavior
- [ ] **4.2** Evaluate viewport left gutter for diff views:
  - Implement `LeftGutterFunc` for line numbers in diff rendering if viewport is used there
- [ ] **4.3** Evaluate viewport search highlighting:
  - `SetHighlights()`, `HighlightNext()`, `HighlightPrevious()` for search functionality in chat history
- [ ] **4.4** Add window title via declarative View:
  - Set `view.WindowTitle = "OpenCode - <session>"` in the main model's View()

### Phase 5: Cleanup

- [ ] **5.1** Remove old dependencies from go.mod:
  - `github.com/charmbracelet/bubbletea`
  - `github.com/charmbracelet/bubbles`
  - `github.com/charmbracelet/lipgloss`
  - `github.com/charmbracelet/glamour`
  - `github.com/JohannesKaufmann/html-to-markdown` (v1)
  - `github.com/muesli/reflow`
  - `github.com/muesli/ansi`
  - `github.com/muesli/termenv` (if no longer needed as indirect)
- [ ] **5.2** Run `go mod tidy`
- [ ] **5.3** Run full test suite: `make test`
- [ ] **5.4** Manual TUI testing: verify all dialogs, overlays, themes, markdown rendering, diff views, file picker, spinner, chat editor, session list, agent/log tables

## Edge Cases

### Theme detection race condition

1. App starts, `Init()` sends `tea.RequestBackgroundColor`
2. First `View()` is called before `BackgroundColorMsg` arrives
3. Theme colors must have a sensible default (assume dark) until the msg arrives
4. When msg arrives, all cached rendered content must be invalidated

### go-udiff v0.4 API compatibility

1. v0.2 `udiff.Unified()` takes 4 args: `(oldName, newName, old, new string)`
2. v0.3+ added a context-lines parameter
3. Need to check if our callsite compiles against v0.4 or needs a 5th argument
4. If it needs context lines, use `udiff.DefaultContextLines` constant

### Indirect dependency conflicts

1. Charm v2 libs pull in new transitive deps (`charmbracelet/colorprofile`, `charmbracelet/ultraviolet`, etc.)
2. These may conflict with existing indirect deps
3. Run `go mod tidy` early and resolve any version conflicts

### Overlay rendering after lipgloss v2

1. `overlay.go` uses raw `termenv.Style` and `muesli/ansi.PrintableRuneWidth`
2. lipgloss v2 may change how string width measurement works internally
3. If replacing with Compositor, test overlay positioning carefully with multi-byte chars and ANSI sequences

### Spinner in non-TUI context

1. `internal/format/spinner.go` creates its own `tea.NewProgram` with `tea.WithOutput(os.Stderr)`
2. This is separate from the main TUI program
3. Its `View() string` also needs to change to `View() tea.View`
4. It does NOT need alt-screen or mouse mode

## Open Questions

1. **Should we remove bubblezone entirely?**
   - Only `zone.NewGlobal()` is called with zero zone marking in the codebase
   - bubbletea v2 has `View.OnMouse` for declarative mouse handling
   - **Recommendation**: Remove it unless mouse click zones are planned soon. It's dead code.

2. **Should the theme system resolve colors eagerly or lazily?**
   - Eagerly: detect isDark once at startup, resolve all `AdaptiveColor` values to concrete `color.Color`, store in theme. Simpler but can't react to runtime background changes.
   - Lazily: keep both variants, resolve at render time via `LightDark(isDark)`. More flexible but requires threading `isDark` everywhere.
   - **Recommendation**: Eager resolution at startup + re-resolve on `BackgroundColorMsg`. The theme is already switched as a unit via the TUI, so full re-resolution on change is fine.

3. **Should we replace overlay.go with lipgloss Compositor?**
   - Compositor is new in v2 and may not cover all overlay use cases (shadow rendering, arbitrary positioning)
   - **Recommendation**: Investigate in Phase 3. If Compositor handles it, great. If not, just migrate muesli calls to x/ansi equivalents. Don't block the migration on this.

4. **Should we adopt viewport soft-wrap by default?**
   - Soft-wrap changes the visual behavior of all viewports
   - Some content (code blocks, diffs) should NOT soft-wrap
   - **Recommendation**: Enable selectively. Chat message viewports: yes. Diff views: no. Permission dialog: yes.

## Success Criteria

- [ ] All imports use `charm.land/*` paths for bubbletea, bubbles, lipgloss, glamour
- [ ] `go build ./...` succeeds with zero references to old import paths
- [ ] `make test` passes (all unit tests green)
- [ ] All 9 themes render correctly in both dark and light terminal backgrounds
- [ ] Markdown rendering in chat messages works with custom style config
- [ ] Diff rendering with syntax highlighting works
- [ ] All dialogs (quit, session, command, model, theme, init, arguments, filepicker, permission, help, complete) render and function correctly
- [ ] Overlay positioning works (dialogs centered over content)
- [ ] External editor launch via `tea.ExecProcess` works
- [ ] Spinner renders correctly (both TUI spinner and standalone stderr spinner)
- [ ] html-to-markdown conversion in webfetch tool works
- [ ] go-udiff diff generation works
- [ ] No `muesli/reflow`, `muesli/ansi`, or `muesli/termenv` in final go.mod (unless kept as indirect by other deps)
- [ ] `go mod tidy` produces clean go.mod/go.sum

## References

- `cmd/root.go` — tea.NewProgram, tea.WithAltScreen, zone.NewGlobal, program.Run/Send/Quit
- `internal/tui/tui.go` — main app model, View(), Update() with tea.KeyMsg/WindowSizeMsg
- `internal/tui/theme/*.go` — all theme definitions using lipgloss.AdaptiveColor
- `internal/tui/styles/styles.go` — style factories using lipgloss.NewStyle
- `internal/tui/styles/markdown.go` — glamour renderer with custom ansi.StyleConfig
- `internal/tui/styles/background.go` — getColorRGB using lipgloss.TerminalColor
- `internal/tui/layout/overlay.go` — muesli/ansi, muesli/reflow, muesli/termenv, charmbracelet/x/ansi
- `internal/tui/layout/split.go` — viewport-like split layout
- `internal/tui/components/chat/editor.go` — textarea, tea.ExecProcess
- `internal/tui/components/chat/list.go` — viewport, spinner
- `internal/tui/components/chat/message.go` — ansi.Truncate, lipgloss.TerminalColor
- `internal/tui/components/dialog/*.go` — all dialogs with tea.KeyMsg, viewport, textinput
- `internal/tui/components/agents/table.go` — table.DefaultStyles(), table.New()
- `internal/tui/components/logs/table.go` — same table patterns
- `internal/tui/image/images.go` — go-colorful, lipgloss.Color
- `internal/diff/diff.go` — chroma, ansi.Truncate, lipgloss.HasDarkBackground, go-udiff
- `internal/llm/tools/webfetch.go` — html-to-markdown
- `internal/format/spinner.go` — standalone tea.Program, spinner
