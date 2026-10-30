package workflow

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

type libOwnerFiles map[string]bool

func (f libOwnerFiles) FileExists(_ uuid.UUID, _ string, p string) bool { return f[p] }

func TestImportFileSet_LibPathTreatedAsPresent(t *testing.T) {
	owner := uuid.New()

	t.Run("no fallback wired: valid lib path is present", func(t *testing.T) {
		f := newImportFileSet([]string{"a.md"}, nil, owner)
		assert.True(t, f.FileExists("wf", "a.md"))
		assert.True(t, f.FileExists("wf", "@lib/x.md"))
		assert.False(t, f.FileExists("wf", "b.md"))
	})

	t.Run("malformed lib path is never present", func(t *testing.T) {
		f := newImportFileSet(nil, nil, owner)
		assert.False(t, f.FileExists("wf", "@lib/../x"))
	})

	t.Run("fallback wired: lib answered by the real lib", func(t *testing.T) {
		f := newImportFileSet(nil, libOwnerFiles{"@lib/x.md": true}, owner)
		assert.True(t, f.FileExists("wf", "@lib/x.md"))
		assert.False(t, f.FileExists("wf", "@lib/missing.md"))
	})

	t.Run("case-variant prefix is an ordinary workflow file", func(t *testing.T) {
		f := newImportFileSet(nil, nil, owner)
		assert.False(t, f.FileExists("wf", "@LIB/x"))
	})
}
