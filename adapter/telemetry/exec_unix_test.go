//go:build darwin || linux

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecCommandKeepsStdoutOnFailure(t *testing.T) {
	out, err := execCommand(context.Background(), "/bin/sh", "-c", `printf 'hw.ncpu: 4\n'; exit 3`)
	require.Error(t, err)
	assert.Equal(t, "hw.ncpu: 4\n", out)
}

func TestExecCommandForcesCLocale(t *testing.T) {
	out, err := execCommand(context.Background(), "/bin/sh", "-c", `printf '%s' "$LC_ALL"`)
	require.NoError(t, err)
	assert.Equal(t, "C", out, "decimal points in top/sysctl output must not be localised")
}

func TestStatfsFreeReportsVolumeSpace(t *testing.T) {
	n, err := statfsFree(t.TempDir())
	require.NoError(t, err)
	assert.Greater(t, n, uint64(0))
	_, err = statfsFree("/definitely/not/a/real/path")
	assert.Error(t, err)
}
