package engine

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Includes are base-aware. A directive in a workflow file resolves against the
// workflow; a directive in an @lib file resolves against the library; an
// explicit @lib/ prefix always names the library. The reader sees the literal
// path, so a lib read is the key "@lib/<rel>".

// recordingReader wraps includeReader and records every path it is asked for.
type recordingReader struct {
	includeReader
	reads []string
}

func (r *recordingReader) ReadFile(o uuid.UUID, w, p string) ([]byte, error) {
	r.reads = append(r.reads, p)
	return r.includeReader.ReadFile(o, w, p)
}

func recordingWith(files map[string]string) *recordingReader {
	return &recordingReader{includeReader: readerWith(files)}
}

// HAPPY
func TestIncludeLib_WorkflowTemplateIncludesLib(t *testing.T) {
	r := recordingWith(map[string]string{"@lib/common.md": "LIB TEXT"})
	out, err := expandIncludes(r, uuid.Nil, wf, "before {{include:@lib/common.md}} after")
	require.NoError(t, err)
	assert.Equal(t, "before LIB TEXT after", out)
	assert.Equal(t, []string{"@lib/common.md"}, r.reads)
}

// HAPPY
func TestIncludeLib_LibFileRelativeIncludeStaysInLib(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/a.md":    "A[{{include:part.md}}]",
		"@lib/part.md": "PART",
	})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/a.md}}")
	require.NoError(t, err)
	assert.Equal(t, "A[PART]", out)
	assert.Equal(t, []string{"@lib/a.md", "@lib/part.md"}, r.reads)
}

// HAPPY
func TestIncludeLib_LibTemplateFileExpandsLibRelative(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/x.md": "X[{{include:y.md}}]",
		"@lib/y.md": "Y",
		"y.md":      "WORKFLOW Y",
	})
	out, err := loadTemplate(r, uuid.Nil, wf, "@lib/x.md")
	require.NoError(t, err)
	assert.Equal(t, "X[Y]", out)
	assert.Equal(t, []string{"@lib/x.md", "@lib/y.md"}, r.reads)
}

// EDGE
func TestIncludeLib_LibRelativeBeatsSameNamedWorkflowFile(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/a.md":    "{{include:part.md}}",
		"@lib/part.md": "LIB PART",
		"part.md":      "WORKFLOW PART",
	})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/a.md}}")
	require.NoError(t, err)
	assert.Equal(t, "LIB PART", out)
}

// EDGE: an explicit @lib/ prefix inside a lib file is read as written. A
// relative include resolves against the library root, not the including
// file's directory.
func TestIncludeLib_ExplicitLibPrefixInsideLibFile(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/a.md":     "{{include:@lib/sub/b.md}}",
		"@lib/sub/b.md": "B[{{include:c.md}}]",
		"@lib/c.md":     "C",
	})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/a.md}}")
	require.NoError(t, err)
	assert.Equal(t, "B[C]", out)
	assert.Equal(t, []string{"@lib/a.md", "@lib/sub/b.md", "@lib/c.md"}, r.reads)
}

// EDGE
func TestIncludeLib_SameNameInBothBasesBothExpand(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/common.md": "LIB",
		"common.md":      "WF",
	})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:common.md}}|{{include:@lib/common.md}}")
	require.NoError(t, err, "same name in two bases is not a cycle")
	assert.Equal(t, "WF|LIB", out)
}

// ERROR: a lib file cannot reach a workflow-only file.
func TestIncludeLib_LibCannotReachWorkflowFile(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/a.md":           "{{include:only-in-workflow.md}}",
		"only-in-workflow.md": "SECRET",
	})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/a.md}}")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTemplateInclude), "got %v", err)
	assert.Equal(t, []string{"@lib/a.md", "@lib/only-in-workflow.md"}, r.reads)
}

// ERROR
func TestIncludeLib_CycleAcrossLibFilesDetected(t *testing.T) {
	r := recordingWith(map[string]string{
		"@lib/a.md": "{{include:b.md}}",
		"@lib/b.md": "{{include:a.md}}",
	})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/a.md}}")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTemplateInclude))
	assert.Contains(t, err.Error(), "cycle")
	assert.Contains(t, err.Error(), "@lib/a.md")
}

// ERROR
func TestIncludeLib_MalformedLibIncludeFails(t *testing.T) {
	r := recordingWith(map[string]string{"x": "X"})
	_, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/../x}}")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTemplateInclude), "got %v", err)
	assert.Empty(t, r.reads, "an invalid path is rejected before any read")
}

// ERROR (ruling P15): inside a lib file a relative include is rewritten to
// @lib/<path> and held to the library's strictness, so ./x and sub/../x are
// rejected there even though workflow includes accept them.
func TestIncludeLib_LibRelativeIncludeUsesLibStrictness(t *testing.T) {
	for _, inc := range []string{"./x.md", "sub/../x.md", "../x.md"} {
		t.Run(inc, func(t *testing.T) {
			r := recordingWith(map[string]string{
				"@lib/a.md": "{{include:" + inc + "}}",
				"@lib/x.md": "X",
				"x.md":      "X",
			})
			_, err := expandIncludes(r, uuid.Nil, wf, "{{include:@lib/a.md}}")
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrTemplateInclude), "got %v", err)
			assert.Equal(t, []string{"@lib/a.md"}, r.reads)
		})
	}
	// Workflow-based includes keep accepting the same spellings.
	// (The adapter cleans the path; this reader keys the literal spelling.)
	r := recordingWith(map[string]string{"./x.md": "X", "sub/../x.md": "X"})
	out, err := expandIncludes(r, uuid.Nil, wf, "{{include:./x.md}}{{include:sub/../x.md}}")
	require.NoError(t, err)
	assert.Equal(t, "XX", out)
}

// BOUNDARY: the include-count budget is shared across both bases.
func TestIncludeLib_BudgetsSharedAcrossBases(t *testing.T) {
	r := recordingWith(map[string]string{"@lib/l.md": "L", "w.md": "W"})
	build := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			if i%2 == 0 {
				b.WriteString("{{include:@lib/l.md}}")
			} else {
				b.WriteString("{{include:w.md}}")
			}
		}
		return b.String()
	}
	out, err := expandIncludes(r, uuid.Nil, wf, build(maxIncludeCount))
	require.NoError(t, err, fmt.Sprintf("include %d must succeed", maxIncludeCount))
	assert.Len(t, out, maxIncludeCount)

	_, err = expandIncludes(r, uuid.Nil, wf, build(maxIncludeCount+1))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTemplateInclude))
}
