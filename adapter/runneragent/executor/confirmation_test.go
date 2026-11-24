package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recConfirmations struct {
	got  []provider.ResolvedBinding
	dirs []string
	res  runner.ConfirmationResult
	err  error
}

func (r *recConfirmations) Confirm(_ context.Context, b provider.ResolvedBinding, dir string) (runner.ConfirmationResult, error) {
	r.got = append(r.got, b)
	r.dirs = append(r.dirs, dir)
	return r.res, r.err
}
func confirmDispatch() runneragent.Dispatch {
	return runneragent.Dispatch{JobID: "6b1f3c1e-5d2a-4c5b-9e0f-7a8b9c0d1e2f", Generation: 1, Op: runneragent.OpConfirm, Confirmation: &runneragent.ConfirmationJob{Model: "claude-opus-5-5", Backend: "claude-code", Provider: "claude-cli", ContextWindow: "1m", Effort: "high"}}
}
func TestExecutor_ConfirmRunsLocalNativeVerifierInPerJobScratch(t *testing.T) {
	scratch := t.TempDir()
	conf := &recConfirmations{res: runner.ConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}}
	e, err := New(&fakeSelector{}, Config{ScratchRoot: scratch, Confirmations: conf})
	require.NoError(t, err)
	ok, reason := e.Supports(confirmDispatch())
	require.True(t, ok, reason)
	res := e.Execute(context.Background(), confirmDispatch(), nil)
	require.True(t, res.OK, res.ErrorText)
	assert.Equal(t, &conf.res, res.Confirmation)
	require.Len(t, conf.got, 1)
	assert.Equal(t, provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "claude-code", Model: "claude-opus-5-5", ContextWindow: provider.Context1M, Effort: provider.EffortHigh}, conf.got[0])
	assert.Equal(t, scratch, filepath.Dir(conf.dirs[0]))
	_, err = os.Stat(conf.dirs[0])
	assert.True(t, os.IsNotExist(err))
	conf.res.ExecutedModel = "claude-sonnet-4-5"
	res = e.Execute(context.Background(), confirmDispatch(), nil)
	assert.False(t, res.OK)
	assert.Equal(t, runner.OutcomeOther, res.Outcome.Class)
	assert.Nil(t, res.Confirmation)
}
func TestExecutor_ConfirmFailsClosedWithoutVerifierOrDescriptor(t *testing.T) {
	scratch := t.TempDir()
	conf := &recConfirmations{}
	none, err := New(&fakeSelector{}, Config{ScratchRoot: scratch})
	require.NoError(t, err)
	ok, _ := none.Supports(confirmDispatch())
	assert.False(t, ok)
	e, err := New(&fakeSelector{}, Config{ScratchRoot: scratch, Confirmations: conf})
	require.NoError(t, err)
	for name, d := range map[string]runneragent.Dispatch{
		"no descriptor":    {JobID: "j", Generation: 1, Op: runneragent.OpConfirm},
		"opencode backend": func() runneragent.Dispatch { d := confirmDispatch(); d.Confirmation.Backend = "opencode"; return d }(),
		"no model":         func() runneragent.Dispatch { d := confirmDispatch(); d.Confirmation.Model = ""; return d }(),
	} {
		ok, _ := e.Supports(d)
		assert.False(t, ok, name)
		assert.False(t, e.Execute(context.Background(), d, nil).OK, name)
	}
	assert.Empty(t, conf.got)
	var typed *recConfirmations
	_, err = New(&fakeSelector{}, Config{ScratchRoot: scratch, Confirmations: typed})
	assert.Error(t, err)
}
