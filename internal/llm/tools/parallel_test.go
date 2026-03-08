package tools

import (
	"encoding/json"
	"testing"
)

func TestIsMutatingTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"edit is mutating", EditToolName, true},
		{"write is mutating", WriteToolName, true},
		{"multiedit is mutating", MultiEditToolName, true},
		{"delete is mutating", DeleteToolName, true},
		{"patch is mutating", PatchToolName, true},
		{"read is not mutating", ReadToolName, false},
		{"glob is not mutating", GlobToolName, false},
		{"bash is not mutating", BashToolName, false},
		{"grep is not mutating", GrepToolName, false},
		{"unknown is not mutating", "unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMutatingTool(tt.toolName); got != tt.want {
				t.Errorf("IsMutatingTool(%q) = %v, want %v", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestExtractPathsFromCall(t *testing.T) {
	tests := []struct {
		name  string
		call  ToolCall
		paths []string
	}{
		{
			"edit with file_path",
			ToolCall{ID: "1", Name: EditToolName, Input: `{"file_path":"/a/b.go","old_string":"x","new_string":"y"}`},
			[]string{"/a/b.go"},
		},
		{
			"delete with path",
			ToolCall{ID: "2", Name: DeleteToolName, Input: `{"path":"/a/b.go"}`},
			[]string{"/a/b.go"},
		},
		{
			"write with file_path",
			ToolCall{ID: "3", Name: WriteToolName, Input: `{"file_path":"/c/d.go","content":"hello"}`},
			[]string{"/c/d.go"},
		},
		{
			"read tool has no paths",
			ToolCall{ID: "4", Name: ReadToolName, Input: `{"file_path":"/e/f.go"}`},
			[]string{"/e/f.go"},
		},
		{
			"invalid JSON",
			ToolCall{ID: "5", Name: EditToolName, Input: `{invalid}`},
			nil,
		},
		{
			"empty input",
			ToolCall{ID: "6", Name: EditToolName, Input: `{}`},
			nil,
		},
		{
			"patch extracts files from patch_text",
			ToolCall{ID: "7", Name: PatchToolName, Input: `{"patch_text":"*** Begin Patch\n*** Update File: /a.go\n@@ func foo()\n-old\n+new\n*** Add File: /b.go\n+content\n*** End Patch"}`},
			[]string{"/a.go", "/b.go"},
		},
		{
			"patch with invalid JSON",
			ToolCall{ID: "8", Name: PatchToolName, Input: `{invalid}`},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractPathsFromCall(tt.call)
			if len(got) != len(tt.paths) {
				t.Errorf("ExtractPathsFromCall() = %v, want %v", got, tt.paths)
				return
			}
			gotSet := make(map[string]bool, len(got))
			for _, p := range got {
				gotSet[p] = true
			}
			for _, p := range tt.paths {
				if !gotSet[p] {
					t.Errorf("ExtractPathsFromCall() missing path %q, got %v", p, got)
				}
			}
		})
	}
}

func TestIsSafeReadOnlyCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"git status", true},
		{"git diff", true},
		{"git log --oneline", true},
		{"go test ./...", true},
		{"go build ./...", true},
		{"go vet ./...", true},
		{"ls -la", true},
		{"pwd", true},
		{"echo hello", true},
		{"rm -rf /", false},
		{"curl http://example.com", false},
		{"wget http://example.com", false},
		{"npm install", false},
		{"docker run", false},
		{"git push", false},
		{"git commit", false},
		{"git checkout", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := IsSafeReadOnlyCommand(tt.command); got != tt.want {
				t.Errorf("IsSafeReadOnlyCommand(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestReadOnlyToolsAllowParallelism(t *testing.T) {
	call := ToolCall{ID: "1", Name: "test", Input: "{}"}
	allCalls := []ToolCall{call}

	readOnlyTools := []BaseTool{
		&globTool{},
		&grepTool{},
		&viewTool{},
		&lsTool{},
		&viewImageTool{},
		&fetchTool{},
		&sourcegraphTool{},
	}

	for _, tool := range readOnlyTools {
		t.Run(tool.Info().Name, func(t *testing.T) {
			if !tool.AllowParallelism(call, allCalls) {
				t.Errorf("%s.AllowParallelism() = false, want true", tool.Info().Name)
			}
		})
	}
}

func TestStructOutputNeverParallel(t *testing.T) {
	tool := &structOutputTool{
		schema:       map[string]any{},
		structParams: map[string]any{},
		required:     []string{},
	}
	call := ToolCall{ID: "1", Name: StructOutputToolName, Input: "{}"}
	allCalls := []ToolCall{call}
	if tool.AllowParallelism(call, allCalls) {
		t.Error("structOutputTool.AllowParallelism() = true, want false")
	}
}

func TestBashAllowParallelism(t *testing.T) {
	tool := &bashTool{}
	tests := []struct {
		command string
		want    bool
	}{
		{"git status", true},
		{"go test ./...", true},
		{"go build ./cmd/...", true},
		{"ls", true},
		{"rm -rf /tmp/foo", false},
		{"curl http://example.com", false},
		{"npm install", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			input, _ := json.Marshal(BashParams{Command: tt.command, Description: "test"})
			call := ToolCall{ID: "1", Name: BashToolName, Input: string(input)}
			if got := tool.AllowParallelism(call, []ToolCall{call}); got != tt.want {
				t.Errorf("bashTool.AllowParallelism(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}

	t.Run("invalid JSON returns false", func(t *testing.T) {
		call := ToolCall{ID: "1", Name: BashToolName, Input: `{invalid}`}
		if tool.AllowParallelism(call, []ToolCall{call}) {
			t.Error("bashTool.AllowParallelism(invalid JSON) = true, want false")
		}
	})
}

func TestEditAllowParallelism(t *testing.T) {
	tool := &editTool{}

	t.Run("no conflict with different files", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"x","new_string":"y"}`}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/b.go","old_string":"x","new_string":"y"}`}
		allCalls := []ToolCall{call1, call2}
		if !tool.AllowParallelism(call1, allCalls) {
			t.Error("editTool should allow parallel with different files")
		}
	})

	t.Run("conflict with same file", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"x","new_string":"y"}`}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"a","new_string":"b"}`}
		allCalls := []ToolCall{call1, call2}
		if tool.AllowParallelism(call1, allCalls) {
			t.Error("editTool should not allow parallel with same file")
		}
	})

	t.Run("no conflict with read-only tools", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"x","new_string":"y"}`}
		call2 := ToolCall{ID: "2", Name: ReadToolName, Input: `{"file_path":"/a.go"}`}
		allCalls := []ToolCall{call1, call2}
		if !tool.AllowParallelism(call1, allCalls) {
			t.Error("editTool should allow parallel with read-only tools on same file")
		}
	})

	t.Run("conflict between edit and delete on same file", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"x","new_string":"y"}`}
		call2 := ToolCall{ID: "2", Name: DeleteToolName, Input: `{"path":"/a.go"}`}
		allCalls := []ToolCall{call1, call2}
		if tool.AllowParallelism(call1, allCalls) {
			t.Error("editTool should not allow parallel with delete on same file")
		}
	})

	t.Run("invalid JSON returns false", func(t *testing.T) {
		call := ToolCall{ID: "1", Name: EditToolName, Input: `{invalid}`}
		if tool.AllowParallelism(call, []ToolCall{call}) {
			t.Error("editTool.AllowParallelism(invalid JSON) = true, want false")
		}
	})
}

