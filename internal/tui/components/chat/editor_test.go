package chat

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

// newTestEditor constructs an editorCmp suitable for unit testing. It bypasses
// NewEditorCmp to avoid needing an initialized *app.App and config. The theme
// package is imported transitively via editor.go and its init() functions register
// all built-in themes, so CreateTextArea works without further setup.
func newTestEditor() *editorCmp {
	ta := CreateTextArea(nil)
	return &editorCmp{
		textarea: ta,
		mode:     modeNormal,
	}
}

// TestEditorCmpNoOverflow asserts that the rendered view width never exceeds the
// container width set by SetSize. For practical widths (>= the component's physical
// minimum render width of 4 = 2-col prompt + 2-col textarea minimum), the full
// no-overflow invariant <= w is asserted. For widths 1–3, no implementation can
// satisfy <= w because the prompt and textarea each have fixed minimums; those
// subtests verify only that no panic occurs.
func TestEditorCmpNoOverflow(t *testing.T) {
	widths := []int{1, 2, 3, 5, 20, 40, 80, 120}
	modes := []editorMode{modeNormal, modeShell}

	for _, w := range widths {
		for _, mode := range modes {
			w, mode := w, mode
			t.Run(fmt.Sprintf("w=%d/mode=%s", w, mode), func(t *testing.T) {
				ed := newTestEditor()
				ed.mode = mode
				ed.SetSize(w, 10) //nolint:errcheck

				view := ed.View()
				got := lipgloss.Width(view.Content)
				// minRender: the smallest achievable combined width.
				// promptColumnWidth()==2 plus textarea minimum (internal " " prompt +
				// 1 content col = 2) gives a floor of 4 that no correct implementation
				// can beat. Widths < 4 are degenerate; for those we only require
				// no-panic and that the rendered width doesn't exceed what a correct
				// implementation would produce (the floor), preventing regression.
				minRender := ed.promptColumnWidth() + 2
				bound := max(w, minRender)
				if got > bound {
					t.Errorf("View width %d exceeds bound %d (container=%d)", got, bound, w)
				}
			})
		}
	}
}

// TestEditorCmpNoOverflowWithAttachment asserts the no-overflow invariant when an
// attachment is present. This tests both the textarea width path (promptColumnWidth
// reservation) and the height-sync path (syncTextareaHeight called from Update).
// The assertion is checked at widths where both the editor row and the attachment
// badge fit within the container; at smaller widths the attachment badge itself
// (fixed size) would exceed the container, which is a pre-existing display limitation
// unrelated to the textarea overflow fix and outside the scope of this change.
func TestEditorCmpNoOverflowWithAttachment(t *testing.T) {
	// Only assert the strict invariant at widths where the attachment badge fits.
	// The badge for a short filename is approximately 12 cols; we test from 20 up.
	widths := []int{20, 40, 80, 120}
	modes := []editorMode{modeNormal, modeShell}

	for _, w := range widths {
		for _, mode := range modes {
			w, mode := w, mode
			t.Run(fmt.Sprintf("w=%d/mode=%s/attachment", w, mode), func(t *testing.T) {
				ed := newTestEditor()
				ed.mode = mode
				ed.SetSize(w, 10) //nolint:errcheck

				// Add an attachment via Update so syncTextareaHeight is called.
				model, _ := ed.Update(dialog.AttachmentAddedMsg{
					Attachment: message.Attachment{FileName: "test.png"},
				})
				ed = model.(*editorCmp)

				view := ed.View()
				got := lipgloss.Width(view.Content)
				if got > w {
					t.Errorf("View width %d exceeds container width %d (with attachment)", got, w)
				}
			})
		}
	}
}

// TestEditorCmpNoPanic asserts that SetSize and View do not panic at the degenerate
// width of 1, which exercises the max(0, 1-3)==0 clamping path.
func TestEditorCmpNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic at width=1: %v", r)
		}
	}()

	ed := newTestEditor()
	ed.SetSize(1, 10) //nolint:errcheck
	ed.View()         // must not panic
}

// TestEditorCmpHeightSync asserts that textarea height is updated in Update (not in
// View) in response to attachment add/remove messages, and that removal restores the
// full height without requiring a second SetSize call.
func TestEditorCmpHeightSync(t *testing.T) {
	ed := newTestEditor()
	ed.SetSize(80, 10) //nolint:errcheck

	// No attachments: textarea height must equal m.height (10).
	if got := ed.textarea.Height(); got != 10 {
		t.Fatalf("after SetSize(80,10) with no attachments: want Height()==10, got %d", got)
	}

	// Add attachment via Update: height must shrink to m.height-1 (9).
	model, _ := ed.Update(dialog.AttachmentAddedMsg{
		Attachment: message.Attachment{FileName: "test.png"},
	})
	ed = model.(*editorCmp)

	if got := ed.textarea.Height(); got != 9 {
		t.Fatalf("after AttachmentAddedMsg: want Height()==9, got %d", got)
	}

	// Remove the attachment. We access internal fields directly (same package) to
	// simulate the net effect of any removal Update branch and call syncTextareaHeight,
	// verifying that height is restored without a second SetSize call.
	ed.attachments = nil
	ed.syncTextareaHeight()

	if got := ed.textarea.Height(); got != 10 {
		t.Fatalf("after attachment removed (no second SetSize): want Height()==10, got %d", got)
	}
}

// TestEditorCmpLineWrap asserts that no rendered line of the view exceeds the
// container width when a string of exactly w printable ASCII characters is typed.
func TestEditorCmpLineWrap(t *testing.T) {
	widths := []int{20, 40, 80}

	for _, w := range widths {
		w := w
		t.Run(fmt.Sprintf("w=%d", w), func(t *testing.T) {
			ed := newTestEditor()
			ed.SetSize(w, 10) //nolint:errcheck

			// Fill the textarea with exactly w ASCII characters.
			ed.textarea.SetValue(strings.Repeat("a", w))

			view := ed.View()
			lines := strings.Split(view.Content, "\n")
			for i, line := range lines {
				lw := lipgloss.Width(line)
				if lw > w {
					t.Errorf("line %d width %d exceeds container width %d: %q", i, lw, w, line)
				}
			}
		})
	}
}
