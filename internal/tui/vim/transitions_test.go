package vim

import "testing"

// helper to create a TransitionContext with basic state tracking
func newTestCtx(text string, offset int) (*TransitionContext, *string, *int) {
	currentText := text
	currentOffset := offset
	insertCalled := false
	_ = insertCalled

	ctx := &TransitionContext{
		OperatorContext: OperatorContext{
			Text:         text,
			Offset:       offset,
			SetText:      func(t string) { currentText = t },
			SetOffset:    func(o int) { currentOffset = o },
			EnterInsert:  func(o int) { currentOffset = o; insertCalled = true },
			GetRegister:  func() (string, bool) { return "", false },
			SetRegister:  func(string, bool) {},
			GetLastFind:  func() *FindRecord { return nil },
			SetLastFind:  func(FindType, string) {},
			RecordChange: func(RecordedChange) {},
		},
	}

	return ctx, &currentText, &currentOffset
}

func TestTransition_IdleToCount(t *testing.T) {
	ctx, _, _ := newTestCtx("hello", 0)
	result := Transition(CommandIdle{}, "3", ctx)

	if _, ok := result.Next.(CommandCount); !ok {
		t.Errorf("expected CommandCount, got %T", result.Next)
	}
	if result.Next.(CommandCount).Digits != "3" {
		t.Errorf("expected digits '3', got %q", result.Next.(CommandCount).Digits)
	}
}

func TestTransition_IdleToOperator(t *testing.T) {
	ctx, _, _ := newTestCtx("hello", 0)
	result := Transition(CommandIdle{}, "d", ctx)

	if op, ok := result.Next.(CommandOperator); !ok {
		t.Errorf("expected CommandOperator, got %T", result.Next)
	} else if op.Op != OpDelete {
		t.Errorf("expected OpDelete, got %v", op.Op)
	}
}

func TestTransition_IdleMotion(t *testing.T) {
	ctx, _, offset := newTestCtx("hello world", 0)
	result := Transition(CommandIdle{}, "w", ctx)

	if result.Execute == nil {
		t.Fatal("expected execute function")
	}
	result.Execute()
	if *offset != 6 {
		t.Errorf("expected offset 6, got %d", *offset)
	}
}

func TestTransition_CountThenMotion(t *testing.T) {
	ctx, _, offset := newTestCtx("one two three four", 0)

	// First press "2"
	result := Transition(CommandIdle{}, "2", ctx)
	state := result.Next

	// Then press "w"
	result = Transition(state, "w", ctx)
	if result.Execute == nil {
		t.Fatal("expected execute function")
	}
	result.Execute()
	if *offset != 8 {
		t.Errorf("expected offset 8, got %d", *offset)
	}
}

func TestTransition_OperatorDoubleKey(t *testing.T) {
	ctx, text, _ := newTestCtx("line1\nline2\nline3", 0)
	result := Transition(CommandOperator{Op: OpDelete, Count: 1}, "d", ctx)

	if result.Execute == nil {
		t.Fatal("expected execute function for dd")
	}
	result.Execute()
	if *text == "line1\nline2\nline3" {
		t.Error("text should have been modified by dd")
	}
}

func TestTransition_IdleFind(t *testing.T) {
	ctx, _, _ := newTestCtx("hello", 0)
	result := Transition(CommandIdle{}, "f", ctx)

	if _, ok := result.Next.(CommandFind); !ok {
		t.Errorf("expected CommandFind, got %T", result.Next)
	}
}

func TestTransition_FindChar(t *testing.T) {
	ctx, _, offset := newTestCtx("hello world", 0)
	result := Transition(CommandFind{Find: FindF, Count: 1}, "o", ctx)

	if result.Execute == nil {
		t.Fatal("expected execute function")
	}
	result.Execute()
	if *offset != 4 {
		t.Errorf("expected offset 4 (for 'o' in 'hello'), got %d", *offset)
	}
}

func TestTransition_IdleG(t *testing.T) {
	ctx, _, _ := newTestCtx("hello", 0)
	result := Transition(CommandIdle{}, "g", ctx)

	if _, ok := result.Next.(CommandG); !ok {
		t.Errorf("expected CommandG, got %T", result.Next)
	}
}

func TestTransition_GG(t *testing.T) {
	ctx, _, offset := newTestCtx("line1\nline2\nline3", 10)
	result := Transition(CommandG{Count: 1}, "g", ctx)

	if result.Execute == nil {
		t.Fatal("expected execute function for gg")
	}
	result.Execute()
	if *offset != 0 {
		t.Errorf("expected offset 0 for gg, got %d", *offset)
	}
}

