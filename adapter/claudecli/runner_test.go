package claudecli

import (
	"context"
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
}
