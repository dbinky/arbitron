package runnerproto

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The wire package stays importable by the runner without the server domain:
// standard library plus github.com/google/uuid only.
func TestRunnerproto_DependsOnlyOnStdlibAndUUID(t *testing.T) {
	out, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	require.NoError(t, err)
	for _, p := range strings.Fields(string(out)) {
		assert.Contains(t, []string{"github.com/arbitron/arbitron/adapter/runnerproto", "github.com/google/uuid"}, p)
	}
}
