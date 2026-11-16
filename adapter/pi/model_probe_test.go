package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionHeader is the first line pi's json mode writes, even under --no-session.
func sessionHeader(id string) string {
	return `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-10-20T00:00:00.000Z","cwd":"/tmp/probe"}`
}

func modelMessage(role, providerID, model string) string {
	b, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{
		"role": role, "provider": providerID, "model": model, "stopReason": "stop",
		"content": []map[string]string{{"type": "text", "text": "ok"}},
	}})
	return string(b)
}

// This runner only emits the requested evidence if the real launch carries
// every load-bearing input. It never uses argv as model-execution evidence.
type modelRunner struct {
	t       *testing.T
	output  []string
	calls   int
	workdir string
	waitErr error
}

func (r *modelRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls++
	lines := []string{"0.87.0"}
	if !slices.Equal(spec.Args, []string{"--version"}) {
		expectedArgs := []string{"--print", "--mode", "json", "--no-session", "--model", "anthropic/uncatalogued-model"}
		valid := assert.Equal(r.t, expectedArgs, spec.Args)
		valid = assert.Equal(r.t, "pi", spec.Bin) && valid
		valid = assert.Equal(r.t, "do the thing", spec.Stdin) && valid
		valid = assert.Equal(r.t, r.workdir, spec.Dir) && valid
		valid = assert.Contains(r.t, spec.Env, "ANTHROPIC_API_KEY=sk-ant-test") && valid
		lines = []string{modelMessage("assistant", "anthropic", "missing-launch-input")}
		if valid {
			lines = r.output
		}
	}
	out := make(chan string, len(lines))
	for _, line := range lines {
		out <- line
	}
	close(out)
	return out, func() error {
		if slices.Equal(spec.Args, []string{"--version"}) {
			return nil
		}
		return r.waitErr
	}, nil
}

func TestModelProbePiLunaRouteReportedModelEvidence(t *testing.T) {
	binding := lrPiBinding()
	lines := []string{modelMessage("assistant", "lunaroute", binding.Model), modelMessage("assistant", "", binding.Model)}
	out, err := parseOutput(lines)
	require.NoError(t, err)
	require.NoError(t, out.verifyExecutedModel(binding))
	evidence := out.modelEvidence()
	require.Len(t, evidence, 2)
	assert.Equal(t, "lunaroute", evidence[0].Provider)
	assert.Empty(t, evidence[1].Provider)
	assert.Equal(t, binding.Model, evidence[1].Model)
	assert.Equal(t, "pi_message_end", evidence[1].Source)
	assert.Equal(t, engine.EvidenceReportedExecution, evidence[1].Kind)
	for _, wrong := range []string{"glm-5.3-flash", "claude-sonnet-5-lr1", "", "glm-" + lrPiKey} {
		out, err := parseOutput(append([]string{modelMessage("assistant", "lunaroute", wrong)}, lines...))
		require.NoError(t, err)
		require.ErrorIs(t, out.verifyExecutedModel(binding), ErrModelEvidence, "later exact matches cannot hide an invalid earlier identity")
	}
}

func TestModelProbePiExactExecution(t *testing.T) {
	exact := modelMessage("assistant", "anthropic", "uncatalogued-model")
	wrong := modelMessage("assistant", "anthropic", "fallback")
	for name, tc := range map[string]struct {
		output []string
		valid  bool
	}{
		"uncatalogued exact":               {[]string{exact}, true},
		"wrong model":                      {[]string{wrong}, false},
		"wrong provider":                   {[]string{modelMessage("assistant", "openai", "uncatalogued-model")}, false},
		"missing model":                    {[]string{modelMessage("assistant", "anthropic", "")}, false},
		"missing provider":                 {[]string{modelMessage("assistant", "", "uncatalogued-model")}, false},
		"empty output":                     {nil, false},
		"user echo":                        {[]string{modelMessage("user", "anthropic", "uncatalogued-model")}, false},
		"delta only":                       {[]string{`{"type":"message_update","message":{"role":"assistant","provider":"anthropic","model":"uncatalogued-model"}}`}, false},
		"transcript only":                  {[]string{`{"type":"agent_end","messages":[{"role":"assistant","provider":"anthropic","model":"uncatalogued-model"}]}`}, false},
		"fallback after exact":             {[]string{exact, wrong}, false},
		"exact after fallback":             {[]string{wrong, exact}, false},
		"missing proof on earlier message": {[]string{modelMessage("assistant", "", ""), exact}, false},
		"malformed":                        {[]string{exact, `{"type":`}, false},
	} {
		t.Run(name, func(t *testing.T) {
			workdir := t.TempDir()
			runner := &modelRunner{t: t, output: tc.output, workdir: workdir}
			binding := piBinding("anthropic/uncatalogued-model", provider.EffortDefault)
			binding.Pinned = true
			p := New(Config{Binding: binding, BinaryPath: "pi", Runner: runner})
			m := mission()
			m.WorkDir = workdir
			result, err := p.RunAgent(context.Background(), m)
			if tc.valid {
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, "ok", result.Summary)
				assert.Equal(t, binding.Model, result.Model)
			} else {
				require.Error(t, err, "request echo or plausible success is not execution proof")
				assert.Nil(t, result, "unverified output must not reach the result sink")
			}
			assert.Equal(t, 2, runner.calls)
		})
	}
}

