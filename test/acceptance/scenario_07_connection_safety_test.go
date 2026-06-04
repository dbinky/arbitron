package acceptance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
)

// TestAcceptance_07_ConnectionSafety_EnvInjectionAndSecrecy proves spec §8 #7,
// Scenario A: a connection's key is resolvable from a profile and injected into
// the opencode subprocess for a non-Claude agent, yet the key appears in NEITHER
// any log output NOR the workflow files.
//
// Requires: Phase 2 AES-GCM store + lazy RevealKey + log-redaction; Phase 3
// opencode env-injection.
//
// LOAD-BEARING (design §6): the explicit log-capture secrecy assertion. The
// buffer-backed slog logger installed by the harness captures ALL log output
// across the entire resolve-and-run sequence; it is asserted to contain NO
// substring of the sentinel key. Log references to the connection use its name.
func TestAcceptance_07_ConnectionSafety_EnvInjectionAndSecrecy(t *testing.T) {
	runner := newFakeRunner().withFallback("Editing.", "Done.")
	transport := newFakeTransport(true) // assessor reports done → completes
	b := openWeightBinding(runner, transport)
	h := newHarness(t, b)

	res := h.runWorkedExample(t, 5)
	require.Equal(t, instance.StateCompleted, res.instance.State)

	// --- Delivery: the sentinel key reached the opencode child process env ---
	var opencodeEnv []string
	recorded := runner.recordedSpecs()
	for i := range recorded {
		if v, ok := envValue(recorded[i].Env, "OPENROUTER_API_KEY"); ok && v == opencodeKey {
			opencodeEnv = recorded[i].Env
			break
		}
	}
	require.NotNil(t, opencodeEnv, "the opencode subprocess env must carry the connection's sealed key")
	key, _ := envValue(opencodeEnv, "OPENROUTER_API_KEY")
	assert.Equal(t, opencodeKey, key, "the env-injected key equals the sealed connection key")

	// --- LOAD-BEARING #7: the sentinel key never appears in any log line -----
	logs := h.logBuf.String()
	assert.NotContains(t, logs, opencodeKey,
		"the opencode connection key must never appear in any log output")
	assert.NotContains(t, logs, openrouterKey,
		"the openrouter connection key must never appear in any log output")
	// Log references to a connection use its NAME, not its key (the resolve path
	// names connections by name in any diagnostics). The opencode connection name
	// is safe to surface; the key is not.
	assert.NotContains(t, logs, "sk-SENTINEL", "no sentinel-key prefix may leak into logs")

	// --- The key never appears in workflow.json / the instance snapshot ------
	snapJSON, err := json.Marshal(res.instance.WorkflowSnapshot)
	require.NoError(t, err)
	assert.NotContains(t, string(snapJSON), opencodeKey,
		"the WorkflowSnapshot must contain no connection key")
	assert.NotContains(t, string(snapJSON), "sk-SENTINEL",
		"the workflow files hold only model_profile aliases, never secrets")
	// The graph holds only the alias references.
	assert.Contains(t, string(snapJSON), "\"model_profile\":\"editor\"")
	assert.Contains(t, string(snapJSON), "\"model_profile\":\"assessor\"")

	// And the on-disk workflow.json itself carries no key (it is pure data).
	wfBytes, err := h.fileReader.ReadFile(h.defaultUser, workedExample, "workflow.json")
	require.NoError(t, err)
	assert.NotContains(t, string(wfBytes), "sk-SENTINEL")
	assert.NotContains(t, string(wfBytes), "api-key")
	assert.NotContains(t, string(wfBytes), "connectionRef",
		"connections are not named in the graph file")

	// --- Persisted execution trace: keys must never enter the variable store ---
	// The lazy RevealKey contract: keys are decrypted only at the moment the
	// adapter injects them into the subprocess env or HTTP headers — never earlier.
	// If this invariant broke, a key would appear in the variable store and be
	// persisted to every downstream record. Verify all three layers:
	ctx := context.Background()

	// 1. Final instance variables (written after each node by UpdateVariables).
	instVarsJSON, err := json.Marshal(res.instance.Variables)
	require.NoError(t, err)
	assert.NotContains(t, string(instVarsJSON), "sk-SENTINEL",
		"final instance variables must never contain a connection key")

	// 2. Variable snapshots: pre-execution state captured before each node runs.
	snaps, err := h.snapshots.ListByInstance(ctx, res.instance.ID)
	require.NoError(t, err)
	for i, snap := range snaps {
		snapJSON, _ := json.Marshal(snap.Variables)
		assert.NotContains(t, string(snapJSON), "sk-SENTINEL",
			"variable snapshot[%d] (node=%s) must not contain a connection key", i, snap.NodeID)
	}

	// 3. Node execution records: resolved inputs and outputs written per attempt.
	execs, err := h.nodeExecs.ListByInstance(ctx, res.instance.ID)
	require.NoError(t, err)
	for i, ex := range execs {
		inputsJSON, _ := json.Marshal(ex.ResolvedInputs)
		outputJSON, _ := json.Marshal(ex.Output)
		assert.NotContains(t, string(inputsJSON), "sk-SENTINEL",
			"nodeExec[%d] (node=%s) resolved inputs must not contain a connection key", i, ex.NodeID)
		assert.NotContains(t, string(outputJSON), "sk-SENTINEL",
			"nodeExec[%d] (node=%s) output must not contain a connection key", i, ex.NodeID)
	}
}

