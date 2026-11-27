package http

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerConnectCopies_Translate(t *testing.T) {
	run := uuid.New()
	p := &runner.DeleteCopyPayload{RunID: run, HomeGeneration: 4, Reason: "workspace_removed"}
	w := toWireDeleteCopy(p)
	assert.Equal(t, runnerproto.DeleteCopy{RunID: run.String(), HomeGeneration: 4, Reason: "workspace_removed"}, w)
	// Exercise the production outbound switch, not just the helper.
	env, err := fromOutbound(runner.Outbound{Kind: runner.MsgDeleteCopy, DeleteCopy: p}, "mac-1", 5)
	require.NoError(t, err)
	assert.Equal(t, runnerproto.KindDeleteCopy, env.Kind)
	var decoded runnerproto.DeleteCopy
	require.NoError(t, runnerproto.DecodePayload(env, &decoded))
	assert.Equal(t, w, decoded)
	held, err := fromWireCopies([]runnerproto.WireCopy{{RunID: run.String(), HomeGeneration: 2}})
	require.NoError(t, err)
	assert.Equal(t, []runner.HeldCopyInfo{{RunID: run, HomeGeneration: 2}}, held)
	info, err := helloInfo(runnerproto.Hello{Copies: []runnerproto.WireCopy{{RunID: run.String(), HomeGeneration: 2}}})
	require.NoError(t, err)
	assert.Equal(t, held, info.Copies)
	_, err = helloInfo(runnerproto.Hello{Copies: []runnerproto.WireCopy{{RunID: "bad"}}})
	require.Error(t, err)
	_, err = fromWireCopies([]runnerproto.WireCopy{{RunID: "bad"}})
	require.Error(t, err)
}