func TestDeleteAllowParallelism(t *testing.T) {
	tool := &deleteTool{}

	t.Run("no conflict with different files", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: DeleteToolName, Input: `{"path":"/a.go"}`}
		call2 := ToolCall{ID: "2", Name: DeleteToolName, Input: `{"path":"/b.go"}`}
		allCalls := []ToolCall{call1, call2}
		if !tool.AllowParallelism(call1, allCalls) {
			t.Error("deleteTool should allow parallel with different files")
		}
	})

	t.Run("conflict with write on same file", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: DeleteToolName, Input: `{"path":"/a.go"}`}
		call2 := ToolCall{ID: "2", Name: WriteToolName, Input: `{"file_path":"/a.go","content":"x"}`}
		allCalls := []ToolCall{call1, call2}
		if tool.AllowParallelism(call1, allCalls) {
			t.Error("deleteTool should not allow parallel with write on same file")
		}
	})
}

func TestWriteAllowParallelism(t *testing.T) {
	tool := &writeTool{}

	t.Run("no conflict with different files", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: WriteToolName, Input: `{"file_path":"/a.go","content":"x"}`}
		call2 := ToolCall{ID: "2", Name: WriteToolName, Input: `{"file_path":"/b.go","content":"y"}`}
		allCalls := []ToolCall{call1, call2}
		if !tool.AllowParallelism(call1, allCalls) {
			t.Error("writeTool should allow parallel with different files")
		}
	})

	t.Run("conflict with same file", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: WriteToolName, Input: `{"file_path":"/a.go","content":"x"}`}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"a","new_string":"b"}`}
		allCalls := []ToolCall{call1, call2}
		if tool.AllowParallelism(call1, allCalls) {
			t.Error("writeTool should not allow parallel with edit on same file")
		}
	})
}

func TestMultiEditAllowParallelism(t *testing.T) {
	tool := &multiEditTool{}

	t.Run("no conflict with different files", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: MultiEditToolName, Input: `{"file_path":"/a.go","edits":[]}`}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/b.go","old_string":"a","new_string":"b"}`}
		allCalls := []ToolCall{call1, call2}
		if !tool.AllowParallelism(call1, allCalls) {
			t.Error("multiEditTool should allow parallel with different files")
		}
	})

	t.Run("conflict with same file", func(t *testing.T) {
		call1 := ToolCall{ID: "1", Name: MultiEditToolName, Input: `{"file_path":"/a.go","edits":[]}`}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"a","new_string":"b"}`}
		allCalls := []ToolCall{call1, call2}
		if tool.AllowParallelism(call1, allCalls) {
			t.Error("multiEditTool should not allow parallel with edit on same file")
		}
	})
}

func TestPatchAllowParallelism(t *testing.T) {
	tool := &patchTool{}

	t.Run("no conflict with different files", func(t *testing.T) {
		patchText := "*** Begin Patch\n*** Update File: /a.go\n@@ func foo()\n-old\n+new\n*** End Patch"
		input, _ := json.Marshal(PatchParams{PatchText: patchText})
		call1 := ToolCall{ID: "1", Name: PatchToolName, Input: string(input)}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/b.go","old_string":"a","new_string":"b"}`}
		allCalls := []ToolCall{call1, call2}
		if !tool.AllowParallelism(call1, allCalls) {
			t.Error("patchTool should allow parallel with different files")
		}
	})

	t.Run("conflict with same file", func(t *testing.T) {
		patchText := "*** Begin Patch\n*** Update File: /a.go\n@@ func foo()\n-old\n+new\n*** End Patch"
		input, _ := json.Marshal(PatchParams{PatchText: patchText})
		call1 := ToolCall{ID: "1", Name: PatchToolName, Input: string(input)}
		call2 := ToolCall{ID: "2", Name: EditToolName, Input: `{"file_path":"/a.go","old_string":"a","new_string":"b"}`}
		allCalls := []ToolCall{call1, call2}
		if tool.AllowParallelism(call1, allCalls) {
			t.Error("patchTool should not allow parallel with edit on same file")
		}
	})

	t.Run("invalid JSON returns false", func(t *testing.T) {
		call := ToolCall{ID: "1", Name: PatchToolName, Input: `{invalid}`}
		if tool.AllowParallelism(call, []ToolCall{call}) {
			t.Error("patchTool.AllowParallelism(invalid JSON) = true, want false")
		}
	})
}
