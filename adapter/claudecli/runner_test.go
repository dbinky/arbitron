package claudecli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecRunner(t *testing.T) {
	t.Run("streams stdout lines and exits cleanly", func(t *testing.T) {
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{
			Bin:   "printf",
			Args:  []string{`one\ntwo\n`},
			Stdin: "",
		})
		require.NoError(t, err)

		var got []string
		for line := range lines {
			got = append(got, line)
		}
		require.NoError(t, wait())
		assert.Equal(t, []string{"one", "two"}, got)
	})

	t.Run("non-zero exit is reported by wait", func(t *testing.T) {
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{Bin: "false"})
		require.NoError(t, err)
		for range lines { //nolint:revive // drain
		}
		assert.Error(t, wait())
	})

	t.Run("unknown binary fails at Start", func(t *testing.T) {
		r := newExecRunner()
		_, _, err := r.Run(context.Background(), RunSpec{Bin: "definitely-not-a-real-binary-xyz"})
		assert.Error(t, err)
	})

	t.Run("stdin is delivered to the process", func(t *testing.T) {
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{Bin: "cat", Stdin: "piped-input"})
		require.NoError(t, err)
		var got string
		for line := range lines {
			got += line
		}
		require.NoError(t, wait())
		assert.Equal(t, "piped-input", got)
	})

	t.Run("context cancellation kills the process and closes lines", func(t *testing.T) {
		r := newExecRunner()
		ctx, cancel := context.WithCancel(context.Background())
		lines, wait, err := r.Run(ctx, RunSpec{Bin: "sleep", Args: []string{"30"}})
		require.NoError(t, err)

		cancel()
		for range lines { //nolint:revive // drain
		}
		assert.Error(t, wait())
	})

	t.Run("MaxResponseBytes overrun returns ErrResponseTooLarge and kills the process", func(t *testing.T) {
		// MF7 / PR #8 thread 3291941918. Generate ~10KB of stdout while
		// the cap is 1KB; the runner must abort and surface
		// ErrResponseTooLarge from wait().
		big := strings.Repeat("x", 10*1024)
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{
			Bin:              "printf",
			Args:             []string{`%s\n`, big},
			MaxResponseBytes: 1024,
		})
		require.NoError(t, err)

		// Drain whatever made it through; the runner closes lines on
		// overrun.
		for range lines { //nolint:revive // drain
		}
		waitErr := wait()
		require.Error(t, waitErr)
		assert.ErrorIs(t, waitErr, ErrResponseTooLarge)
	})

	t.Run("ReadBytes has no fixed per-line ceiling (replaces 4MB Scanner cap)", func(t *testing.T) {
		// MF8 / PR #8 thread 3291921375. A single line over the old 4MB
		// limit must stream end-to-end. Use 5MB on stdin (avoids OS argv
		// limits) and have cat echo it back. No trailing newline, so the
		// reader gets one giant chunk followed by EOF.
		const size = 5 * 1024 * 1024
		big := strings.Repeat("y", size)
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{
			Bin:   "cat",
			Stdin: big,
		})
		require.NoError(t, err)

		var got int
		for line := range lines {
			got += len(line)
		}
		require.NoError(t, wait())
		assert.Equal(t, size, got, "5MB unterminated chunk must be delivered intact")
	})

	t.Run("stderr tail is summarised into wait()'s returned error", func(t *testing.T) {
		// SF1 / PR #8 thread 3291907729. A non-zero-exit subprocess that
		// wrote to stderr must surface a short summary in wait()'s error.
		r := newExecRunner()
		lines, wait, err := r.Run(context.Background(), RunSpec{
			Bin:  "sh",
			Args: []string{"-c", "echo something-broke >&2; exit 1"},
		})
		require.NoError(t, err)
		for range lines { //nolint:revive // drain
		}
		waitErr := wait()
		require.Error(t, waitErr)
		assert.Contains(t, waitErr.Error(), "something-broke")
	})
}

func TestTailBuf(t *testing.T) {
	t.Run("retains only the last max bytes across many small writes", func(t *testing.T) {
		buf := newTailBuf(8)
		for _, c := range []byte("abcdefghijklmnop") {
			_, _ = buf.Write([]byte{c})
		}
		assert.Equal(t, "ijklmnop", string(buf.Bytes()))
	})

	t.Run("single oversized write keeps only its trailing window", func(t *testing.T) {
		buf := newTailBuf(4)
		_, _ = buf.Write([]byte("abcdefghij"))
		assert.Equal(t, "ghij", string(buf.Bytes()))
	})

	t.Run("max=0 swallows writes silently", func(t *testing.T) {
		buf := newTailBuf(0)
		n, err := buf.Write([]byte("anything"))
		require.NoError(t, err)
		assert.Equal(t, 8, n)
		assert.Empty(t, buf.Bytes())
	})
}
