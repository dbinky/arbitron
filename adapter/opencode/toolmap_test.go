package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapTools(t *testing.T) {
	got, dropped := mapTools([]string{"Read", "Write", "Edit", "Grep", "Glob", "Bash"})
	assert.Equal(t, []string{"read", "write", "edit", "grep", "glob", "bash"}, got)
	assert.Empty(t, dropped)
}

func TestMapTools_DropsUnknownAndReportsThem(t *testing.T) {
	got, dropped := mapTools([]string{"Read", "MultiEdit", "TodoRead", "Bash"})
	assert.Equal(t, []string{"read", "bash"}, got)
	assert.Equal(t, []string{"MultiEdit", "TodoRead"}, dropped)
}

func TestMapTools_CaseInsensitive(t *testing.T) {
	got, _ := mapTools([]string{" read ", "BASH"})
	assert.Equal(t, []string{"read", "bash"}, got)
}

func TestMapTools_EmptyInputEmptyOutput(t *testing.T) {
	got, dropped := mapTools(nil)
	require.Empty(t, got)
	require.Empty(t, dropped)
}

// SECURITY — the mapped set is the canonical permitted set, so duplicate requests
// (including ones that differ only by case/whitespace or distinct names colliding
// on one opencode tool) collapse to a single entry. The permission block then
// names each permitted tool exactly once.
func TestMapTools_DedupsOnInsert(t *testing.T) {
	got, dropped := mapTools([]string{"Read", "read", " READ ", "Bash", "bash"})
	assert.Equal(t, []string{"read", "bash"}, got, "duplicates collapse, first-seen order preserved")
	assert.Empty(t, dropped)
}
