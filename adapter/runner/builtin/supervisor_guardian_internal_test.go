package builtin

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuardian_ServerDeathBeforeReleaseRunsNothing(t *testing.T) {
	worked := filepath.Join(t.TempDir(), "worked")
	cmd := exec.Command("/bin/sh", "-c", `touch "$0"`, worked)
	goR, goW, err := armGuardian(cmd)
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_ = goR.Close()
	_ = goW.Close() // the server dies: its end of the release pipe closes without "go"
	err = cmd.Wait()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, guardianAbortExit, exit.ExitCode())
	assert.NoFileExists(t, worked, "an unreleased guardian never runs the real command")
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setpgid, "the guardian leads its own process group")
}

func TestGuardian_ReleaseExecsTheRealCommandInPlace(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command("/bin/sh", "-c", `echo $$ > "$0"; echo "$1"`, pidFile, "arg with space")
	var out bytes.Buffer
	cmd.Stdout = &out
	goR, goW, err := armGuardian(cmd)
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_ = goR.Close()
	_, err = io.WriteString(goW, guardianGo)
	require.NoError(t, err)
	_ = goW.Close()
	require.NoError(t, cmd.Wait())
	assert.Equal(t, "arg with space\n", out.String(), "arguments survive the wrapper verbatim")
	b, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(cmd.Process.Pid), strings.TrimSpace(string(b)), "exec keeps the guardian pid, so the recorded pid is the worker")
}

func TestProcessStartMarker_IdentifiesALiveProcessAndReportsGone(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	require.NoError(t, cmd.Start())
	m1, err := processStartMarker(cmd.Process.Pid)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(m1, runtime.GOOS+":"), "marker %q", m1)
	m2, err := processStartMarker(cmd.Process.Pid)
	require.NoError(t, err)
	assert.Equal(t, m1, m2, "stable for one process")
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_, err = processStartMarker(cmd.Process.Pid)
	require.ErrorIs(t, err, errProcessGone)
}
