package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
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
