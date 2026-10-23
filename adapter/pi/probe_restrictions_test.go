package pi

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type restrictionRunner struct {
	outcome   string
	ordinary  bool
	turns     int
	validated int
}

func (r *restrictionRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	output := func(lines ...string) (<-chan string, func() error, error) {
		ch := make(chan string, len(lines))
		for _, line := range lines {
			ch <- line
		}
		close(ch)
		return ch, func() error { return nil }, nil
	}
	if slices.Equal(spec.Args, []string{"--version"}) {
		return output("1.3.0")
	}
	r.turns++
	flag := func(name, value string) bool {
		i := slices.Index(spec.Args, name)
		return i >= 0 && i+1 < len(spec.Args) && spec.Args[i+1] == value
	}
	if spec.Bin != "pi" || spec.Dir != "/tmp/probe" || spec.Stdin != "say ok" || !flag("--model", "claude-sonnet-4-6") || !flag("--thinking", "high") || !flag("--mode", "json") || !slices.Contains(spec.Args, "--print") || !slices.Contains(spec.Args, "--no-session") || !slices.Contains(spec.Env, "ANTHROPIC_API_KEY=sk-ant-test") {
		return nil, nil, fmt.Errorf("missing real launch inputs")
	}
	for _, f := range []string{"--no-tools", "--no-mcp", "--no-extensions", "--offline"} {
		if slices.Contains(spec.Args, f) == r.ordinary {
			return nil, nil, fmt.Errorf("incorrect probe-only restriction %s", f)
		}
	}
	r.validated++
	if r.outcome == "rejected restrictions" {
		return nil, nil, fmt.Errorf("unsupported restriction: sk-ant-test")
	}
	actual := "claude-sonnet-4-6"
	if r.outcome == "wrong model" {
		actual = "fallback"
	}
	return output(sessionHeader("pi-ses-1"), modelMessage("assistant", "anthropic", actual))
}

func TestModelProbePiRestrictions(t *testing.T) {
	for _, outcome := range []string{"exact", "wrong model", "rejected restrictions"} {
		t.Run(outcome, func(t *testing.T) {
			runner := &restrictionRunner{outcome: outcome}
			p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "pi", Runner: runner})
			got, session, err := p.ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
			if outcome == "exact" {
				require.NoError(t, err)
				assert.Equal(t, "claude-sonnet-4-6", got)
				assert.Equal(t, "pi-ses-1", session)
			} else {
				require.Error(t, err)
				assert.Empty(t, got)
				assert.Empty(t, session)
				assert.NotContains(t, err.Error(), "sk-ant-test")
			}
			assert.Equal(t, 1, runner.turns, "never retry with weaker restrictions")
			assert.Equal(t, 1, runner.validated, "negative controls must reach the intended outcome, not fail on absent launch inputs")
		})
	}
}

func TestModelProbePiRestrictionsLeaveOrdinaryRunsUnchanged(t *testing.T) {
	runner := &restrictionRunner{ordinary: true}
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "pi", Runner: runner})
	result, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "say ok", WorkDir: "/tmp/probe"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", result.Model)
	assert.Equal(t, 1, runner.turns)
	assert.Equal(t, 1, runner.validated)
}
