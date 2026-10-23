package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These reduced envelopes follow the source-recorded run/export schemas in
// testdata/model-evidence.md. They are not recordings of a successful model call.
const exactRunEvent = `{"type":"text","sessionID":"ses_run","part":{"id":"prt_text","sessionID":"ses_run","messageID":"msg_answer","type":"text","text":"verified output"}}`

func modelExport(session, message, role, providerID, model string) string {
	b, _ := json.Marshal(map[string]any{
		"info": map[string]any{"id": session},
		"messages": []any{map[string]any{"info": map[string]any{
			"id": message, "sessionID": session, "role": role,
			"providerID": providerID, "modelID": model, "time": map[string]int{"created": 1, "completed": 2},
		}, "parts": []any{}}},
	})
	return string(b)
}

func combineModelExports(t *testing.T, exports ...string) string {
	t.Helper()
	var combined struct {
		Info     json.RawMessage   `json:"info"`
		Messages []json.RawMessage `json:"messages"`
	}
	for _, raw := range exports {
		var data struct {
			Info     json.RawMessage   `json:"info"`
			Messages []json.RawMessage `json:"messages"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &data))
		combined.Info = data.Info
		combined.Messages = append(combined.Messages, data.Messages...)
	}
	raw, err := json.Marshal(combined)
	require.NoError(t, err)
	return string(raw)
}

type evidenceRunner struct {
	t          *testing.T
	dir        string
	run        []string
	export     string
	exportErr  error
	calls      []claudecli.RunSpec
	modelRef   string
	credential string
	endpoint   string
}

func (r *evidenceRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls = append(r.calls, spec)
	var lines []string
	var waitErr error
	switch {
	case slices.Equal(spec.Args, []string{"--version"}):
		lines = []string{"opencode " + minOpencodeVersion}
	case len(spec.Args) > 0 && spec.Args[0] == "run":
		valid := assert.Equal(r.t, []string{"run", "do the thing", "-m", r.modelRef, "--dangerously-skip-permissions", "--format", "json"}, spec.Args)
		valid = assert.Equal(r.t, r.dir, spec.Dir) && valid
		valid = assert.Contains(r.t, spec.Env, r.credential) && valid
		cfg, ok := envValue(spec.Env, "OPENCODE_CONFIG_CONTENT")
		valid = assert.True(r.t, ok) && valid
		valid = assert.Contains(r.t, cfg, `"baseURL":"`+r.endpoint+`"`) && valid
		valid = assert.Contains(r.t, cfg, `"permission":{"*":"deny","read":"allow"}`) && valid
		if valid {
			lines = r.run
		}
	case slices.Equal(spec.Args, []string{"export", "ses_run"}):
		require.GreaterOrEqual(r.t, len(r.calls), 3)
		run := r.calls[len(r.calls)-2]
		valid := assert.Equal(r.t, run.Env, spec.Env)
		valid = assert.Equal(r.t, run.Dir, spec.Dir) && valid
		valid = assert.Equal(r.t, run.Bin, spec.Bin) && valid
		valid = assert.Equal(r.t, int64(1<<20), spec.MaxResponseBytes) && valid
		deadline, ok := ctx.Deadline()
		valid = assert.True(r.t, ok, "export must be bounded even without a mission timeout") && valid
		valid = assert.WithinDuration(r.t, time.Now().Add(30*time.Second), deadline, time.Second) && valid
		if valid {
			lines = strings.Split(r.export, "\n")
		}
		waitErr = r.exportErr
	default:
		r.t.Errorf("unexpected invocation: %v", spec.Args)
	}
	ch := make(chan string, len(lines))
	for _, line := range lines {
		ch <- line
	}
	close(ch)
	return ch, func() error { return waitErr }, nil
}

func TestModelProbeOpenCodeExactExecution(t *testing.T) {
	exact := modelExport("ses_run", "msg_answer", "assistant", "anthropic", "claude-sonnet-4-5")
	wrong := modelExport("ses_run", "msg_answer", "assistant", "anthropic", "fallback")
	extraWrong := modelExport("ses_run", "msg_other", "assistant", "anthropic", "fallback")
	extraExact := modelExport("ses_run", "msg_other", "assistant", "anthropic", "claude-sonnet-4-5")
	extraRun := strings.ReplaceAll(exactRunEvent, "msg_answer", "msg_other")
	for name, tc := range map[string]struct {
		run       []string
		export    string
		exportErr error
		valid     bool
	}{
		"exact":                 {[]string{exactRunEvent}, exact, nil, true},
		"pretty printed export": {[]string{exactRunEvent}, strings.ReplaceAll(exact, ",", ",\n"), nil, true},
		"wrong model":           {[]string{exactRunEvent}, wrong, nil, false},
		"wrong provider":        {[]string{exactRunEvent}, modelExport("ses_run", "msg_answer", "assistant", "openai", "claude-sonnet-4-5"), nil, false},
		"missing model":         {[]string{exactRunEvent}, modelExport("ses_run", "msg_answer", "assistant", "anthropic", ""), nil, false},
		"missing provider":      {[]string{exactRunEvent}, modelExport("ses_run", "msg_answer", "assistant", "", "claude-sonnet-4-5"), nil, false},
		"user request echo":     {[]string{exactRunEvent}, modelExport("ses_run", "msg_answer", "user", "anthropic", "claude-sonnet-4-5"), nil, false},
		"unrelated session":     {[]string{exactRunEvent}, modelExport("ses_other", "msg_answer", "assistant", "anthropic", "claude-sonnet-4-5"), nil, false},
		"unrelated turn":        {[]string{exactRunEvent}, modelExport("ses_run", "msg_earlier", "assistant", "anthropic", "claude-sonnet-4-5"), nil, false},
		"missing run session":   {[]string{strings.ReplaceAll(exactRunEvent, `"ses_run"`, `""`)}, exact, nil, false},
		"missing run message":   {[]string{strings.ReplaceAll(exactRunEvent, `"msg_answer"`, `""`)}, exact, nil, false},
		"mixed run sessions":    {[]string{exactRunEvent, strings.ReplaceAll(exactRunEvent, "ses_run", "ses_other")}, exact, nil, false},
		"malformed run":         {[]string{exactRunEvent, `{"type":`}, exact, nil, false},
		"truncated object":      {[]string{`"type":"error","secret-evidence":true}`, exactRunEvent}, exact, nil, false},
		"array after exact":     {[]string{exactRunEvent, `[{"type":"error"}]`}, exact, nil, false},
		"scalar after exact":    {[]string{exactRunEvent, `null`}, exact, nil, false},
		"text after exact":      {[]string{exactRunEvent, `secret-evidence`}, exact, nil, false},
		"blank lines":           {[]string{"", "  \t", exactRunEvent, ""}, exact, nil, true},
		"empty run":             {nil, exact, nil, false},
		"empty export":          {[]string{exactRunEvent}, "", nil, false},
		"malformed export":      {[]string{exactRunEvent}, `{`, nil, false},
		"trailing JSON":         {[]string{exactRunEvent}, exact + exact, nil, false},
		"incomplete assistant":  {[]string{exactRunEvent}, strings.ReplaceAll(exact, `"completed":2`, `"completed":0`), nil, false},
		"errored assistant":     {[]string{exactRunEvent}, strings.Replace(exact, `"role":"assistant"`, `"role":"assistant","error":{"name":"APIError"}`, 1), nil, false},
		"export failed":         {[]string{exactRunEvent}, exact, errors.New("secret-evidence"), false},
		"oversized export":      {[]string{exactRunEvent}, exact + strings.Repeat(" ", 1<<20), nil, false},
		"multiple exact steps":  {[]string{exactRunEvent, extraRun}, combineModelExports(t, exact, extraExact), nil, true},
		"missing later step":    {[]string{exactRunEvent, extraRun}, exact, nil, false},
		"fallback after exact":  {[]string{exactRunEvent, extraRun}, combineModelExports(t, exact, extraWrong), nil, false},
		"exact after fallback":  {[]string{extraRun, exactRunEvent}, combineModelExports(t, extraWrong, exact), nil, false},
		"duplicate message":     {[]string{exactRunEvent}, combineModelExports(t, exact, exact), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			runner := &evidenceRunner{t: t, dir: dir, run: tc.run, export: tc.export, exportErr: tc.exportErr,
				modelRef: "anthropic/claude-sonnet-4-5", credential: "ANTHROPIC_API_KEY=secret-evidence", endpoint: "https://api.anthropic.com"}
			binding := anthropicBinding("secret-evidence", nil)
			binding.Model, binding.Pinned, binding.AllowedTools = "anthropic/claude-sonnet-4-5", true, []string{"Read"}
			p := New(Config{Binding: binding, BinaryPath: "opencode", Runner: runner})
			result, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "do the thing", WorkDir: dir, Model: binding.Model})
			if tc.valid {
				require.NoError(t, err)
				require.NotNil(t, result)
				var expectedTexts []string
				for _, line := range tc.run {
					if strings.TrimSpace(line) != "" {
						expectedTexts = append(expectedTexts, "verified output")
					}
				}
				assert.Equal(t, strings.Join(expectedTexts, "\n"), result.Summary)
				assert.Equal(t, binding.Model, result.Model)
				require.Len(t, runner.calls, 3, "execution proof must come from the correlated export")
			} else {
				require.Error(t, err, "plausible success/request echo is not model execution evidence")
				assert.ErrorIs(t, err, ErrModelExecutionEvidence)
				assert.Nil(t, result)
				assert.NotContains(t, err.Error(), "secret-evidence")
			}
		})
	}
}

func TestModelProbeOpenCodeSlashInNativeID(t *testing.T) {
	binding := anthropicBinding("key", nil)
	binding.Provider, binding.Model, binding.Pinned = "openrouter", "openrouter/deepseek/deepseek-v4-flash", true
	binding.BaseURL, binding.AllowedTools = "https://openrouter.ai/api/v1", []string{"Read"}
	dir := t.TempDir()
	runner := &evidenceRunner{t: t, dir: dir, run: []string{exactRunEvent},
		export:   modelExport("ses_run", "msg_answer", "assistant", "openrouter", "deepseek/deepseek-v4-flash"),
		modelRef: binding.Model, credential: "OPENROUTER_API_KEY=key", endpoint: "https://openrouter.ai/api/v1"}
	result, err := New(Config{Binding: binding, BinaryPath: "opencode", Runner: runner}).RunAgent(context.Background(), engine.AgentMission{Prompt: "do the thing", WorkDir: dir, Model: binding.Model})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, runner.calls, 3)
	model, ok := argValue(runner.calls[1].Args, "-m")
	require.True(t, ok)
	assert.Equal(t, binding.Model, model)
}
