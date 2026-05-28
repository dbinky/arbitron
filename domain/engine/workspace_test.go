package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// fakeWorkspaceMgrForTests is a tiny WorkspaceManager that exposes only the
// hook surface needed by these unit tests. It lives in this file rather than
// re-using the engine_test.go fake (different package) so it can drive the
// warnWorkspaceRetained helper directly.
type fakeWorkspaceMgrForTests struct {
	bytes int64
	files int
	err   error
}

func (f *fakeWorkspaceMgrForTests) EnsureInstanceDir(uuid.UUID) (string, error) {
	return "/tmp", nil
}
func (f *fakeWorkspaceMgrForTests) RemoveInstanceDir(uuid.UUID) error { return nil }
func (f *fakeWorkspaceMgrForTests) InstanceDirStats(uuid.UUID) (int64, int, error) {
	return f.bytes, f.files, f.err
}

// captureSlog routes slog output to buf for the duration of fn.
func captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

func TestEngineWarnWorkspaceRetained(t *testing.T) {
	t.Run("emits a warning with bytes and files when workspace has content", func(t *testing.T) {
		e := &Engine{workspaceManager: &fakeWorkspaceMgrForTests{bytes: 4096, files: 3}}
		id := uuid.New()

		out := captureSlog(t, func() {
			e.warnWorkspaceRetained(id, "instance_failed")
		})
		assert.Contains(t, out, "workspace retained on disk")
		assert.Contains(t, out, "reason=instance_failed")
		assert.Contains(t, out, "bytes=4096")
		assert.Contains(t, out, "files=3")
	})

	t.Run("no-op when workspace is empty (nothing to retain)", func(t *testing.T) {
		e := &Engine{workspaceManager: &fakeWorkspaceMgrForTests{}}
		out := captureSlog(t, func() {
			e.warnWorkspaceRetained(uuid.New(), "instance_failed")
		})
		assert.NotContains(t, strings.ToLower(out), "workspace retained on disk")
	})

	t.Run("logs warning with error when stats call fails", func(t *testing.T) {
		e := &Engine{workspaceManager: &fakeWorkspaceMgrForTests{err: assertErr{"stat broke"}}}
		out := captureSlog(t, func() {
			e.warnWorkspaceRetained(uuid.New(), "set_completed_failed")
		})
		assert.Contains(t, out, "workspace retained but stat failed")
		assert.Contains(t, out, "stat broke")
	})

	t.Run("no-op when no workspace manager is wired", func(t *testing.T) {
		e := &Engine{} // workspaceManager nil
		out := captureSlog(t, func() {
			e.warnWorkspaceRetained(uuid.New(), "x")
		})
		assert.Empty(t, out)
	})
}

type assertErr struct{ s string }

func (e assertErr) Error() string { return e.s }
