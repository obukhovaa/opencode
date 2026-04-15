package vim

import "testing"

func newTestOpCtx(text string, offset int) (*OperatorContext, *string, *int) {
	currentText := text
	currentOffset := offset

	ctx := &OperatorContext{
		Text:         text,
		Offset:       offset,
		SetText:      func(t string) { currentText = t },
		SetOffset:    func(o int) { currentOffset = o },
		EnterInsert:  func(o int) { currentOffset = o },
		GetRegister:  func() (string, bool) { return "", false },
		SetRegister:  func(string, bool) {},
		GetLastFind:  func() *FindRecord { return nil },
		SetLastFind:  func(FindType, string) {},
		RecordChange: func(RecordedChange) {},
	}
	return ctx, &currentText, &currentOffset
}

func TestExecuteX(t *testing.T) {
	ctx, text, offset := newTestOpCtx("hello", 0)
	ExecuteX(1, ctx)

	if *text != "ello" {
		t.Errorf("expected 'ello', got %q", *text)
	}
	if *offset != 0 {
		t.Errorf("expected offset 0, got %d", *offset)
	}
}

func TestExecuteX_Count(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello", 0)
	ExecuteX(3, ctx)

	if *text != "lo" {
		t.Errorf("expected 'lo', got %q", *text)
	}
}

func TestExecuteX_AtEnd(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello", 5)
	ExecuteX(1, ctx)

	if *text != "hello" {
		t.Errorf("expected 'hello' unchanged, got %q", *text)
	}
}

func TestExecuteReplace(t *testing.T) {
	ctx, text, offset := newTestOpCtx("hello", 0)
	ExecuteReplace("x", 1, ctx)

	if *text != "xello" {
		t.Errorf("expected 'xello', got %q", *text)
	}
	if *offset != 0 {
		t.Errorf("expected offset 0, got %d", *offset)
	}
}

func TestExecuteReplace_Count(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello", 0)
	ExecuteReplace("x", 3, ctx)

	if *text != "xxxlo" {
		t.Errorf("expected 'xxxlo', got %q", *text)
	}
}

func TestExecuteToggleCase(t *testing.T) {
	ctx, text, _ := newTestOpCtx("Hello", 0)
	ExecuteToggleCase(3, ctx)

	if *text != "hELlo" {
		t.Errorf("expected 'hELlo', got %q", *text)
	}
}

func TestExecuteJoin(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello\nworld", 0)
	ExecuteJoin(1, ctx)

	if *text != "hello world" {
		t.Errorf("expected 'hello world', got %q", *text)
	}
}

func TestExecuteJoin_LastLine(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello", 0)
	ExecuteJoin(1, ctx)

	if *text != "hello" {
		t.Errorf("expected 'hello' unchanged, got %q", *text)
	}
}

func TestExecuteOperatorMotion_Delete(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello world", 0)
	ExecuteOperatorMotion(OpDelete, "w", 1, ctx)

	if *text != "world" {
		t.Errorf("expected 'world', got %q", *text)
	}
}

func TestExecuteOperatorMotion_Change(t *testing.T) {
	insertOffset := -1
	ctx := &OperatorContext{
		Text:         "hello world",
		Offset:       0,
		SetText:      func(string) {},
		SetOffset:    func(int) {},
		EnterInsert:  func(o int) { insertOffset = o },
		GetRegister:  func() (string, bool) { return "", false },
		SetRegister:  func(string, bool) {},
		GetLastFind:  func() *FindRecord { return nil },
		SetLastFind:  func(FindType, string) {},
		RecordChange: func(RecordedChange) {},
	}
	ExecuteOperatorMotion(OpChange, "w", 1, ctx)

	if insertOffset == -1 {
		t.Error("expected enter insert to be called")
	}
}

