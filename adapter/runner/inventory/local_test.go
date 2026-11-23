package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type binRunner struct {
	byBin map[string][]string // bin -> --version output; missing bin = start failure
}

func (r binRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	out, ok := r.byBin[spec.Bin]
	if !ok || strings.Join(spec.Args, " ") != "--version" {
		return nil, nil, errors.New("exec: not found")
	}
	ch := make(chan string, len(out))
	for _, l := range out {
		ch <- l
	}
	close(ch)
	return ch, func() error { return nil }, nil
}

type lister struct {
	conns []provider.Connection
	err   error
}

func (l lister) List(context.Context) ([]provider.Connection, error) { return l.conns, l.err }

func TestDetectLocal_FullHost(t *testing.T) {
	r := binRunner{byBin: map[string][]string{"/opt/claude": {"2.1.328 (Claude Code)"}, "/opt/codex": {"codex-cli 0.163.0"}}}
	in, err := DetectLocal(context.Background(), Config{
		ClaudeBinary: "/opt/claude", Codex: codex.Config{BinaryPath: "/opt/codex", Runner: r}, Runner: r,
		Connections: lister{conns: []provider.Connection{{Name: "lr", Provider: "lunaroute"}}},
		LookPath: func(name string) (string, error) {
			if name == "pi" {
				return "/opt/pi", nil
			}
			return "", errors.New("not found")
		},
	})
	require.NoError(t, err)
	require.NotNil(t, in.ClaudeCLI)
	assert.Equal(t, "2.1.328", in.ClaudeCLI.Version)
	assert.True(t, in.ClaudeCLI.Supported)
	require.NotNil(t, in.Codex)
	assert.Equal(t, "0.163.0", in.Codex.Version)
	assert.False(t, in.Codex.Supported, "below the capability floor: annotated, not removed")
	assert.NotEmpty(t, in.Codex.Reason)
	assert.Equal(t, []string{"pi"}, in.LocalBackends)
	assert.Len(t, in.Connections, 1)
}

func TestDetectLocal_MissingBinaries(t *testing.T) {
	r := binRunner{byBin: map[string][]string{}}
	in, err := DetectLocal(context.Background(), Config{
		ClaudeBinary: "/nope/claude", Codex: codex.Config{BinaryPath: "/nope/codex", Runner: r}, Runner: r,
		Connections: lister{}, LookPath: func(string) (string, error) { return "", errors.New("nf") },
	})
	require.NoError(t, err)
	require.NotNil(t, in.ClaudeCLI, "explicit path counts as present; version failure marks it unsupported")
	assert.False(t, in.ClaudeCLI.Supported)
	assert.Nil(t, in.Codex, "missing codex is absent, not incompatible")
	assert.Empty(t, in.LocalBackends)
}

func TestDetectLocal_ConnectionListErrorFails(t *testing.T) {
	_, err := DetectLocal(context.Background(), Config{Runner: binRunner{}, Connections: lister{err: errors.New("db")}, LookPath: func(string) (string, error) { return "", errors.New("nf") }})
	require.Error(t, err)
	_, err = DetectLocal(context.Background(), Config{})
	assert.ErrorIs(t, err, ErrNoConnectionLister)
}