func TestModelProbePiFailureErrorsAreSecretFree(t *testing.T) {
	const secret = "sk-ant-test"
	exitErr := errors.New("exit status 1")
	for _, reason := range []string{"error", "aborted"} {
		for _, waitErr := range []error{nil, exitErr} {
			for _, laterSuccess := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/nonzero=%v/laterSuccess=%v", reason, waitErr != nil, laterSuccess), func(t *testing.T) {
					failure, err := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{
						"role": "assistant", "provider": "anthropic", "model": "uncatalogued-model",
						"stopReason": reason, "errorMessage": "credential rejected: " + secret,
						"content": []map[string]string{{"type": "text", "text": "unaccepted output"}},
					}})
					require.NoError(t, err)
					lines := []string{string(failure)}
					if laterSuccess {
						lines = append(lines, modelMessage("assistant", "anthropic", "uncatalogued-model"))
					}
					workdir := t.TempDir()
					runner := &modelRunner{t: t, output: lines, workdir: workdir, waitErr: waitErr}
					binding := piBinding("anthropic/uncatalogued-model", provider.EffortDefault)
					binding.Pinned = true
					p := New(Config{Binding: binding, BinaryPath: "pi", Runner: runner})
					m := mission()
					m.WorkDir = workdir
					result, err := p.RunAgent(context.Background(), m)
					require.Error(t, err)
					assert.Nil(t, result, "failed output must not reach the sink")
					assert.Contains(t, err.Error(), binding.Model)
					assert.NotContains(t, err.Error(), secret)
					assert.NotContains(t, err.Error(), "credential rejected")
					if waitErr == nil {
						assert.ErrorIs(t, err, ErrTurnFailed)
					} else {
						assert.ErrorIs(t, err, exitErr)
					}
					assert.Equal(t, 2, runner.calls)
				})
			}
		}
	}
}

func TestModelProbePiSubscriptionExactExecution(t *testing.T) {
	binding := provider.ResolvedBinding{Provider: "claude-cli", Model: "openai-codex/gpt-5-sol", Pinned: true}
	for _, actual := range []string{"openai-codex", "openai"} {
		t.Run(actual, func(t *testing.T) {
			mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{modelMessage("assistant", actual, "gpt-5-sol")}}}}
			res, err := newProvider(mr, binding).RunAgent(context.Background(), mission())
			if actual == "openai-codex" {
				require.NoError(t, err)
				require.NotNil(t, res)
			} else {
				require.Error(t, err)
				assert.Nil(t, res)
			}
		})
	}
}

func TestModelProbePiSlashInNativeID(t *testing.T) {
	binding := piBinding("openrouter/deepseek/deepseek-v4-flash", provider.EffortDefault)
	binding.Provider, binding.Pinned = "openrouter", true
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{modelMessage("assistant", "openrouter", "deepseek/deepseek-v4-flash")}}}}
	result, err := newProvider(mr, binding).RunAgent(context.Background(), mission())
	require.NoError(t, err)
	assert.Equal(t, binding.Model, result.Model, "strip only the actual routing provider, not the native ID's slash prefix")
}
