package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const controlsOverlay = `{"model":"lunaroute/glm-5.3","small_model":"lunaroute/glm-5.3","enabled_providers":["lunaroute"],"provider":{"lunaroute":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://gw.lunaroute.com/v1","apiKey":"{env:LUNAROUTE_API_KEY}"},"models":{"glm-5.3":{"name":"glm-5.3","tool_call":true,"limit":{"context":202752,"output":32768}}}}}}`

func effectiveControlsFixture() string {
	return strings.ReplaceAll(controlsOverlay, "{env:LUNAROUTE_API_KEY}", "***")
}

func TestAgentEvidenceOpenCodeEffectiveControls(t *testing.T) {
	proof, err := validateOpenCodeEffectiveControls([]byte(controlsOverlay), []byte(effectiveControlsFixture()))
	require.NoError(t, err)
	assert.Equal(t, "glm-5.3", proof.Model)
	assert.Equal(t, 202752, proof.ContextTokens)
	assert.Equal(t, 32768, proof.OutputTokens)
	raw, err := json.Marshal(proof)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "apiKey")
	assert.NotContains(t, string(raw), "***")
	// Inherited foreign definitions may remain merged, but cannot be enabled.
	merged := strings.Replace(effectiveControlsFixture(), `"provider":{`, `"provider":{"foreign":{"options":{"apiKey":"private-debug-value"}},`, 1)
	proof, err = validateOpenCodeEffectiveControls([]byte(controlsOverlay), []byte(merged))
	require.NoError(t, err)
	raw, err = json.Marshal(proof)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-debug-value")
}

func TestAgentEvidenceOpenCodeEffectiveControlsRefusesDrift(t *testing.T) {
	for name, body := range map[string]string{
		"empty": "", "null": "null", "malformed": "{", "trailing": effectiveControlsFixture() + `{}`,
		"duplicate":         strings.Replace(effectiveControlsFixture(), `"model":`, `"model":"foreign/other","model":`, 1),
		"main":              strings.Replace(effectiveControlsFixture(), `"model":"lunaroute/glm-5.3"`, `"model":"foreign/other"`, 1),
		"helper":            strings.Replace(effectiveControlsFixture(), `"small_model":"lunaroute/glm-5.3"`, `"small_model":"foreign/other"`, 1),
		"enabled":           strings.Replace(effectiveControlsFixture(), `["lunaroute"]`, `["lunaroute","foreign"]`, 1),
		"npm":               strings.Replace(effectiveControlsFixture(), "@ai-sdk/openai-compatible", "@ai-sdk/anthropic", 1),
		"destination":       strings.Replace(effectiveControlsFixture(), hostedRoot, "https://foreign.example", 1),
		"auth_not_redacted": strings.Replace(effectiveControlsFixture(), "***", captureFixtureKey, 1),
		"tools":             strings.Replace(effectiveControlsFixture(), `"tool_call":true`, `"tool_call":false`, 1),
		"context":           strings.Replace(effectiveControlsFixture(), "202752", "202753", 1),
		"output":            strings.Replace(effectiveControlsFixture(), "32768", "32769", 1),
		"missing_model":     strings.Replace(effectiveControlsFixture(), `"glm-5.3":{`, `"other":{`, 1),
		"oversized":         string(bytes.Repeat([]byte(" "), openCodeControlsLimit+1)),
	} {
		t.Run(name, func(t *testing.T) {
			proof, err := validateOpenCodeEffectiveControls([]byte(controlsOverlay), []byte(body))
			require.Error(t, err)
			assert.Empty(t, proof)
			assert.NotContains(t, err.Error(), captureFixtureKey)
			assert.NotContains(t, err.Error(), "foreign.example")
		})
	}
	for name, overlay := range map[string]string{
		"missing": "", "unknown_limits": strings.Replace(controlsOverlay, "202752", "0", 1),
		"literal_key": strings.Replace(controlsOverlay, "{env:LUNAROUTE_API_KEY}", captureFixtureKey, 1),
		"revoked":     strings.ReplaceAll(controlsOverlay, "glm-5.3", "claude-sonnet-5-lr1"),
	} {
		t.Run("emitted/"+name, func(t *testing.T) {
			_, err := validateOpenCodeEffectiveControls([]byte(overlay), []byte(effectiveControlsFixture()))
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
		})
	}
}

