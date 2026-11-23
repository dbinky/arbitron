package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

// namedRunner answers with its own name so the test can tell runners apart.
type namedRunner string

func (n namedRunner) Run(context.Context, runner.Assignment, runner.StepJob) (runner.StepOutput, error) {
	return runner.StepOutput{Chat: &engine.ChatResponse{Output: string(n)}}, nil
}

// lookupRegistry answers GetRunner from its table only (other Registry
// methods are unused and would panic through the nil embedded interface).
type lookupRegistry struct {
	runner.Registry
	runners map[runner.RunnerID]runner.Runner
	err     error
	asked   []runner.RunnerID
}

func (r *lookupRegistry) GetRunner(_ context.Context, id runner.RunnerID) (runner.Runner, []runner.Source, error) {
	r.asked = append(r.asked, id)
	if r.err != nil {
		return runner.Runner{}, nil, r.err
	}
	got, ok := r.runners[id]
	if !ok {
		return runner.Runner{}, nil, runner.ErrRunnerNotFound
	}
	return got, nil, nil
}

func resolve(t *testing.T, set runner.RunnerSet, id runner.RunnerID) (string, bool) {
	t.Helper()
	sr, ok := set.For(id)
	if !ok {
		return "", false
	}
	out, err := sr.Run(context.Background(), runner.Assignment{}, runner.StepJob{})
	require.NoError(t, err)
	return out.Chat.Output.(string), true
}

func TestHubRunnerSet_RoutesBuiltInLocallyAndRegisteredRemotesToTheHub(t *testing.T) {
	reg := &lookupRegistry{runners: map[runner.RunnerID]runner.Runner{"mac-a": {ID: "mac-a"}, "mac-b": {ID: "mac-b"}}}
	set := runner.NewHubRunnerSet(namedRunner("builtin"), namedRunner("remote"), reg)

	got, ok := resolve(t, set, runner.BuiltInRunnerID)
	assert.True(t, ok)
	assert.Equal(t, "builtin", got)
	assert.Empty(t, reg.asked, "the built-in runner needs no registry lookup")

	for _, id := range []runner.RunnerID{"mac-a", "mac-b"} {
		got, ok = resolve(t, set, id)
		assert.True(t, ok, id)
		assert.Equal(t, "remote", got, id)
	}
	_, ok = resolve(t, set, "ghost")
	assert.False(t, ok, "an unregistered runner has no step runner")
	_, ok = resolve(t, set, "")
	assert.False(t, ok)
}

func TestHubRunnerSet_FailsClosed(t *testing.T) {
	down := &lookupRegistry{err: errors.New("database is locked")}
	_, ok := runner.NewHubRunnerSet(namedRunner("builtin"), namedRunner("remote"), down).For("mac-a")
	assert.False(t, ok, "a registry failure never guesses a runner")

	odd := &lookupRegistry{runners: map[runner.RunnerID]runner.Runner{"other-builtin": {ID: "other-builtin", BuiltIn: true}}}
	_, ok = runner.NewHubRunnerSet(namedRunner("builtin"), namedRunner("remote"), odd).For("other-builtin")
	assert.False(t, ok, "a built-in row under another id is never treated as remote")
}

func TestNewHubRunnerSet_PanicsOnNil(t *testing.T) {
	reg := &lookupRegistry{}
	assert.Panics(t, func() { runner.NewHubRunnerSet(nil, namedRunner("r"), reg) })
	assert.Panics(t, func() { runner.NewHubRunnerSet(namedRunner("b"), nil, reg) })
	assert.Panics(t, func() { runner.NewHubRunnerSet(namedRunner("b"), namedRunner("r"), nil) })
}
