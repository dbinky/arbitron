package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractSentinelResult(t *testing.T) {
	t.Run("extracts and parses a well-formed sentinel block", func(t *testing.T) {
		text := "Here is the outcome.\n<arbitron:result>{\"selected\":[1,2]}</arbitron:result>\nDone."
		out, ok, err := extractSentinelResult(text)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []any{float64(1), float64(2)}, out["selected"])
	})

	t.Run("no sentinel returns ok=false without error", func(t *testing.T) {
		_, ok, err := extractSentinelResult("just a normal reply")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("sentinel with invalid JSON returns an error", func(t *testing.T) {
		_, ok, err := extractSentinelResult("<arbitron:result>{not json}</arbitron:result>")
		require.Error(t, err)
		assert.False(t, ok)
	})

	t.Run("whitespace inside the sentinel is tolerated", func(t *testing.T) {
		out, ok, err := extractSentinelResult("<arbitron:result>\n  {\"a\":1}\n</arbitron:result>")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, float64(1), out["a"])
	})

	t.Run("last sentinel wins when several are present", func(t *testing.T) {
		out, ok, err := extractSentinelResult(
			"<arbitron:result>{\"v\":1}</arbitron:result> ... <arbitron:result>{\"v\":2}</arbitron:result>")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, float64(2), out["v"])
	})
}
