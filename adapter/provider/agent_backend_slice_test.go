package provider_test

// Vertical slice: workflow node (backend axis) → AgentCallExecutor → Factory
// → concrete adapter → subprocess env.
//
// Closes the gap between the two halves of the existing tests:
//   - executor_agent_backend_test.go mocks AgentProviderSelector, proving
//     precedence but never exercising the real factory dispatch.
//   - factory_claudecode_inject_test.go calls AgentProviderFor directly, proving
//     env injection but bypassing the executor's precedence chain.
//
// This slice wires REAL executor + REAL factory + REAL StaticProfileResolver.
// Only subprocess runners are replaced so tests run without installed CLIs.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// --- local runners -----------------------------------------------------------

// opencodeCaptureRunner handles the two-call opencode runner protocol:
// call 1 → version probe (returns "opencode 0.4.0"); call 2 → run (captures
// spec, returns canned output). The opencode.Provider memoizes the probe via
// sync.Once so the first call is always the probe for a fresh Provider instance.
type opencodeCaptureRunner struct {
	calls int
	spec  claudecli.RunSpec
}

func (r *opencodeCaptureRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls++
	ch := make(chan string, 2)
	if r.calls == 1 {
		ch <- "opencode 0.4.0" // compatible version line for the probe
	} else {
		r.spec = spec
		ch <- "Done." // canned run output
	}
	close(ch)
	return ch, func() error { return nil }, nil
}

// --- local file reader -------------------------------------------------------

// sliceFileReader satisfies engine.FileReader for slice tests. It ignores the
// owner UUID (equivalent to the engine's mockFileReader).
type sliceFileReader struct {
	files map[string][]byte
}

func newSliceFileReader() *sliceFileReader {
	return &sliceFileReader{files: map[string][]byte{}}
}

func (r *sliceFileReader) ReadFile(_ uuid.UUID, wfName, relPath string) ([]byte, error) {
	key := wfName + "/" + relPath
	if b, ok := r.files[key]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("slice: file not found: %q", key)
}

func (r *sliceFileReader) ResolvePath(_ uuid.UUID, wfName, relPath string) (string, error) {
	key := wfName + "/" + relPath
	if _, ok := r.files[key]; ok {
		return key, nil
	}
	return "", fmt.Errorf("slice: path not found: %q", key)
}

func (r *sliceFileReader) FileExists(_ uuid.UUID, wfName, relPath string) bool {
	_, ok := r.files[wfName+"/"+relPath]
	return ok
}

// --- local helpers -----------------------------------------------------------

// sliceWorkspace returns a VariableStore seeded with the reserved {{workspace}}
// variable so the executor's WorkDir validation passes.
func sliceWorkspace() *variable.VariableStore {
	vs := variable.NewStore()
	vs.SetReserved(engine.ReservedVarWorkspace, "/ws/inst")
	return vs
}

// sliceNode builds a workflow.Node for an agent_call with the given profile
// alias and node-level backend (pass "" to leave the node backend unset).
func sliceNode(alias, backend string) workflow.Node {
	return workflow.Node{
		ID:   "a1",
		Type: workflow.NodeTypeAgentCall,
		Config: workflow.NodeConfig{
			TemplateFile: "mission.txt",
			ModelProfile: alias,
			Backend:      backend,
		},
	}
}

// litellmProfile builds a profile + store pair with a real LiteLLM connection
// so the StaticProfileResolver exercises the full alias→connection chain.
func litellmProfile(alias, backend string) (provider.Profile, *fakeConnectionStore) {
	store := newFakeConnectionStore()
	conn := provider.Connection{
		Name:    "conn-litellm",
		Provider: "litellm",
		BaseURL:  "https://litellm.internal:4000",
	}
	store.put(conn, "sk-litellm-key")
	return provider.Profile{
		Alias:         alias,
		Kind:          provider.ProfileKindAgent,
		ConnectionRef: conn.Name,
		Model:         "gpt-4o",
		Backend:       backend,
	}, store
}

// subscriptionProfile builds a minimal no-connection profile (the claude-cli
// subscription path; ConnectionStore is nil).
func subscriptionProfile(alias string) provider.Profile {
	return provider.Profile{
		Alias: alias,
		Kind:  provider.ProfileKindAgent,
		Model: "claude-3-5-sonnet",
	}
}

// newSliceResolver builds a StaticProfileResolver for the given profiles and store.
func newSliceResolver(t *testing.T, profiles []provider.Profile, store provider.ConnectionStore) *adapterprovider.StaticProfileResolver {
	t.Helper()
	r, err := adapterprovider.NewStaticProfileResolver(profiles, store)
	require.NoError(t, err)
	return r
}

// runSlice wires the full stack (real executor + real factory) and executes a
// single agent node. Returns (result, err, claudeRunner, opencodeRunner) so
// callers can assert which subprocess path was taken.
func runSlice(
	t *testing.T,
	node workflow.Node,
	resolver provider.ProfileResolver,
	defaultBackend string,
) (*engine.NodeResult, error, *captureRunner, *opencodeCaptureRunner) {
	t.Helper()

	fr := newSliceFileReader()
	fr.files["slice-wf/mission.txt"] = []byte("do the task")

	claudeR := &captureRunner{}
	opencodeR := &opencodeCaptureRunner{}

	f := adapterprovider.NewFactory(adapterprovider.Deps{
		DefaultAgentBackend: defaultBackend,
		ClaudeAgentConfig:   claudecli.Config{BinaryPath: "claude", Runner: claudeR},
		Runner:              opencodeR,
		OpencodeBinaryPath:  "opencode",
	})
	exec := engine.NewAgentCallExecutor(fr, f, resolver, defaultBackend)
	res, err := exec.Execute(context.Background(), node, sliceWorkspace(), "slice-wf")
	return res, err, claudeR, opencodeR
}

