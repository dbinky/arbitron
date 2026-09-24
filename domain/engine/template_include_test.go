package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Templates share a common preamble (gh recipes, attribution rules, the
// read-only definition) by referencing a sibling file. Only the node's OWN
// templateFile was ever delivered to the agent, so those references named a
// file the agent never received — six pr-review personas ran for months on
// instructions whose second half silently never arrived, and one of them
// eventually posted nothing at all because the word "read-only" arrived
// without the sentence that defines it.
//
// expandIncludes closes that: `{{include:<path>}}` is replaced with the file's
// contents BEFORE variable substitution, so an included file's own {{vars}}
// still resolve normally.

const wf = "demo"

// includeReader is a minimal in-package FileReader. helpers_test.go's mock
// lives in package engine_test and cannot reach an unexported helper.
type includeReader struct{ files map[string]string }

func (r includeReader) ReadFile(_ uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	body, ok := r.files[workflowName+"/"+relativePath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(body), nil
}
func (r includeReader) ResolvePath(_ uuid.UUID, _, _ string) (string, error) { return "", nil }
func (r includeReader) FileExists(_ uuid.UUID, workflowName, relativePath string) bool {
	_, ok := r.files[workflowName+"/"+relativePath]
	return ok
}

func readerWith(files map[string]string) includeReader {
	keyed := make(map[string]string, len(files))
	for p, body := range files {
		keyed[wf+"/"+p] = body
	}
	return includeReader{files: keyed}
}

// HAPPY — a directive is replaced by the referenced file's contents.
func TestExpandIncludes_InlinesTheReferencedFile(t *testing.T) {
	r := readerWith(map[string]string{"_common.md": "SHARED PREAMBLE"})
	out, err := expandIncludes(r, uuid.Nil, wf, "before\n{{include:_common.md}}\nafter")
	require.NoError(t, err)
	assert.Equal(t, "before\nSHARED PREAMBLE\nafter", out)
}

// SUCCESS — includes nest, and an included file's variables survive expansion
// so the later substitution pass can resolve them.
func TestExpandIncludes_NestsAndPreservesVariables(t *testing.T) {
	r := readerWith(map[string]string{
		"a.md": "A({{include:b.md}})",
		"b.md": "B for PR {{pr_number}}",
	})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:a.md}}")
	require.NoError(t, err)
	assert.Equal(t, "A(B for PR {{pr_number}})", out,
		"nested include expands and {{pr_number}} is left for variable.Resolve")
}

// SUCCESS — the same file included twice expands at both sites. Repetition is
// not a cycle.
func TestExpandIncludes_SameFileTwiceIsNotACycle(t *testing.T) {
	r := readerWith(map[string]string{"x.md": "X"})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:x.md}} and {{include:x.md}}")
	require.NoError(t, err)
	assert.Equal(t, "X and X", out)
}

// FAILURE — a missing include FAILS the node. Silently dropping it is the exact
// defect this exists to prevent: the agent would run on half its instructions
// and nothing would look wrong.
func TestExpandIncludes_MissingFileFailsClosed(t *testing.T) {
	r := readerWith(map[string]string{})
	_, err := expandIncludes(r, uuid.Nil, wf, "x {{include:nope.md}} y")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope.md", "the error must name the missing file")
}

// ERROR — a cycle terminates with an error instead of recursing forever.
func TestExpandIncludes_CycleIsRejected(t *testing.T) {
	r := readerWith(map[string]string{
		"a.md": "{{include:b.md}}",
		"b.md": "{{include:a.md}}",
	})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:a.md}}")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "include", "error should name the include problem")
}

// ERROR — a self-include is a cycle too.
func TestExpandIncludes_SelfIncludeIsRejected(t *testing.T) {
	r := readerWith(map[string]string{"loop.md": "{{include:loop.md}}"})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:loop.md}}")
	require.Error(t, err)
}

// ERROR — an empty include path is rejected rather than read as the workflow
// directory itself.
func TestExpandIncludes_EmptyPathIsRejected(t *testing.T) {
	r := readerWith(map[string]string{})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:}}")
	require.Error(t, err)
}

