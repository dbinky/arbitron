// domain/runner/sync_adopt_test.go
package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An adopted job's attempt is rebuilt from its exact live claim and the
// committed base; nothing is moved, restored or claimed anew.
func TestWorkspaceSync_AdoptAttemptRebuildsTheExactClaim(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	f.write(t, "a.txt", "written before the restart")

	got, err := f.sync.AdoptAttempt(context.Background(), a, f.job())
	require.NoError(t, err)
	assert.Equal(t, runner.WorkspaceAttempt{RunID: f.run, WorkDir: f.dir(), Runner: builtin, Base: base, Resource: att.Resource}, got)
	assert.Equal(t, "written before the restart", f.read(t, "a.txt"), "adoption never restores")
}

func TestWorkspaceSync_AdoptAttemptRefusesAJobWithoutTheClaim(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	holder := f.reserve(t, builtin)
	f.begin(t, holder, stay(builtin))
	other := f.reserve(t, builtin)

	_, err := f.sync.AdoptAttempt(context.Background(), other, f.job())
	require.ErrorIs(t, err, runner.ErrResourceFenced)
}
