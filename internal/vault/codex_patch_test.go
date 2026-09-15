package vault

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexPatchToDiff_Add(t *testing.T) {
	text, added, removed, ok := codexPatchToDiff(codexAddPatch)
	require.True(t, ok)
	assert.Equal(t, strings.Join([]string{
		"*** Add File: /tmp/proj/script.py",
		"@@ -0,0 +1,2 @@",
		"+#!/usr/bin/env python3",
		"+import json",
	}, "\n"), text, "an Add File section gets an exact hunk header")
	assert.Equal(t, 2, added)
	assert.Equal(t, 0, removed)
}

func TestCodexPatchToDiff_UpdateTwoHunks(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: /p/config.toml\n@@ [db]\n-retries = 1\n+retries = 3\n@@\n timeout = 30\n\n+keepalive = true\n*** End of File\n*** End Patch"
	text, added, removed, ok := codexPatchToDiff(patch)
	require.True(t, ok)
	assert.Equal(t, strings.Join([]string{
		"*** Update File: /p/config.toml",
		"@@ [db]",
		"-retries = 1",
		"+retries = 3",
		"@@",
		" timeout = 30",
		"", // blank context line kept verbatim
		"+keepalive = true",
	}, "\n"), text, "update hunks keep Codex's own @@ headers (no line numbers exist to fabricate); End of File is dropped")
	assert.Equal(t, 2, added)
	assert.Equal(t, 1, removed)
}

func TestCodexPatchToDiff_Delete(t *testing.T) {
	text, added, removed, ok := codexPatchToDiff("*** Begin Patch\n*** Delete File: /p/old.go\n*** End Patch\n")
	require.True(t, ok, "a delete-only patch is a real change with no lines")
	assert.Equal(t, "*** Delete File: /p/old.go", text)
	assert.Zero(t, added)
	assert.Zero(t, removed)
}

func TestCodexPatchToDiff_Move(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: /p/a.go\n*** Move to: /p/b.go\n@@\n-package a\n+package b\n*** End Patch\n"
	text, added, removed, ok := codexPatchToDiff(patch)
	require.True(t, ok)
	assert.Equal(t, "*** Update File: /p/a.go\n*** Move to: /p/b.go\n@@\n-package a\n+package b", text)
	assert.Equal(t, 1, added)
	assert.Equal(t, 1, removed)
}

func TestCodexPatchToDiff_MultiFile(t *testing.T) {
	patch := "\n*** Begin Patch\n*** Add File: /p/new.txt\n+hello\n*** Delete File: /p/gone.txt\n*** Update File: /p/keep.txt\n@@ hint\n-x\n+y\n*** End Patch\n\n"
	text, added, removed, ok := codexPatchToDiff(patch)
	require.True(t, ok, "leading/trailing blank lines are tolerated")
	assert.Equal(t, strings.Join([]string{
		"*** Add File: /p/new.txt", "@@ -0,0 +1,1 @@", "+hello",
		"*** Delete File: /p/gone.txt",
		"*** Update File: /p/keep.txt", "@@ hint", "-x", "+y",
	}, "\n"), text)
	assert.Equal(t, 2, added)
	assert.Equal(t, 1, removed)
}

func TestCodexPatchToDiff_EmptyAdd(t *testing.T) {
	text, added, _, ok := codexPatchToDiff("*** Begin Patch\n*** Add File: /p/empty\n*** End Patch")
	require.True(t, ok)
	assert.Equal(t, "*** Add File: /p/empty", text, "an empty file add has a header and no hunk")
	assert.Zero(t, added)
}

func TestCodexPatchToDiff_Malformed(t *testing.T) {
	for name, patch := range map[string]string{
		"empty":                       "",
		"no begin":                    "*** Add File: /p/x\n+a\n*** End Patch",
		"no end":                      "*** Begin Patch\n*** Add File: /p/x\n+a\n",
		"content after end":           "*** Begin Patch\n*** Add File: /p/x\n+a\n*** End Patch\n+stray",
		"body before any file":        "*** Begin Patch\n+a\n*** End Patch",
		"non-plus line under add":     "*** Begin Patch\n*** Add File: /p/x\n a\n*** End Patch",
		"body under delete":           "*** Begin Patch\n*** Delete File: /p/x\n-a\n*** End Patch",
		"update body before hunk":     "*** Begin Patch\n*** Update File: /p/x\n-a\n*** End Patch",
		"unknown hunk prefix":         "*** Begin Patch\n*** Update File: /p/x\n@@\n*a\n*** End Patch",
		"unknown directive":           "*** Begin Patch\n*** Rename File: /p/x\n*** End Patch",
		"move outside update":         "*** Begin Patch\n*** Add File: /p/x\n*** Move to: /p/y\n*** End Patch",
		"move inside hunk":            "*** Begin Patch\n*** Update File: /p/x\n@@\n a\n*** Move to: /p/y\n*** End Patch",
		"end of file outside hunk":    "*** Begin Patch\n*** Update File: /p/x\n*** End of File\n*** End Patch",
		"hunk header under add":       "*** Begin Patch\n*** Add File: /p/x\n@@\n+a\n*** End Patch",
		"claude structuredPatch json": `{"structuredPatch":[{"lines":["+a"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			text, added, removed, ok := codexPatchToDiff(patch)
			assert.False(t, ok)
			assert.Empty(t, text)
			assert.Zero(t, added)
			assert.Zero(t, removed)
		})
	}
}

func TestCodexPatchToDiff_CRLF(t *testing.T) {
	text, added, _, ok := codexPatchToDiff("*** Begin Patch\r\n*** Add File: /p/x\r\n+a\r\n*** End Patch\r\n")
	require.True(t, ok)
	assert.Equal(t, "*** Add File: /p/x\n@@ -0,0 +1,1 @@\n+a", text)
	assert.Equal(t, 1, added)
}

func TestCodexPatchFiles(t *testing.T) {
	assert.Equal(t, []string{"/tmp/proj/script.py"}, codexPatchFiles(codexAddPatch))
	assert.Equal(t, []string{"/p/new.txt", "/p/gone.txt", "/p/keep.txt"},
		codexPatchFiles("*** Begin Patch\n*** Add File: /p/new.txt\n+hello\n*** Delete File: /p/gone.txt\n*** Update File: /p/keep.txt\n*** Move to: /p/moved.txt\n@@\n-x\n*** End Patch\n"),
		"in patch order; a Move keeps the Update path")
	assert.Equal(t, []string{"/p/x"}, codexPatchFiles("*** Begin Patch\n*** Add File: /p/x\n+truncated"), "lenient: works on a malformed patch")
	assert.Nil(t, codexPatchFiles("*** Begin Patch\n*** Add File:   \n*** End Patch"), "blank path ignored")
	assert.Nil(t, codexPatchFiles(""))
}

func TestCodexPatchToDiff_WhitespaceAfterEnd(t *testing.T) {
	text, added, _, ok := codexPatchToDiff("*** Begin Patch\n*** Add File: /p/x\n+a\n*** End Patch\n   \n\t\n")
	require.True(t, ok, "whitespace-only lines after End Patch are tolerated")
	assert.Equal(t, "*** Add File: /p/x\n@@ -0,0 +1,1 @@\n+a", text)
	assert.Equal(t, 1, added)
}
