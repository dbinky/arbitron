package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each probe owns a private, subsequently removed child of the root's scratch
// directory. Checking the actual parent avoids accepting a production workspace
// (or an arbitrary path merely containing the text .model-probe).
func assertPrivateProbeDir(t *testing.T, invocation, root string) {
	t.Helper()
	fields := strings.Fields(invocation)
	require.NotEmpty(t, fields)
	dir := fields[0]
	physicalRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, physicalRoot, filepath.Dir(dir))
	assert.True(t, strings.HasPrefix(filepath.Base(dir), "check-"), "not a private probe: %s", dir)
	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, os.ErrNotExist, "probe scratch directory must be cleaned")
}

func isProbeTurn(invocation string) bool {
	fields := strings.Fields(invocation)
	return len(fields) > 0 && filepath.Base(filepath.Dir(fields[0])) == ".model-probe" && strings.HasPrefix(filepath.Base(fields[0]), "check-")
}
