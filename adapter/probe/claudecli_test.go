package probe

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner is a CommandRunner that replays canned stdout lines and captures
// the spec it was invoked with.
type fakeRunner struct {
	lines    []string
	waitErr  error
	startErr error
	gotSpec  claudecli.RunSpec
}

func (f *fakeRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	f.gotSpec = spec
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range f.lines {
			ch <- l
		}
	}()
	return ch, func() error { return f.waitErr }, nil
}

// Happy-path / edge-case: a clean `claude --version` exit reports Reachable +
// ModelsUnsupported with no models, and the probe invokes the right command.
func TestClaudeCLIProbe_ReachableModelsUnsupported(t *testing.T) {
	fr := &fakeRunner{lines: []string{"claude 1.2.3"}}
	p := newClaudeCLIProbe(Deps{Runner: fr})

	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.True(t, res.ModelsUnsupported)
	assert.Empty(t, res.Models)

	assert.Equal(t, "claude", fr.gotSpec.Bin)
	assert.Equal(t, []string{"--version"}, fr.gotSpec.Args)
}

// Edge-case: a provider with no listing endpoint is success, not failure.
func TestClaudeCLIProbe_NoListingEndpoint_NotAnError(t *testing.T) {
	fr := &fakeRunner{}
	p := newClaudeCLIProbe(Deps{Runner: fr})
	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{})
	require.NoError(t, err)
	assert.True(t, res.ModelsUnsupported)
}

// Input-validation: a custom binary path is honoured.
func TestClaudeCLIProbe_CustomBinaryPath(t *testing.T) {
	fr := &fakeRunner{}
	p := newClaudeCLIProbe(Deps{Runner: fr, ClaudeBinaryPath: "/opt/claude"})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{})
	require.NoError(t, err)
	assert.Equal(t, "/opt/claude", fr.gotSpec.Bin)
}

// Error-handling: a missing binary (start error) classifies as ErrUnreachable.
func TestClaudeCLIProbe_BinaryMissing_Unreachable(t *testing.T) {
	fr := &fakeRunner{startErr: errors.New(`exec: "claude": not found`)}
	p := newClaudeCLIProbe(Deps{Runner: fr})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
}

// Error-handling: a non-zero exit (wait error) classifies as ErrUnreachable.
func TestClaudeCLIProbe_NonZeroExit_Unreachable(t *testing.T) {
	fr := &fakeRunner{waitErr: errors.New("claude exited with error: exit status 1")}
	p := newClaudeCLIProbe(Deps{Runner: fr})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
}