type controlsRunnerFunc func(context.Context, claudecli.RunSpec) (<-chan string, func() error, error)

func (f controlsRunnerFunc) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	return f(ctx, spec)
}

func TestAgentEvidenceOpenCodeControlsLaunchBoundary(t *testing.T) {
	base := claudecli.RunSpec{Bin: "selected-binary", Args: []string{"run", "prompt-nonce", "-m", "lunaroute/glm-5.3", "--pure"}, Dir: "selected-workspace", Stdin: "stdin-nonce", Env: []string{"LUNAROUTE_API_KEY=" + captureFixtureKey, "OPENCODE_CONFIG_CONTENT=" + controlsOverlay}, MaxResponseBytes: 1234}
	for _, name := range []string{"missing-pure", "wrong-model", "missing-key", "empty-key", "duplicate-key", "missing-overlay", "duplicate-overlay", "missing-cwd"} {
		t.Run(name, func(t *testing.T) {
			spec := base
			switch name {
			case "missing-pure":
				spec.Args = spec.Args[:len(spec.Args)-1]
			case "wrong-model":
				spec.Args = []string{"run", "prompt", "-m", "foreign/other", "--pure"}
			case "missing-key":
				spec.Env = spec.Env[1:]
			case "empty-key":
				spec.Env = append([]string{"LUNAROUTE_API_KEY="}, spec.Env[1:]...)
			case "duplicate-key":
				spec.Env = append(append([]string{}, spec.Env...), spec.Env[0])
			case "missing-overlay":
				spec.Env = spec.Env[:1]
			case "duplicate-overlay":
				spec.Env = append(append([]string{}, spec.Env...), spec.Env[1])
			case "missing-cwd":
				spec.Dir = ""
			}
			runner := controlsRunnerFunc(func(context.Context, claudecli.RunSpec) (<-chan string, func() error, error) {
				t.Error("invalid emitted launch reached debug subprocess")
				return nil, nil, errAgentEvidence
			})
			_, err := queryOpenCodeEffectiveControls(context.Background(), runner, spec)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
		})
	}
	calls := 0
	runner := controlsRunnerFunc(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
		calls++
		require.Equal(t, base.Bin, spec.Bin)
		require.Equal(t, base.Dir, spec.Dir)
		require.Equal(t, base.Env, spec.Env)
		require.Equal(t, []string{"debug", "config", "--pure"}, spec.Args)
		require.Empty(t, spec.Stdin)
		require.EqualValues(t, openCodeControlsLimit, spec.MaxResponseBytes)
		_, bounded := ctx.Deadline()
		require.True(t, bounded)
		ch := make(chan string, 1)
		ch <- effectiveControlsFixture()
		close(ch)
		return ch, func() error { return nil }, nil
	})
	_, err := queryOpenCodeEffectiveControls(context.Background(), runner, base)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = queryOpenCodeEffectiveControls(canceled, runner, base)
	require.Error(t, err, "a completed debug payload cannot override cancellation")
	assert.Equal(t, "stdin-nonce", base.Stdin)
	assert.Equal(t, "run", base.Args[0])
}

