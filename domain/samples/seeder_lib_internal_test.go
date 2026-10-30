package samples

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

type libOwnerFiles map[string]bool

func (f libOwnerFiles) FileExists(_ uuid.UUID, _ string, p string) bool { return f[p] }

func TestSeedFiles_LibPathTreatedAsPresent(t *testing.T) {
	owner := uuid.New()
	files := map[string][]byte{"a.md": []byte("x")}

	t.Run("no fallback wired: valid lib path is present", func(t *testing.T) {
		f := newSeedFiles(files, nil, owner)
		assert.True(t, f.FileExists("wf", "a.md"))
		assert.True(t, f.FileExists("wf", "@lib/x.md"))
		assert.False(t, f.FileExists("wf", "b.md"))
	})

	t.Run("malformed lib path is never present", func(t *testing.T) {
		assert.False(t, newSeedFiles(files, nil, owner).FileExists("wf", "@lib/../x"))
	})

	t.Run("fallback wired: lib answered by the real lib", func(t *testing.T) {
		f := newSeedFiles(files, libOwnerFiles{"@lib/x.md": true}, owner)
		assert.True(t, f.FileExists("wf", "@lib/x.md"))
		assert.False(t, f.FileExists("wf", "@lib/missing.md"))
	})
}
