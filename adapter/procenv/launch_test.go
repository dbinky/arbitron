package procenv_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/stretchr/testify/require"
)

func TestUnsupervised_StartsTheCommandAsIs(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	finish, err := procenv.Unsupervised{}.Start(context.Background(), cmd)
	require.NoError(t, err)
	require.NoError(t, cmd.Wait())
	finish()
	require.Equal(t, "/bin/sh", cmd.Path, "the unsupervised launcher never rewrites the command")
}
