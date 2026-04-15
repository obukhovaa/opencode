package vim

import "testing"

func TestFindTextObject_Word(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		offset  int
		objType string
		isInner bool
		wantS   int
		wantE   int
	}{
		{"inner word", "hello world", 1, "w", true, 0, 5},
		{"around word", "hello world", 1, "w", false, 0, 6},
		{"inner word at end", "hello world", 7, "w", true, 6, 11},
		{"around word at end", "hello world", 7, "w", false, 5, 11},
		{"inner WORD", "foo.bar baz", 1, "W", true, 0, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindTextObject(tt.text, tt.offset, tt.objType, tt.isInner)
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.Start != tt.wantS || got.End != tt.wantE {
				t.Errorf("FindTextObject(%q, %d, %q, %v) = {%d, %d}, want {%d, %d}",
					tt.text, tt.offset, tt.objType, tt.isInner, got.Start, got.End, tt.wantS, tt.wantE)
			}
		})
	}
}

func TestFindTextObject_Quotes(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		offset  int
		objType string
		isInner bool
		wantS   int
		wantE   int
	}{
		{"inner double quote", `say "hello" world`, 6, "\"", true, 5, 10},
		{"around double quote", `say "hello" world`, 6, "\"", false, 4, 11},
		{"inner single quote", "say 'hi' world", 6, "'", true, 5, 7},
		{"around single quote", "say 'hi' world", 6, "'", false, 4, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindTextObject(tt.text, tt.offset, tt.objType, tt.isInner)
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.Start != tt.wantS || got.End != tt.wantE {
				t.Errorf("FindTextObject(%q, %d, %q, %v) = {%d, %d}, want {%d, %d}",
					tt.text, tt.offset, tt.objType, tt.isInner, got.Start, got.End, tt.wantS, tt.wantE)
			}
		})
	}
}

func TestFindTextObject_Brackets(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		offset  int
		objType string
		isInner bool
		wantS   int
		wantE   int
	}{
		{"inner parens", "foo(bar)baz", 5, "(", true, 4, 7},
		{"around parens", "foo(bar)baz", 5, "(", false, 3, 8},
		{"inner brackets", "foo[bar]baz", 5, "[", true, 4, 7},
		{"around brackets", "foo[bar]baz", 5, "[", false, 3, 8},
		{"inner braces", "foo{bar}baz", 5, "{", true, 4, 7},
		{"nested parens", "foo(a(b)c)d", 6, "(", true, 6, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindTextObject(tt.text, tt.offset, tt.objType, tt.isInner)
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.Start != tt.wantS || got.End != tt.wantE {
				t.Errorf("FindTextObject(%q, %d, %q, %v) = {%d, %d}, want {%d, %d}",
					tt.text, tt.offset, tt.objType, tt.isInner, got.Start, got.End, tt.wantS, tt.wantE)
			}
		})
	}
}

func TestFindTextObject_NotFound(t *testing.T) {
	got := FindTextObject("hello world", 3, "(", true)
	if got != nil {
		t.Errorf("expected nil for no matching brackets, got %+v", got)
	}
}
