package process_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestScriptBundleChecker_RejectsEscapingSymlinkAndAcceptsSelfContainedFolder(t *testing.T) {
	base := t.TempDir()
	owner := uuid.New()
	dir := filepath.Join(base, "users", owner.String(), "wf", "scripts")
	writeFile(t, filepath.Join(dir, "run.sh"), "echo OK", 0755)
	writeFile(t, filepath.Join(dir, "helper.sh"), "helper", 0644)
	checker := process.NewScriptBundleChecker(filesystem.NewFileReader(base))
	require.NoError(t, checker.CheckBundleable(context.Background(), owner, "wf", "scripts/run.sh"))
	require.NoError(t, os.Symlink("../../secret", filepath.Join(dir, "leak")))
	require.ErrorIs(t, checker.CheckBundleable(context.Background(), owner, "wf", "scripts/run.sh"), workflow.ErrBundleOutsideReference)
	require.Error(t, checker.CheckBundleable(context.Background(), uuid.New(), "wf", "scripts/run.sh"), "must not read another owner's script")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, checker.CheckBundleable(ctx, owner, "wf", "scripts/run.sh"), context.Canceled)
}
