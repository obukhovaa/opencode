package diff

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func applyPatchInMemory(t *testing.T, patchText string, files map[string]string) map[string]string {
	t.Helper()

	patch, fuzz, err := TextToPatch(patchText, files)
	require.NoError(t, err)
	assert.LessOrEqual(t, fuzz, 3, "fuzz level too high")

	commit, err := PatchToCommit(patch, files)
	require.NoError(t, err)

	result := make(map[string]string)
	for k, v := range files {
		result[k] = v
	}

	err = ApplyCommit(commit, func(path string, content string) error {
		result[path] = content
		return nil
	}, func(path string) error {
		delete(result, path)
		return nil
	})
	require.NoError(t, err)

	return result
}

func TestTextToPatch_InvalidFormat(t *testing.T) {
	_, _, err := TextToPatch("invalid patch", nil)
	assert.Error(t, err)
}

func TestTextToPatch_EmptyPatch(t *testing.T) {
	patch, _, err := TextToPatch("*** Begin Patch\n*** End Patch", map[string]string{})
	require.NoError(t, err)
	assert.Empty(t, patch.Actions)
}

func TestTextToPatch_InvalidHeader(t *testing.T) {
	_, _, err := TextToPatch("*** Begin Patch\n*** Frobnicate File: foo\n*** End Patch", map[string]string{})
	assert.Error(t, err)
}

func TestPatch_AddUpdateDelete(t *testing.T) {
	files := map[string]string{
		"modify.txt": "line1\nline2\n",
		"delete.txt": "obsolete\n",
	}

	patchText := "*** Begin Patch\n*** Add File: nested/new.txt\n+created\n*** Delete File: delete.txt\n*** Update File: modify.txt\n@@\n-line2\n+changed\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)

	assert.Equal(t, "created", result["nested/new.txt"])
	assert.Equal(t, "line1\nchanged\n", result["modify.txt"])
	_, exists := result["delete.txt"]
	assert.False(t, exists)
}

func TestPatch_MultipleHunks(t *testing.T) {
	files := map[string]string{
		"multi.txt": "line1\nline2\nline3\nline4\n",
	}

	patchText := "*** Begin Patch\n*** Update File: multi.txt\n@@\n-line2\n+changed2\n@@\n-line4\n+changed4\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "line1\nchanged2\nline3\nchanged4\n", result["multi.txt"])
}

func TestPatch_InsertOnly(t *testing.T) {
	files := map[string]string{
		"insert_only.txt": "alpha\nomega\n",
	}

	patchText := "*** Begin Patch\n*** Update File: insert_only.txt\n@@\n alpha\n+beta\n omega\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "alpha\nbeta\nomega\n", result["insert_only.txt"])
}

func TestPatch_MoveFile(t *testing.T) {
	files := map[string]string{
		"old/name.txt": "old content\n",
	}

	patchText := "*** Begin Patch\n*** Update File: old/name.txt\n*** Move to: renamed/dir/name.txt\n@@\n-old content\n+new content\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)

	_, exists := result["old/name.txt"]
	assert.False(t, exists)
	assert.Equal(t, "new content\n", result["renamed/dir/name.txt"])
}

func TestPatch_MissingContextRejected(t *testing.T) {
	files := map[string]string{
		"modify.txt": "line1\nline2\n",
	}

	patchText := "*** Begin Patch\n*** Update File: modify.txt\n@@\n-missing\n+changed\n*** End Patch"

	_, _, err := TextToPatch(patchText, files)
	assert.Error(t, err)
}

func TestPatch_MissingFileForUpdate(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: missing.txt\n@@\n-nope\n+better\n*** End Patch"

	_, _, err := TextToPatch(patchText, map[string]string{})
	assert.Error(t, err)
}

func TestPatch_MissingFileForDelete(t *testing.T) {
	patchText := "*** Begin Patch\n*** Delete File: missing.txt\n*** End Patch"

	_, _, err := TextToPatch(patchText, map[string]string{})
	assert.Error(t, err)
}

func TestPatch_DisambiguateContextWithHeader(t *testing.T) {
	files := map[string]string{
		"multi_ctx.txt": "fn a\nx=10\ny=2\nfn b\nx=10\ny=20\n",
	}

	patchText := "*** Begin Patch\n*** Update File: multi_ctx.txt\n@@ fn b\n-x=10\n+x=11\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "fn a\nx=10\ny=2\nfn b\nx=11\ny=20\n", result["multi_ctx.txt"])
}