func TestExecuteOperatorMotion_Yank(t *testing.T) {
	var yanked string
	var linewise bool
	ctx := &OperatorContext{
		Text:         "hello world",
		Offset:       0,
		SetText:      func(string) {},
		SetOffset:    func(int) {},
		EnterInsert:  func(int) {},
		GetRegister:  func() (string, bool) { return "", false },
		SetRegister:  func(s string, lw bool) { yanked = s; linewise = lw },
		GetLastFind:  func() *FindRecord { return nil },
		SetLastFind:  func(FindType, string) {},
		RecordChange: func(RecordedChange) {},
	}
	ExecuteOperatorMotion(OpYank, "w", 1, ctx)

	if yanked != "hello " {
		t.Errorf("expected 'hello ' yanked, got %q", yanked)
	}
	if linewise {
		t.Error("expected non-linewise yank")
	}
}

func TestExecuteLineOp_Delete(t *testing.T) {
	ctx, text, _ := newTestOpCtx("line1\nline2\nline3", 0)
	ExecuteLineOp(OpDelete, 1, ctx)

	if *text != "line2\nline3" {
		t.Errorf("expected 'line2\\nline3', got %q", *text)
	}
}

func TestExecuteLineOp_Yank(t *testing.T) {
	var yanked string
	ctx := &OperatorContext{
		Text:         "line1\nline2\nline3",
		Offset:       0,
		SetText:      func(string) {},
		SetOffset:    func(int) {},
		EnterInsert:  func(int) {},
		GetRegister:  func() (string, bool) { return "", false },
		SetRegister:  func(s string, _ bool) { yanked = s },
		GetLastFind:  func() *FindRecord { return nil },
		SetLastFind:  func(FindType, string) {},
		RecordChange: func(RecordedChange) {},
	}
	ExecuteLineOp(OpYank, 1, ctx)

	if yanked != "line1\n" {
		t.Errorf("expected 'line1\\n' yanked, got %q", yanked)
	}
}

func TestExecuteIndent(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello", 0)
	ExecuteIndent('>', 1, ctx)

	if *text != "  hello" {
		t.Errorf("expected '  hello', got %q", *text)
	}
}

func TestExecuteIndent_Unindent(t *testing.T) {
	ctx, text, _ := newTestOpCtx("  hello", 0)
	ExecuteIndent('<', 1, ctx)

	if *text != "hello" {
		t.Errorf("expected 'hello', got %q", *text)
	}
}

func TestExecuteOpenLine_Below(t *testing.T) {
	ctx, text, _ := newTestOpCtx("line1\nline2", 0)
	ExecuteOpenLine("below", ctx)

	if *text != "line1\n\nline2" {
		t.Errorf("expected 'line1\\n\\nline2', got %q", *text)
	}
}

func TestExecuteOpenLine_Above(t *testing.T) {
	ctx, text, _ := newTestOpCtx("line1\nline2", 6)
	ExecuteOpenLine("above", ctx)

	if *text != "line1\n\nline2" {
		t.Errorf("expected 'line1\\n\\nline2', got %q", *text)
	}
}

func TestExecutePaste_CharacterWise(t *testing.T) {
	ctx := &OperatorContext{
		Text:         "hello",
		Offset:       2,
		SetText:      func(string) {},
		SetOffset:    func(int) {},
		EnterInsert:  func(int) {},
		GetRegister:  func() (string, bool) { return "xyz", false },
		SetRegister:  func(string, bool) {},
		GetLastFind:  func() *FindRecord { return nil },
		SetLastFind:  func(FindType, string) {},
		RecordChange: func(RecordedChange) {},
	}

	var newText string
	ctx.SetText = func(t string) { newText = t }

	ExecutePaste(true, 1, ctx)

	if newText != "helxyzlo" {
		t.Errorf("expected 'helxyzlo', got %q", newText)
	}
}

func TestExecuteOperatorTextObj(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello world", 1)
	ExecuteOperatorTextObj(OpDelete, ScopeInner, "w", 1, ctx)

	if *text != " world" {
		t.Errorf("expected ' world', got %q", *text)
	}
}

func TestExecuteOperatorFind_Delete(t *testing.T) {
	ctx, text, _ := newTestOpCtx("hello world", 0)
	ExecuteOperatorFind(OpDelete, FindF, "o", 1, ctx)

	if *text != " world" {
		t.Errorf("expected ' world', got %q", *text)
	}
}
