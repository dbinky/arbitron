package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *jobFixture) localCopy() string {
	return filepath.Join(f.localRoot, "instances", f.runID.String())
}

func (f *jobFixture) assertNoTransfer(t *testing.T) {
	t.Helper()
	assert.Zero(t, f.srv.ManifestCalls(), "no manifest was fetched")
	assert.Empty(t, f.srv.Fetches(), "no file was pulled")
	assert.Zero(t, f.srv.Commits(), "nothing was pushed")
}

func TestWorkspaceExecutor_MissingResourceClaimRejectedBeforeAccept(t *testing.T) {
	cases := map[string]func(*runneragent.WorkspaceJob){
		"no claim token":     func(j *runneragent.WorkspaceJob) { j.FenceToken = "" },
		"no pinned base":     func(j *runneragent.WorkspaceJob) { j.BaseRef = "" },
		"claim of other run": func(j *runneragent.WorkspaceJob) { j.FenceKey = "run:" + uuid.NewString() },
		"zero generation":    func(j *runneragent.WorkspaceJob) { j.FenceGeneration = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newJobFixture(t, nil)
			ws := f.plan(t)
			bad := *ws
			mutate(&bad)
			d := runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: &bad, Process: &runneragent.ProcessJob{
				Kind: "tool_call", Command: "sh", Args: []string{"-c", "echo x > touched.txt"}, Dir: bad.WorkDir, WorkDir: bad.WorkDir}}

			ok, reason := f.jobs.Supports(d)
			assert.False(t, ok, "incomplete scope evidence is never accepted")
			assert.True(t, strings.HasPrefix(reason, "workspace_preparation_failed: "), reason)

			res := f.jobs.Execute(context.Background(), d)
			assert.False(t, res.OK, "Execute re-checks and fails closed")
			assert.True(t, strings.HasPrefix(res.ErrorText, "workspace_preparation_failed: "), res.ErrorText)
			f.assertNoTransfer(t)
			_, err := os.Stat(f.localCopy())
			assert.True(t, os.IsNotExist(err), "no local copy was created")
		})
	}
}

func TestWorkspaceExecutor_UnsupportedCommandNeverMutatesWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell")
	}
	ctx := context.Background()
	f := newJobFixture(t, nil)
	ws := f.plan(t)
	local, err := f.syncer.Pull(ctx, *ws) // a clean committed copy already exists
	require.NoError(t, err)

	for _, p := range []*runneragent.ProcessJob{
		{Kind: "tool_call", Command: "definitely-not-installed-xyz", Dir: ws.WorkDir, WorkDir: ws.WorkDir},
		{Kind: "tool_call", Command: "sh", Args: []string{"-c", "echo x > input.txt"}, Requires: []string{"definitely-not-installed-req"}, Dir: ws.WorkDir, WorkDir: ws.WorkDir},
	} {
		f.srv.ResetCounters()
		d := runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: p}
		ok, reason := f.jobs.Supports(d)
		assert.False(t, ok)
		assert.True(t, strings.HasPrefix(reason, "command_unavailable: "), reason)
		res := f.jobs.Execute(ctx, d)
		assert.False(t, res.OK)
		assert.True(t, strings.HasPrefix(res.ErrorText, "command_unavailable: "), res.ErrorText)
		f.assertNoTransfer(t)
		b, err := os.ReadFile(filepath.Join(local, "input.txt"))
		require.NoError(t, err)
		assert.Equal(t, "in", string(b), "the rejected dispatch never ran or restored anything")
	}

	// The copy was never marked dirty, so the next supported step reuses it.
	f.srv.ResetCounters()
	res := f.jobs.Execute(ctx, runneragent.Dispatch{Op: runneragent.OpProcess, Workspace: ws, Process: &runneragent.ProcessJob{
		Kind: "tool_call", Command: "sh", Args: []string{"-c", `printf '{"saw":"%s"}' "$(cat input.txt)"`}, Dir: ws.WorkDir, WorkDir: ws.WorkDir}})
	require.True(t, res.OK, res.ErrorText)
	assert.JSONEq(t, `{"saw":"in"}`, res.Process.Stdout)
	assert.Empty(t, f.srv.Fetches(), "the clean copy is reused: zero pulls")
	assert.Zero(t, f.srv.ManifestCalls())
}