func TestPatch_EOFAnchor(t *testing.T) {
	files := map[string]string{
		"tail.txt": "alpha\nlast",
	}

	patchText := "*** Begin Patch\n*** Update File: tail.txt\n@@\n-last\n+end\n*** End of File\n*** End Patch"

	patch, _, err := TextToPatch(patchText, files)
	require.NoError(t, err)

	commit, err := PatchToCommit(patch, files)
	require.NoError(t, err)

	result := make(map[string]string)
	for k, v := range files {
		result[k] = v
	}
	err = ApplyCommit(commit, func(path string, content string) error {
		result[path] = content
		return nil
	}, func(path string) error {
		delete(result, path)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha\nend", result["tail.txt"])
}

func TestPatch_EOFAnchorMatchesFromEnd(t *testing.T) {
	files := map[string]string{
		"eof_anchor.txt": "start\nmarker\nmiddle\nmarker\nend",
	}

	patchText := "*** Begin Patch\n*** Update File: eof_anchor.txt\n@@\n-marker\n-end\n+marker-changed\n+end\n*** End of File\n*** End Patch"

	patch, _, err := TextToPatch(patchText, files)
	require.NoError(t, err)

	commit, err := PatchToCommit(patch, files)
	require.NoError(t, err)

	result := make(map[string]string)
	for k, v := range files {
		result[k] = v
	}
	err = ApplyCommit(commit, func(path string, content string) error {
		result[path] = content
		return nil
	}, func(path string) error {
		delete(result, path)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "start\nmarker\nmiddle\nmarker-changed\nend", result["eof_anchor.txt"])
}

func TestPatch_TrailingWhitespaceFuzzy(t *testing.T) {
	files := map[string]string{
		"trailing_ws.txt": "line1  \nline2\nline3   \n",
	}

	patchText := "*** Begin Patch\n*** Update File: trailing_ws.txt\n@@\n-line2\n+changed\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "line1  \nchanged\nline3   \n", result["trailing_ws.txt"])
}

func TestPatch_LeadingWhitespaceFuzzy(t *testing.T) {
	files := map[string]string{
		"leading_ws.txt": "  line1\nline2\n  line3\n",
	}

	patchText := "*** Begin Patch\n*** Update File: leading_ws.txt\n@@\n-line2\n+changed\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "  line1\nchanged\n  line3\n", result["leading_ws.txt"])
}

func TestPatch_UnicodePunctuationNormalization(t *testing.T) {
	leftQuote := "\u201C"
	rightQuote := "\u201D"
	emDash := "\u2014"
	files := map[string]string{
		"unicode.txt": "He said " + leftQuote + "hello" + rightQuote + "\nsome" + emDash + "dash\nend\n",
	}

	patchText := "*** Begin Patch\n*** Update File: unicode.txt\n@@\n-He said \"hello\"\n+He said \"hi\"\n*** End Patch"

	patch, fuzz, err := TextToPatch(patchText, files)
	require.NoError(t, err)
	assert.Greater(t, fuzz, 0, "expected fuzzy match for unicode normalization")

	commit, err := PatchToCommit(patch, files)
	require.NoError(t, err)

	result := make(map[string]string)
	for k, v := range files {
		result[k] = v
	}
	err = ApplyCommit(commit, func(path string, content string) error {
		result[path] = content
		return nil
	}, func(path string) error {
		delete(result, path)
		return nil
	})
	require.NoError(t, err)

	assert.Contains(t, result["unicode.txt"], `He said "hi"`)
}

func TestPatch_VerificationFailureNoSideEffects(t *testing.T) {
	files := map[string]string{}

	patchText := "*** Begin Patch\n*** Add File: created.txt\n+hello\n*** Update File: missing.txt\n@@\n-old\n+new\n*** End Patch"

	_, _, err := TextToPatch(patchText, files)
	assert.Error(t, err)
}

func TestPatch_DuplicateUpdatePath(t *testing.T) {
	files := map[string]string{
		"dup.txt": "content\n",
	}

	patchText := "*** Begin Patch\n*** Update File: dup.txt\n@@\n-content\n+changed\n*** Update File: dup.txt\n@@\n-changed\n+again\n*** End Patch"

	_, _, err := TextToPatch(patchText, files)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Duplicate")
}

func TestPatch_AddFileAlreadyExists(t *testing.T) {
	files := map[string]string{
		"existing.txt": "content\n",
	}

	patchText := "*** Begin Patch\n*** Add File: existing.txt\n+new content\n*** End Patch"

	_, _, err := TextToPatch(patchText, files)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestPatch_AddFileInvalidLine(t *testing.T) {
	patchText := "*** Begin Patch\n*** Add File: new.txt\nnot a plus line\n*** End Patch"

	_, _, err := TextToPatch(patchText, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid Add File Line")
}

func TestIdentifyFilesNeeded(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Delete File: b.txt\n*** Add File: c.txt\n+content\n*** End Patch"

	files := IdentifyFilesNeeded(patchText)
	assert.ElementsMatch(t, []string{"a.txt", "b.txt"}, files)
}

func TestIdentifyFilesAdded(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Add File: c.txt\n+content\n*** Add File: d.txt\n+more\n*** End Patch"

	files := IdentifyFilesAdded(patchText)
	assert.ElementsMatch(t, []string{"c.txt", "d.txt"}, files)
}

func TestPatchToCommit_Delete(t *testing.T) {
	files := map[string]string{
		"delete.txt": "old content\n",
	}

	patchText := "*** Begin Patch\n*** Delete File: delete.txt\n*** End Patch"

	patch, _, err := TextToPatch(patchText, files)
	require.NoError(t, err)

	commit, err := PatchToCommit(patch, files)
	require.NoError(t, err)

	change := commit.Changes["delete.txt"]
	assert.Equal(t, ActionDelete, change.Type)
	assert.Equal(t, "old content\n", *change.OldContent)
	assert.Nil(t, change.NewContent)
}

func TestPatchToCommit_Add(t *testing.T) {
	patchText := "*** Begin Patch\n*** Add File: new.txt\n+hello world\n*** End Patch"

	patch, _, err := TextToPatch(patchText, map[string]string{})
	require.NoError(t, err)

	commit, err := PatchToCommit(patch, map[string]string{})
	require.NoError(t, err)

	change := commit.Changes["new.txt"]
	assert.Equal(t, ActionAdd, change.Type)
	assert.Nil(t, change.OldContent)
	assert.Equal(t, "hello world", *change.NewContent)
}

func TestPatchToCommit_MovePreservesPath(t *testing.T) {
	files := map[string]string{
		"old.txt": "content\n",
	}

	patchText := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt\n@@\n-content\n+updated\n*** End Patch"

	patch, _, err := TextToPatch(patchText, files)
	require.NoError(t, err)

	commit, err := PatchToCommit(patch, files)
	require.NoError(t, err)

	change := commit.Changes["old.txt"]
	assert.Equal(t, ActionUpdate, change.Type)
	require.NotNil(t, change.MovePath)
	assert.Equal(t, "new.txt", *change.MovePath)
	assert.Equal(t, "updated\n", *change.NewContent)
}

func TestNormalizeUnicode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"left single quote", "\u2018hello\u2019", "'hello'"},
		{"left double quote", "\u201Chello\u201D", "\"hello\""},
		{"em dash", "a\u2014b", "a-b"},
		{"en dash", "a\u2013b", "a-b"},
		{"ellipsis", "wait\u2026", "wait..."},
		{"non-breaking space", "hello\u00A0world", "hello world"},
		{"plain ascii unchanged", "hello world", "hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeUnicode(tt.input))
		})
	}
}

func TestValidatePatch(t *testing.T) {
	t.Run("valid patch", func(t *testing.T) {
		files := map[string]string{
			"test.txt": "hello\nworld\n",
		}
		patchText := "*** Begin Patch\n*** Update File: test.txt\n@@\n-hello\n+hi\n*** End Patch"

		valid, msg, err := ValidatePatch(patchText, files)
		require.NoError(t, err)
		assert.True(t, valid)
		assert.Equal(t, "Patch is valid", msg)
	})

	t.Run("missing begin patch", func(t *testing.T) {
		valid, msg, err := ValidatePatch("not a patch", nil)
		require.NoError(t, err)
		assert.False(t, valid)
		assert.Contains(t, msg, "Begin Patch")
	})

	t.Run("missing file", func(t *testing.T) {
		patchText := "*** Begin Patch\n*** Update File: missing.txt\n@@\n-old\n+new\n*** End Patch"

		valid, msg, err := ValidatePatch(patchText, map[string]string{})
		require.NoError(t, err)
		assert.False(t, valid)
		assert.Contains(t, msg, "not found")
	})
}

func TestPatch_MultipleAddFiles(t *testing.T) {
	patchText := "*** Begin Patch\n*** Add File: a.txt\n+aaa\n*** Add File: b.txt\n+bbb\n*** End Patch"

	result := applyPatchInMemory(t, patchText, map[string]string{})
	assert.Equal(t, "aaa", result["a.txt"])
	assert.Equal(t, "bbb", result["b.txt"])
}

func TestPatch_UpdatePreservesUnchangedLines(t *testing.T) {
	files := map[string]string{
		"file.txt": "line1\nline2\nline3\nline4\nline5\n",
	}

	patchText := "*** Begin Patch\n*** Update File: file.txt\n@@\n-line3\n+CHANGED\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	lines := strings.Split(result["file.txt"], "\n")
	assert.Equal(t, "line1", lines[0])
	assert.Equal(t, "line2", lines[1])
	assert.Equal(t, "CHANGED", lines[2])
	assert.Equal(t, "line4", lines[3])
	assert.Equal(t, "line5", lines[4])
}

func TestPatch_DeleteMultipleLinesInChunk(t *testing.T) {
	files := map[string]string{
		"file.txt": "keep\nremove1\nremove2\nremove3\nalso keep\n",
	}

	patchText := "*** Begin Patch\n*** Update File: file.txt\n@@\n keep\n-remove1\n-remove2\n-remove3\n also keep\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "keep\nalso keep\n", result["file.txt"])
}

func TestPatch_ReplaceMultipleLinesWithMultiple(t *testing.T) {
	files := map[string]string{
		"file.txt": "a\nb\nc\n",
	}

	patchText := "*** Begin Patch\n*** Update File: file.txt\n@@\n-a\n-b\n-c\n+x\n+y\n+z\n*** End Patch"

	result := applyPatchInMemory(t, patchText, files)
	assert.Equal(t, "x\ny\nz\n", result["file.txt"])
}
