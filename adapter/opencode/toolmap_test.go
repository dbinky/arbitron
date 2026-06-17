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
