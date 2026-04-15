package vim

import "testing"

func TestResolveMotion_H(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"move left", "hello", 3, 1, 2},
		{"at start", "hello", 0, 1, 0},
		{"count 2", "hello", 3, 2, 1},
		{"stop at line start", "ab\ncd", 3, 5, 3}, // 'd' at offset 3, can't cross newline
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("h", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(h, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_L(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"move right", "hello", 1, 1, 2},
		{"at end", "hello", 4, 1, 4},
		{"count 2", "hello", 1, 2, 3},
		{"stop at newline", "ab\ncd", 1, 5, 1}, // vim l stops before newline
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("l", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(l, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_J(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"move down", "abc\ndef", 1, 1, 5},                 // col 1 of line 0 -> col 1 of line 1
		{"clamp column", "abc\nd", 2, 1, 4},                // col 2 but line 1 has len 1, so col 0
		{"last line", "abc\ndef", 5, 1, 5},                 // already on last line
		{"move down from line start", "abc\ndef", 0, 1, 4}, // col 0 -> col 0
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("j", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(j, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_K(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"move up", "abc\ndef", 5, 1, 1},
		{"first line", "abc\ndef", 1, 1, 1},
		{"clamp column", "a\ndef", 3, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("k", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(k, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_W(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"next word", "hello world", 0, 1, 6},
		{"skip punctuation", "foo.bar baz", 0, 1, 3},
		{"at last word", "hello", 0, 1, 4},
		{"count 2", "one two three", 0, 2, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("w", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(w, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_B(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"prev word", "hello world", 6, 1, 0},
		{"at start", "hello world", 0, 1, 0},
		{"count 2", "one two three", 8, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("b", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(b, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_E(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		count  int
		want   int
	}{
		{"end of word", "hello world", 0, 1, 4},
		{"end of next word", "hello world", 4, 1, 10},
		{"already at end", "hi", 1, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMotion("e", tt.text, tt.offset, tt.count)
			if got != tt.want {
				t.Errorf("ResolveMotion(e, %q, %d, %d) = %d, want %d", tt.text, tt.offset, tt.count, got, tt.want)
			}
		})
	}
}

func TestResolveMotion_LinePositions(t *testing.T) {
	text := "  hello world"

	t.Run("0 start of line", func(t *testing.T) {
		got := ResolveMotion("0", text, 5, 1)
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("^ first non-blank", func(t *testing.T) {
		got := ResolveMotion("^", text, 0, 1)
		if got != 2 {
			t.Errorf("got %d, want 2", got)
		}
	})

	t.Run("$ end of line", func(t *testing.T) {
		got := ResolveMotion("$", text, 0, 1)
		if got != len(text)-1 {
			t.Errorf("got %d, want %d", got, len(text)-1)
		}
	})
}

func TestResolveMotion_BigWord(t *testing.T) {
	text := "foo.bar baz"

	t.Run("W skips non-whitespace", func(t *testing.T) {
		got := ResolveMotion("W", text, 0, 1)
		if got != 8 {
			t.Errorf("got %d, want 8", got)
		}
	})

	t.Run("B skips non-whitespace backwards", func(t *testing.T) {
		got := ResolveMotion("B", text, 8, 1)
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}

func TestFindCharacter(t *testing.T) {
	text := "hello world"
	tests := []struct {
		name     string
		offset   int
		char     string
		findType FindType
		count    int
		want     int
	}{
		{"f find forward", 0, "o", FindF, 1, 4},
		{"f find forward count 2", 0, "l", FindF, 2, 3},
		{"F find backward", 10, "o", FindB, 1, 7},
		{"t to forward", 0, "o", FindT, 1, 3},
		{"T to backward", 10, "o", FindR, 1, 8},
		{"not found", 0, "z", FindF, 1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findCharacter(text, tt.offset, tt.char, tt.findType, tt.count)
			if got != tt.want {
				t.Errorf("findCharacter(%q, %d, %q, %v, %d) = %d, want %d",
					text, tt.offset, tt.char, tt.findType, tt.count, got, tt.want)
			}
		})
	}
}

func TestOffsetToLineCol(t *testing.T) {
	text := "abc\ndef\nghi"
	tests := []struct {
		offset int
		line   int
		col    int
	}{
		{0, 0, 0},
		{2, 0, 2},
		{4, 1, 0},
		{6, 1, 2},
		{8, 2, 0},
	}
	for _, tt := range tests {
		line, col := offsetToLineCol(text, tt.offset)
		if line != tt.line || col != tt.col {
			t.Errorf("offsetToLineCol(%q, %d) = (%d, %d), want (%d, %d)",
				text, tt.offset, line, col, tt.line, tt.col)
		}
	}
}

func TestLineColToOffset(t *testing.T) {
	text := "abc\ndef\nghi"
	tests := []struct {
		line   int
		col    int
		offset int
	}{
		{0, 0, 0},
		{0, 2, 2},
		{1, 0, 4},
		{1, 2, 6},
		{2, 0, 8},
	}
	for _, tt := range tests {
		got := lineColToOffset(text, tt.line, tt.col)
		if got != tt.offset {
			t.Errorf("lineColToOffset(%q, %d, %d) = %d, want %d",
				text, tt.line, tt.col, got, tt.offset)
		}
	}
}