// TestAcceptance_07_ConnectionSafety_MultiRegion proves spec §8 #7, Scenario B:
// two Azure connections with different endpoints + keys, bound to two profiles,
// each resolves to the CORRECT endpoint + key with no cross-contamination.
//
// Requires: Phase 2 resolution + RevealKey + the AES-GCM store; the two-Azure
// topology. Resolution is exercised through the REAL StaticProfileResolver over
// the REAL connection store (sealed at rest), and the lazy RevealKey is invoked
// for each binding to prove no cross-region key bleed.
func TestAcceptance_07_ConnectionSafety_MultiRegion(t *testing.T) {
	const (
		eastKey = "sk-SENTINEL-azure-EAST-DO-NOT-LOG"
		westKey = "sk-SENTINEL-azure-WEST-DO-NOT-LOG"
	)
	runner := newFakeRunner()
	b := binding{
		name: "multi-region-azure",
		profiles: []provider.Profile{
			{Alias: "assessor-east", Kind: provider.ProfileKindChat, ConnectionRef: "azure-east", Model: "gpt-4o-east"},
			{Alias: "assessor-west", Kind: provider.ProfileKindChat, ConnectionRef: "azure-west", Model: "llama-3-west"},
		},
		connections: []seedConn{
			{
				conn: provider.Connection{
					Name: "azure-east", Provider: "azure-foundry",
					BaseURL: "https://east.example.azure.com",
					Extras:  map[string]string{"api-version": "2024-02-01"},
				},
				key: eastKey,
			},
			{
				conn: provider.Connection{
					Name: "azure-west", Provider: "azure-foundry",
					BaseURL: "https://west.example.azure.com",
					Extras:  map[string]string{"api-version": "2024-06-01"},
				},
				key: westKey,
			},
		},
		runner: runner,
	}
	h := newHarness(t, b)
	ctx := context.Background()

	// Resolve each profile and reveal its key — each must map to its OWN endpoint
	// and key, with no cross-region contamination.
	east, err := h.resolver.Resolve(ctx, "assessor-east")
	require.NoError(t, err)
	assert.Equal(t, "https://east.example.azure.com", east.BaseURL)
	assert.Equal(t, "gpt-4o-east", east.Model)
	assert.Equal(t, "2024-02-01", east.Extras["api-version"])
	eastRevealed, err := east.RevealKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, eastKey, eastRevealed, "east profile resolves to the east key")

	west, err := h.resolver.Resolve(ctx, "assessor-west")
	require.NoError(t, err)
	assert.Equal(t, "https://west.example.azure.com", west.BaseURL)
	assert.Equal(t, "llama-3-west", west.Model)
	assert.Equal(t, "2024-06-01", west.Extras["api-version"])
	westRevealed, err := west.RevealKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, westKey, westRevealed, "west profile resolves to the west key")

	// No cross-contamination: the east binding never yields the west key/endpoint.
	assert.NotEqual(t, eastRevealed, westRevealed, "each region resolves a distinct key")
	assert.NotEqual(t, east.BaseURL, west.BaseURL, "each region resolves a distinct endpoint")

	// Secrecy still holds: neither revealed key leaked into the log buffer.
	logs := h.logBuf.String()
	assert.NotContains(t, logs, eastKey)
	assert.NotContains(t, logs, westKey)
	assert.NotContains(t, logs, "sk-SENTINEL")
}
