package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkDirFromVars(t *testing.T) {
	t.Run("returns ok=false when workspace was never seeded", func(t *testing.T) {
		vars := variable.NewStore()
		got, ok := WorkDirFromVars(vars)
		assert.False(t, ok)
		assert.Equal(t, "", got)
	})

	t.Run("returns ok=true with the seeded value", func(t *testing.T) {
		vars := variable.NewStore()
		vars.SetReserved(ReservedVarWorkspace, "/path/to/ws")
		got, ok := WorkDirFromVars(vars)
		assert.True(t, ok)
		assert.Equal(t, "/path/to/ws", got)
	})

	t.Run("returns ok=true with empty string when seeded as empty", func(t *testing.T) {
		// Distinguishes "seeded empty" from "not seeded" — load-bearing for
		// the claudecli provider's refusal to run with empty WorkDir vs
		// refusing to even start when the workspace manager is unwired.
		vars := variable.NewStore()
		vars.SetReserved(ReservedVarWorkspace, "")
		got, ok := WorkDirFromVars(vars)
		assert.True(t, ok)
		assert.Equal(t, "", got)
	})

	t.Run("nil store returns ok=false", func(t *testing.T) {
		got, ok := WorkDirFromVars(nil)
		assert.False(t, ok)
		assert.Equal(t, "", got)
	})

	t.Run("non-string workspace value returns ok=true and empty string", func(t *testing.T) {
		// Defensive: if persistence ever round-trips workspace as a non-string
		// (e.g. int after a corrupt restore), behave like "seeded empty"
		// rather than panicking.
		vars := variable.NewStore()
		vars.SetReserved(ReservedVarWorkspace, 42)
		got, ok := WorkDirFromVars(vars)
		assert.True(t, ok)
		assert.Equal(t, "", got)
	})
}

func TestSessionIDHelpers(t *testing.T) {
	t.Run("SessionID reads the canonical key", func(t *testing.T) {
		assert.Equal(t, "s1", SessionID(map[string]any{ConversationKeySessionID: "s1"}))
	})

	t.Run("SessionID returns empty when key is absent", func(t *testing.T) {
		assert.Equal(t, "", SessionID(map[string]any{}))
	})

	t.Run("SessionID returns empty on nil conv", func(t *testing.T) {
		assert.Equal(t, "", SessionID(nil))
	})

	t.Run("SetSessionID writes under the canonical key", func(t *testing.T) {
		conv := map[string]any{"other": "value"}
		got := SetSessionID(conv, "s2")
		assert.Equal(t, "s2", got[ConversationKeySessionID])
		assert.Equal(t, "value", got["other"])
	})

	t.Run("SetSessionID allocates a new map when given nil", func(t *testing.T) {
		got := SetSessionID(nil, "s3")
		require.NotNil(t, got)
		assert.Equal(t, "s3", got[ConversationKeySessionID])
	})
}