func TestTransition_IdleReplace(t *testing.T) {
	ctx, _, _ := newTestCtx("hello", 0)
	result := Transition(CommandIdle{}, "r", ctx)

	if _, ok := result.Next.(CommandReplace); !ok {
		t.Errorf("expected CommandReplace, got %T", result.Next)
	}
}

func TestTransition_ReplaceChar(t *testing.T) {
	ctx, text, _ := newTestCtx("hello", 0)
	result := Transition(CommandReplace{Count: 1}, "x", ctx)

	if result.Execute == nil {
		t.Fatal("expected execute function")
	}
	result.Execute()
	if *text != "xello" {
		t.Errorf("expected 'xello', got %q", *text)
	}
}

func TestTransition_IndentDouble(t *testing.T) {
	ctx, text, _ := newTestCtx("hello", 0)
	result := Transition(CommandIndent{Dir: '>', Count: 1}, ">", ctx)

	if result.Execute == nil {
		t.Fatal("expected execute function for >>")
	}
	result.Execute()
	if *text != "  hello" {
		t.Errorf("expected '  hello', got %q", *text)
	}
}

func TestTransition_IdleInsertCommands(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"i"}, {"I"}, {"a"}, {"A"}, {"o"}, {"O"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ctx, _, _ := newTestCtx("hello", 2)
			result := Transition(CommandIdle{}, tt.input, ctx)
			if result.Execute == nil {
				t.Errorf("expected execute function for %q", tt.input)
			}
		})
	}
}

func TestTransition_OperatorTextObj(t *testing.T) {
	ctx, _, _ := newTestCtx("hello world", 0)

	// d -> i (operator then text obj scope)
	result := Transition(CommandOperator{Op: OpDelete, Count: 1}, "i", ctx)
	if _, ok := result.Next.(CommandOperatorTextObj); !ok {
		t.Errorf("expected CommandOperatorTextObj, got %T", result.Next)
	}

	// Then "w" to select inner word
	ctx2, text, _ := newTestCtx("hello world", 1)
	result = Transition(CommandOperatorTextObj{Op: OpDelete, Count: 1, Scope: ScopeInner}, "w", ctx2)
	if result.Execute == nil {
		t.Fatal("expected execute function for diw")
	}
	result.Execute()
	if *text != " world" {
		t.Errorf("expected ' world', got %q", *text)
	}
}

func TestTransition_OperatorFind(t *testing.T) {
	ctx, _, _ := newTestCtx("hello world", 0)

	// d -> f
	result := Transition(CommandOperator{Op: OpDelete, Count: 1}, "f", ctx)
	if _, ok := result.Next.(CommandOperatorFind); !ok {
		t.Errorf("expected CommandOperatorFind, got %T", result.Next)
	}

	// Then "o" to delete to 'o'
	ctx2, text, _ := newTestCtx("hello world", 0)
	result = Transition(CommandOperatorFind{Op: OpDelete, Count: 1, Find: FindF}, "o", ctx2)
	if result.Execute == nil {
		t.Fatal("expected execute function for dfo")
	}
	result.Execute()
	if *text != " world" {
		t.Errorf("expected ' world', got %q", *text)
	}
}

func TestTransition_Paste(t *testing.T) {
	register := "xyz"
	ctx := &TransitionContext{
		OperatorContext: OperatorContext{
			Text:         "hello",
			Offset:       2,
			SetText:      func(string) {},
			SetOffset:    func(int) {},
			EnterInsert:  func(int) {},
			GetRegister:  func() (string, bool) { return register, false },
			SetRegister:  func(string, bool) {},
			GetLastFind:  func() *FindRecord { return nil },
			SetLastFind:  func(FindType, string) {},
			RecordChange: func(RecordedChange) {},
		},
	}

	result := Transition(CommandIdle{}, "p", ctx)
	if result.Execute == nil {
		t.Fatal("expected execute function for p")
	}
}

func TestTransition_Undo(t *testing.T) {
	undoCalled := false
	ctx := &TransitionContext{
		OperatorContext: OperatorContext{
			Text:         "hello",
			Offset:       0,
			SetText:      func(string) {},
			SetOffset:    func(int) {},
			EnterInsert:  func(int) {},
			GetRegister:  func() (string, bool) { return "", false },
			SetRegister:  func(string, bool) {},
			GetLastFind:  func() *FindRecord { return nil },
			SetLastFind:  func(FindType, string) {},
			RecordChange: func(RecordedChange) {},
		},
		OnUndo: func() { undoCalled = true },
	}

	result := Transition(CommandIdle{}, "u", ctx)
	if result.Execute == nil {
		t.Fatal("expected execute function for u")
	}
	result.Execute()
	if !undoCalled {
		t.Error("undo callback not called")
	}
}