// --- happy path: claude-code backend + LiteLLM connection → env injection ----

// TestBackendSlice_ClaudeCode_LiteLLM_EnvInjected is the primary happy path: a
// node with backend "claude-code" and a profile that resolves to a LiteLLM
// connection runs all the way through the executor → factory → claude-cli
// adapter and the subprocess env carries ANTHROPIC_BASE_URL (the gateway) and
// ANTHROPIC_API_KEY (the per-call revealed key).
func TestBackendSlice_ClaudeCode_LiteLLM_EnvInjected(t *testing.T) {
	profile, store := litellmProfile("agent-litellm", "claude-code")
	resolver := newSliceResolver(t, []provider.Profile{profile}, store)

	res, execErr, claudeR, opencodeR := runSlice(
		t, sliceNode("agent-litellm", ""), resolver, "claude-code",
	)
	require.NoError(t, execErr)
	require.NotNil(t, res)

	require.True(t, claudeR.called, "claude-code backend must route to the claude-cli runner")
	assert.Equal(t, 0, opencodeR.calls, "opencode runner must not be invoked for claude-code backend")

	joined := strings.Join(claudeR.spec.Env, "\n")
	assert.Contains(t, joined, "ANTHROPIC_BASE_URL=https://litellm.internal:4000",
		"LiteLLM base URL must be injected into the claude subprocess env")
	assert.Contains(t, joined, "ANTHROPIC_API_KEY=sk-litellm-key",
		"LiteLLM key (revealed per call) must appear in the subprocess env")
}

// --- success: node-level backend wins over profile backend end-to-end --------

// TestBackendSlice_NodeBackendOverridesProfile proves the full-stack precedence
// chain: a node with backend "opencode" wins over a profile that carries
// "claude-code", routing to the opencode runner (not the claude runner).
func TestBackendSlice_NodeBackendOverridesProfile(t *testing.T) {
	profile, store := litellmProfile("agent-profile", "claude-code") // profile says claude-code
	resolver := newSliceResolver(t, []provider.Profile{profile}, store)

	// Node says "opencode" — must override the profile's "claude-code".
	_, execErr, claudeR, opencodeR := runSlice(
		t, sliceNode("agent-profile", "opencode"), resolver, "claude-code",
	)
	require.NoError(t, execErr)

	assert.Greater(t, opencodeR.calls, 0,
		"node backend 'opencode' must override profile backend 'claude-code' and reach the opencode runner")
	assert.False(t, claudeR.called,
		"claude runner must not be invoked when the effective backend is 'opencode'")
}

// --- failure: opencode backend routes to opencode adapter (not claude-cli) ---

// TestBackendSlice_OpencodePath_RoutesCorrectly verifies that a profile with
// backend "opencode" and provider "litellm" routes to the opencode runner —
// proving the backend-NOT-provider invariant: provider is litellm but backend
// "opencode" determines the adapter.
func TestBackendSlice_OpencodePath_RoutesCorrectly(t *testing.T) {
	profile, store := litellmProfile("agent-opencode", "opencode")
	resolver := newSliceResolver(t, []provider.Profile{profile}, store)

	_, execErr, claudeR, opencodeR := runSlice(
		t, sliceNode("agent-opencode", ""), resolver, "opencode",
	)
	require.NoError(t, execErr)

	assert.Greater(t, opencodeR.calls, 0,
		"opencode backend must reach the opencode runner, even when provider is litellm")
	assert.False(t, claudeR.called,
		"claude runner must not be invoked when backend=opencode regardless of provider")
}

// --- error: unknown backend fails closed through the full executor path ------

// TestBackendSlice_UnknownBackend_ExecuteErrors asserts that a node carrying an
// unrecognized backend ("xyzzy") causes Execute to return an error wrapping
// ErrUnknownBackend — the factory's fail-closed policy propagates up through
// the executor.
func TestBackendSlice_UnknownBackend_ExecuteErrors(t *testing.T) {
	resolver := newSliceResolver(t, []provider.Profile{subscriptionProfile("sub")}, nil)

	_, execErr, _, _ := runSlice(
		t, sliceNode("sub", "xyzzy"), resolver, "",
	)
	require.Error(t, execErr, "unrecognized backend must cause Execute to return an error")
	assert.ErrorIs(t, execErr, adapterprovider.ErrUnknownBackend,
		"the error must wrap ErrUnknownBackend for callers to handle specifically")
}

// --- edge: all-empty backend chain fails closed (policy in composition root) -

// TestBackendSlice_AllEmptyBackend_ExecuteErrors asserts that when every level
// of the precedence chain is unset (node="", profile="", factory default=""),
// Execute returns an error. The deployment policy lives in the composition root
// via Deps.DefaultAgentBackend; an unconfigured deployment must fail closed, not
// silently fall through to claude-code or any other default.
func TestBackendSlice_AllEmptyBackend_ExecuteErrors(t *testing.T) {
	resolver := newSliceResolver(t, []provider.Profile{subscriptionProfile("sub")}, nil)

	_, execErr, _, _ := runSlice(
		t, sliceNode("sub", ""), resolver, "",
	)
	require.Error(t, execErr, "all-empty backend chain must fail closed")
}