// The installed debug command needs no gateway access or private credential.
// This schema/precedence check does not verify tool completion or helper routing.
func TestAgentEvidenceOpenCodeInstalledDebugSchema(t *testing.T) {
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("installed debug schema unavailable; runtime verification remains pending")
	}
	root := t.TempDir()
	ws := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(ws, 0700))
	hostile := `{"$schema":"https://opencode.ai/config.json","model":"foreign/other","small_model":"foreign/helper","enabled_providers":["foreign"],"provider":{"lunaroute":{"npm":"@ai-sdk/anthropic","options":{"baseURL":"https://foreign.example/v1","apiKey":"foreign-key"},"models":{"glm-5.3":{"name":"wrong","tool_call":false,"limit":{"context":1,"output":2}}}}},"permission":{"read":"allow"}}`
	seeds := map[string]string{
		filepath.Join(root, "config", "opencode", "opencode.json"): hostile,
		filepath.Join(ws, "opencode.json"):                         `{"$schema":"https://opencode.ai/config.json","model":"foreign/project","small_model":"foreign/project-helper","permission":{"write":"allow"}}`,
	}
	for path, body := range seeds {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "OPENCODE_DISABLE_MODELS_FETCH=true", "LUNAROUTE_API_KEY=" + captureFixtureKey, "OPENCODE_CONFIG_CONTENT=" + controlsOverlay}
	spec := claudecli.RunSpec{Bin: bin, Args: []string{"run", "not launched", "-m", "lunaroute/glm-5.3", "--pure"}, Dir: ws, Env: env, MaxResponseBytes: captureBodyLimit}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Control: both inherited layers must actually load without the overlay.
	control := spec
	control.Args = []string{"debug", "config", "--pure"}
	control.Env = env[:len(env)-1]
	lines, err := runCapturedLines(ctx, claudecli.NewExecRunner(), control)
	require.NoError(t, err)
	var inherited struct {
		Model      string            `json:"model"`
		SmallModel string            `json:"small_model"`
		Permission map[string]string `json:"permission"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.Join(lines, "\n")), &inherited))
	assert.Equal(t, "foreign/project", inherited.Model)
	assert.Equal(t, "foreign/project-helper", inherited.SmallModel)
	assert.Equal(t, "allow", inherited.Permission["read"])
	assert.Equal(t, "allow", inherited.Permission["write"])
	proof, err := queryOpenCodeEffectiveControls(ctx, claudecli.NewExecRunner(), spec)
	require.NoError(t, err, "installed effective-control schema/precedence changed; no inference was attempted")
	assert.Equal(t, openCodeControlProof{Model: "glm-5.3", ToolCall: true, ContextTokens: 202752, OutputTokens: 32768}, proof)
	for path, body := range seeds {
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, body, string(after), "seeded user/project config must not be changed")
	}
}

// This is the actual production Factory -> adapter -> runner -> TLS -> workspace
// seam, not a caller-supplied projection of a hypothetical configuration.
func TestAgentEvidenceOpenCodeControlsFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "controls-helper", "controls-limit", "controls-malformed", "controls-secret-exit", "controls-overflow"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "opencode", mode)
			client, _ := agentFactoryProbeClient(t)
			factory := liveFactoryWithClient(root, bin, agentFactoryRules(t), client)
			ws, err := os.MkdirTemp(root, "mission-")
			require.NoError(t, err)
			result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "opencode"), ws)
			if mode == "success" {
				require.NoError(t, err)
				require.NotNil(t, result)
				raw, err := os.ReadFile(filepath.Join(ws, agentProofFile))
				require.NoError(t, err)
				assert.Equal(t, agentProofText, string(raw))
				require.Len(t, capture.Records(), 1)
				require.NoError(t, validateCapturedInference(capture.Records(), "glm-5.3", []string{"main"}))
				marker, err := os.ReadFile(filepath.Join(root, "controls-marker"))
				require.NoError(t, err, "the same-emitted-env debug query must actually run")
				assert.Equal(t, "debug config --pure", string(marker))
			} else {
				require.Error(t, err)
				assert.Nil(t, result)
				assert.Empty(t, capture.Records())
				_, statErr := os.Stat(filepath.Join(root, "launch-marker"))
				require.True(t, os.IsNotExist(statErr), "bad controls must stop the actual mission launch")
				_, statErr = os.Stat(filepath.Join(ws, agentProofFile))
				require.True(t, os.IsNotExist(statErr))
				marker, readErr := os.ReadFile(filepath.Join(root, "controls-marker"))
				require.NoError(t, readErr, "refusal must exercise the intended debug response")
				assert.Equal(t, "debug config --pure", string(marker))
				assert.NotContains(t, err.Error(), captureFixtureKey)
			}
		})
	}
}