// EDGE — a template with no directive is returned byte-for-byte, so every
// existing template is unaffected.
func TestExpandIncludes_NoDirectiveIsUnchanged(t *testing.T) {
	body := "plain {{pr_number}} template\nwith {{a.b}} vars and a } brace"
	r := readerWith(map[string]string{})
	out, err := expandIncludes(r, uuid.Nil, wf, body)
	require.NoError(t, err)
	assert.Equal(t, body, out)
}

// EDGE — surrounding whitespace in the directive is tolerated, since a template
// author will write it both ways.
func TestExpandIncludes_TolerantOfWhitespace(t *testing.T) {
	r := readerWith(map[string]string{"c.md": "C"})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include: c.md }}")
	require.NoError(t, err)
	assert.Equal(t, "C", out)
}

// EDGE — an empty string and an empty included file are both fine.
func TestExpandIncludes_EmptyInputs(t *testing.T) {
	r := readerWith(map[string]string{"empty.md": ""})
	out, err := expandIncludes(r, uuid.Nil, wf, "")
	require.NoError(t, err)
	assert.Empty(t, out)

	out2, err2 := expandIncludes(r, uuid.Nil, wf, "[{{include:empty.md}}]")
	require.NoError(t, err2)
	assert.Equal(t, "[]", out2)
}

// fanoutReader builds the "billion laughs" shape: levels files, each including
// the next one fanout times, ending in a leaf of leafSize bytes.
func fanoutReader(levels, fanout, leafSize int) includeReader {
	files := map[string]string{}
	for i := 1; i < levels; i++ {
		files[fmt.Sprintf("f%d.md", i)] = strings.Repeat(fmt.Sprintf("{{include:f%d.md}}", i+1), fanout)
	}
	files[fmt.Sprintf("f%d.md", levels)] = strings.Repeat("x", leafSize)
	return readerWith(files)
}

// SECURITY — a small fan-out graph must not amplify into gigabytes. Under 1 KB
// of input here expands to ~1 GB without a budget; it must fail closed instead.
func TestExpandIncludes_FanOutAmplificationIsRejected(t *testing.T) {
	r := fanoutReader(9, 6, 100)
	top := strings.Repeat("{{include:f1.md}}", 6)
	_, err := expandIncludes(r, uuid.Nil, wf, top)
	require.ErrorIs(t, err, ErrTemplateInclude)
	assert.Contains(t, err.Error(), "includes resolved")
}

// SECURITY — few includes of a large file are bounded by total bytes, not only
// by include count.
func TestExpandIncludes_ByteBudgetIsEnforced(t *testing.T) {
	r := readerWith(map[string]string{"big.md": strings.Repeat("x", maxIncludeBytes/2+1)})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:big.md}}{{include:big.md}}")
	require.ErrorIs(t, err, ErrTemplateInclude)
	assert.Contains(t, err.Error(), "bytes")
}

// EDGE — the budget is sized so realistic repetition still expands.
func TestExpandIncludes_WithinBudgetSucceeds(t *testing.T) {
	r := readerWith(map[string]string{"x.md": "X"})
	out, err := expandIncludes(r, uuid.Nil, wf, strings.Repeat("{{include:x.md}}", maxIncludeCount))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("X", maxIncludeCount), out)
}

// ERROR — equivalent spellings of the same path are one file for cycle
// detection, so the error names the cycle rather than tripping the depth bound.
func TestExpandIncludes_CycleDetectionCleansPaths(t *testing.T) {
	r := readerWith(map[string]string{
		"a.md":        "{{include:./a.md}}",
		"./a.md":      "{{include:sub/../a.md}}",
		"sub/../a.md": "unreachable",
	})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:a.md}}")
	require.ErrorIs(t, err, ErrTemplateInclude)
	assert.Contains(t, err.Error(), "cycle")
}

// PINNED — there is no escape: a directive in prose is still a directive. If an
// escape syntax is ever added, this test must change deliberately.
func TestExpandIncludes_NoEscapeForLiteralDirective(t *testing.T) {
	r := readerWith(map[string]string{})
	_, err := expandIncludes(r, uuid.Nil, wf, "Write `{{include:_common.md}}` to share a preamble.")
	require.ErrorIs(t, err, ErrTemplateInclude, "a directive mentioned in prose is still read")
}
