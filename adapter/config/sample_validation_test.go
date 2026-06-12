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

// TestInterviewerProfile_ResolvesToClaudeCodeBinding verifies the full chain
// defaults → Load → ProfilesFromConfig → DynamicProfileResolver → Resolve for the
// "interviewer" alias. The agent factory and stream selector consume the
// ResolvedBinding produced by Resolve, not the raw ProfileConfig struct, so this
// test closes the gap between "the profile is loaded" and "the profile RESOLVES
// to a binding the factory dispatches correctly".
//
// The DEFAULT interviewer is claude-code with the subscription default model
// (model ""), so a fresh install runs the bundled feature-pipeline sample out of
// the box. The gpt-5.5/codex mixed-model split ships as a COMMENTED example in
// the sample config (PR #32 Must Fix #4: do not ship a default the authoring host
// could not run). The subscription path (empty ConnectionRef) reveals no key.
func TestInterviewerProfile_ResolvesToClaudeCodeBinding(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver := adapterprovider.NewDynamicProfileResolver(
		adapterprovider.NewProfileCacheFromProfiles(profiles...), nil)

	binding, err := resolver.Resolve(context.Background(), "interviewer")
	require.NoError(t, err, "interviewer alias must resolve (it is present in the default registry)")

	assert.Equal(t, provider.ProfileKindAgent, binding.Kind, "interviewer must be agent-kind")
	// The default interviewer sets no backend, so it resolves to the empty
	// (inherit) backend — floored to the deployment default (claude-code) by the
	// executor before dispatch. The key invariant this pins is that it is NOT the
	// codex backend (which would require codex installed+logged-in and a gpt-5.5
	// plan), so a fresh install runs out of the box.
	assert.Empty(t, binding.Backend, "default interviewer must inherit the backend (empty), not pin codex")
	assert.NotEqual(t, "codex", binding.Backend, "default interviewer must NOT pin the codex backend")
	assert.Empty(t, binding.Model, "default interviewer must use the subscription default model (model \"\")")
	// The subscription path (empty ConnectionRef) never reveals a key: RevealKey
	// must return empty-string, not an error.
	key, keyErr := binding.RevealKey(context.Background())
	require.NoError(t, keyErr, "interviewer RevealKey must not error (subscription-only path)")
	assert.Empty(t, key, "default interviewer must never carry an API key")
}

// TestPlannerCoderProfiles_ResolveToClaudeCodeBindings complements
// TestInterviewerProfile_ResolvesToClaudeCodeBinding by verifying the full
// resolver chain for the planner and coder profiles: both must resolve to
// Backend="claude-code", Kind="agent", Model="opus-4.8" with an empty key
// (subscription-only path). The stream selector dispatches on binding.Backend, so
// an incorrect backend value here would silently route planner/coder to the wrong
// stream adapter.
func TestPlannerCoderProfiles_ResolveToClaudeCodeBindings(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver := adapterprovider.NewDynamicProfileResolver(
		adapterprovider.NewProfileCacheFromProfiles(profiles...), nil)

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

// TestSampleFeaturePipeline_ValidatesWithDefaultProfiles is the acceptance that
// the committed samples/feature-pipeline/workflow.json passes workflow.Validate
// against the default profile registry. The DEFAULT interviewer runs on
// claude-code (model ""), so the bundled sample validates and runs out of the box
// (the gpt-5.5/codex split is a commented example, not the default — PR #32 Must
// Fix #4). The interviewer/planner/coder profiles are agent-kind while assessor
// is chat-kind, so kind-matching holds.
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

	// Sanity: the profile kinds the sample relies on.
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
