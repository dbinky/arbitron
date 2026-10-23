package pi

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolFreeProbe is the canned `pi --version` reply of the release whose
// --no-tools/--no-mcp flags were verified.
func toolFreeProbe() runScript { return runScript{lines: []string{minPiToolFreeVersion}} }

func pinnedProbeBinding() provider.ResolvedBinding {
	b := piBinding("claude-sonnet-4-6", provider.EffortHigh)
	b.Pinned = true
	return b
}

// Happy/success: the probe launches the same binary, credential env and model
// argv a real mission does, with every tool and MCP server disabled, the prompt
// on stdin and the probe dir as cwd, and confirms only from the assistant
// message_end provider/model.
func TestUsabilityPiProbeConfirmsFromAssistantMessage(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{toolFreeProbe(), {lines: []string{sessionHeader("pi-ses-1"), modelMessage("assistant", "anthropic", "claude-sonnet-4-6")}}}}
	got, session, err := newProvider(mr, pinnedProbeBinding()).ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", got)
	assert.Equal(t, "pi-ses-1", session, "proof carries pi's own session id")
	require.Len(t, mr.calls, 2)
	spec := mr.calls[1]
	assert.Equal(t, []string{"--print", "--mode", "json", "--no-session", "--model", "claude-sonnet-4-6", "--thinking", "high", "--no-tools", "--no-mcp", "--no-extensions", "--offline"}, spec.Args)
	assert.Equal(t, "pi", spec.Bin)
	assert.Equal(t, "say ok", spec.Stdin)
	assert.Equal(t, "/tmp/probe", spec.Dir)
	assert.Contains(t, spec.Env, "ANTHROPIC_API_KEY=sk-ant-test")
}

// Failure: plausible success on another model or provider, a missing identity,
// a refused turn, a non-zero exit or malformed output never confirms, and the
// error never carries provider output.
func TestUsabilityPiProbeRejectsUnprovenExecution(t *testing.T) {
	const secret = "sk-ant-test"
	refused := `{"type":"message_end","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-6","stopReason":"error","errorMessage":"rejected ` + secret + `"}}`
	for name, s := range map[string]runScript{
		"fallback model":  {lines: []string{modelMessage("assistant", "anthropic", "claude-haiku-4-5")}},
		"other provider":  {lines: []string{modelMessage("assistant", "openai", "claude-sonnet-4-6")}},
		"missing model":   {lines: []string{modelMessage("assistant", "anthropic", "")}},
		"user echo":       {lines: []string{modelMessage("user", "anthropic", "claude-sonnet-4-6")}},
		"no output":       {},
		"refused turn":    {lines: []string{refused}},
		"non-zero exit":   {lines: []string{modelMessage("assistant", "anthropic", "claude-sonnet-4-6")}, waitErr: errors.New("exit status 1: " + secret)},
		"malformed":       {lines: []string{`{"type":`}},
		"launch fails":    {startErr: errors.New("exec " + secret)},
		"fallback later":  {lines: []string{modelMessage("assistant", "anthropic", "claude-sonnet-4-6"), modelMessage("assistant", "anthropic", "claude-haiku-4-5")}},
		"fallback before": {lines: []string{modelMessage("assistant", "anthropic", "claude-haiku-4-5"), modelMessage("assistant", "anthropic", "claude-sonnet-4-6")}},
		"no session":      {lines: []string{modelMessage("assistant", "anthropic", "claude-sonnet-4-6")}},
	} {
		t.Run(name, func(t *testing.T) {
			if len(s.lines) > 0 && name != "no session" {
				s.lines = append([]string{sessionHeader("pi-ses-1")}, s.lines...)
			}
			mr := &multiRunner{scripts: []runScript{toolFreeProbe(), s}}
			got, session, err := newProvider(mr, pinnedProbeBinding()).ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
			require.Error(t, err)
			assert.Empty(t, got)
			assert.Empty(t, session)
			assert.NotContains(t, err.Error(), secret)
			assert.Len(t, mr.calls, 2)
		})
	}
}

// Error/edge: an unpinned binding, an empty work dir, an empty model, an unknown
// credential provider and a pi older than the verified tool-free flags are all
// refused before any probe turn launches.
func TestUsabilityPiProbeRefusesBeforeLaunch(t *testing.T) {
	noModel := pinnedProbeBinding()
	noModel.Model = ""
	unknownProvider := pinnedProbeBinding()
	unknownProvider.Provider = "mystery"
	for name, tc := range map[string]struct {
		binding provider.ResolvedBinding
		workDir string
		version runScript
		wantErr error
	}{
		"empty work dir":   {binding: pinnedProbeBinding(), version: toolFreeProbe(), wantErr: ErrWorkDirRequired},
		"empty model":      {binding: noModel, workDir: "/tmp/probe", version: toolFreeProbe(), wantErr: ErrModelRequired},
		"unknown provider": {binding: unknownProvider, workDir: "/tmp/probe", version: toolFreeProbe()},
		"pi too old":       {binding: pinnedProbeBinding(), workDir: "/tmp/probe", version: compatibleProbe(), wantErr: ErrIncompatiblePi},
	} {
		t.Run(name, func(t *testing.T) {
			mr := &multiRunner{scripts: []runScript{tc.version, {lines: []string{modelMessage("assistant", "anthropic", "claude-sonnet-4-6")}}}}
			got, _, err := newProvider(mr, tc.binding).ProbeExecutedModel(context.Background(), "say ok", tc.workDir)
			require.Error(t, err)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			}
			assert.Empty(t, got)
			for _, c := range mr.calls {
				assert.Equal(t, []string{"--version"}, c.Args, "no probe turn may launch")
			}
		})
	}
}

// Edge: the tool-free floor does not change the floor for regular missions.
func TestUsabilityPiProbeFloorLeavesMissionsAlone(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	_, err := newProvider(mr, piBinding("anthropic/claude-sonnet-4-6", provider.EffortDefault)).RunAgent(context.Background(), mission())
	require.NoError(t, err)
}

// Version stdout and runner diagnostics are untrusted too. Returning the
// ensureCompatible error directly would expose them before the turn starts.
func TestUsabilityPiProbeVersionErrorsAreSecretFree(t *testing.T) {
	const secret = "version-diagnostic-secret"
	for name, version := range map[string]runScript{
		"malformed stdout": {lines: []string{secret}},
		"launch failure":   {startErr: errors.New("exec: " + secret)},
		"exit failure":     {lines: []string{"1.3.0"}, waitErr: errors.New("stderr: " + secret)},
	} {
		t.Run(name, func(t *testing.T) {
			mr := &multiRunner{scripts: []runScript{version}}
			p := newProvider(mr, pinnedProbeBinding())
			for range 2 {
				model, session, err := p.ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
				require.Error(t, err)
				assert.NotContains(t, err.Error(), secret)
				assert.Empty(t, model)
				assert.Empty(t, session)
			}
			require.Len(t, mr.calls, 1, "memoized version failure never launches a turn or retries")
			assert.Equal(t, []string{"--version"}, mr.calls[0].Args)
		})
	}
}
