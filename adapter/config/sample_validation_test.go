package config_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileKindChecker is a workflow.ProfileChecker backed by the loaded default
// profile registry. It maps a node's model_profile alias to the registry's
// capability kind so validateModelProfiles enforces the llm_call→chat /
// agent_call→agent contract against the SAME profiles the sample ships with.
type profileKindChecker map[string]string

func (p profileKindChecker) ProfileKind(alias string) (string, bool) {
	k, ok := p[alias]
	return k, ok
}

// allFilesExist is a permissive workflow.FileChecker: the sample references
// copy/skill artifacts by relative path, which are not the subject of this test
// (Task 2 is about the codex backend being a valid agent backend and the
// profile kinds matching). Treating every referenced file as present isolates
// the model-profile/backend validation pass.
type allFilesExist struct{}

func (allFilesExist) FileExists(string, string) bool { return true }

// TestInterviewerProfile_ResolvesToCodexBinding verifies the full chain
// defaults → Load → ProfilesFromConfig → StaticProfileResolver → Resolve
// for the "interviewer" alias. The agent factory and stream selector consume
// the ResolvedBinding produced by Resolve, not the raw ProfileConfig struct,
// so this test closes the gap between "the profile is loaded" (covered by
// TestLoad_DefaultProfileRegistry_FeaturePipelineMixedModel) and "the profile
// RESOLVES to a codex binding that the factory will dispatch correctly".
//
// The subscription path (empty ConnectionRef) sets Provider = "claude-cli",
// Backend = "codex", and Model = "gpt-5.5"; the agent factory dispatches on
// Backend alone, so it routes to adapter/codex.New regardless of Provider.
func TestInterviewerProfile_ResolvesToCodexBinding(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver, err := adapterprovider.NewStaticProfileResolver(profiles, nil)
	require.NoError(t, err, "default profile registry must construct a valid resolver")

	binding, err := resolver.Resolve(context.Background(), "interviewer")
	require.NoError(t, err, "interviewer alias must resolve (it is present in the default registry)")

	assert.Equal(t, provider.ProfileKindAgent, binding.Kind, "interviewer must be agent-kind")
	assert.Equal(t, "codex", binding.Backend, "interviewer must resolve to the codex backend")
	assert.Equal(t, "gpt-5.5", binding.Model, "interviewer must resolve to gpt-5.5")
	// The subscription path (empty ConnectionRef) never reveals a key: RevealKey
	// must return empty-string, not an error, so it is safe to call even on a
	// codex binding (which never needs a key — the assert protects the subscription-only invariant).
	key, keyErr := binding.RevealKey(context.Background())
	require.NoError(t, keyErr, "codex interviewer RevealKey must not error (subscription-only path)")
	assert.Empty(t, key, "codex interviewer must never carry an API key")
}

// TestPlannerCoderProfiles_ResolveToClaudeCodeBindings complements
// TestInterviewerProfile_ResolvesToCodexBinding by verifying the full resolver
// chain for the planner and coder profiles: both must resolve to
// Backend="claude-code", Kind="agent", Model="opus-4.8" with an empty key
// (subscription-only path). This confirms the mixed-model split in the default
// registry: interviewer→codex, planner/coder→claude-code. The stream selector
// dispatches on binding.Backend, so an incorrect backend value here would
// silently route planner/coder to the wrong stream adapter.
func TestPlannerCoderProfiles_ResolveToClaudeCodeBindings(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver, err := adapterprovider.NewStaticProfileResolver(profiles, nil)
	require.NoError(t, err)

	for _, alias := range []string{"planner", "coder"} {
		alias := alias
		t.Run(alias, func(t *testing.T) {
			binding, err := resolver.Resolve(context.Background(), alias)
			require.NoError(t, err, "%s alias must resolve", alias)
			assert.Equal(t, provider.ProfileKindAgent, binding.Kind, "%s must be agent-kind", alias)
			assert.Equal(t, "claude-code", binding.Backend, "%s must resolve to claude-code backend", alias)
			assert.Equal(t, "opus-4.8", binding.Model, "%s must resolve to opus-4.8", alias)
			key, keyErr := binding.RevealKey(context.Background())
			require.NoError(t, keyErr, "%s RevealKey must not error (subscription path)", alias)
			assert.Empty(t, key, "%s must not carry an API key (subscription-only)", alias)
		})
	}
}

// TestSampleFeaturePipeline_ValidatesWithDefaultProfiles is the Phase 3 Task 2
// acceptance: the committed samples/feature-pipeline/workflow.json still passes
// workflow.Validate against the default profile registry AFTER the mixed-model
// repoint (interviewer→codex, planner/coder→claude-code). codex is a valid
// agent backend (Phase 1 Task 1), and the interviewer/planner/coder profiles
// stay agent-kind while assessor stays chat-kind, so kind-matching holds.
func TestSampleFeaturePipeline_ValidatesWithDefaultProfiles(t *testing.T) {
	// Load the default registry (no deployment config.yaml → defaults).
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	kinds := profileKindChecker{}
	for _, p := range cfg.Profiles {
		kinds[p.Alias] = p.Kind
	}

	// Sanity: the mixed-model repoint is in effect for this validation.
	assert.Equal(t, "agent", kinds["interviewer"])
	assert.Equal(t, "agent", kinds["planner"])
	assert.Equal(t, "agent", kinds["coder"])
	assert.Equal(t, "chat", kinds["assessor"])

	// Load the committed sample workflow. The test runs in adapter/config, so the
	// repo root is two levels up.
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", "feature-pipeline", "workflow.json"))
	require.NoError(t, err, "sample feature-pipeline workflow must exist")

	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))

	// Validate with the real profile-kind pass and a permissive file checker.
	// ConnChecker is nil (the sample's agent profiles are subscription/no-key).
	errs := workflow.Validate(&wf, allFilesExist{}, kinds, nil)
	assert.Empty(t, errs, "sample feature-pipeline must validate with the mixed-model default profiles: %+v", errs)
}
