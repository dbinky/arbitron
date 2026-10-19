package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const repoPathParentWS = "/ws/instances/parent"

// repoVars builds the parent's live store: {{workspace}} seeded the way
// Engine.prepareWorkspace does it, plus ordinary inputs.
func repoVars(t *testing.T, workspace string, inputs map[string]any) *variable.VariableStore {
	t.Helper()
	s := variable.NewStore()
	if workspace != "" {
		s.SetReserved("workspace", workspace)
	}
	for k, v := range inputs {
		require.NoError(t, s.SetInput(k, v))
	}
	return s
}

func TestResolveGroupRepoPath_EmptyIsNoHint(t *testing.T) {
	got, err := engine.ResolveGroupRepoPath("", repoVars(t, repoPathParentWS, nil), repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestResolveGroupRepoPath_LiteralRelativePathIsCleaned(t *testing.T) {
	got, err := engine.ResolveGroupRepoPath("src/./app/", repoVars(t, repoPathParentWS, nil), repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "src/app", got)
}

func TestResolveGroupRepoPath_TemplateResolvesAgainstTheParentsStore(t *testing.T) {
	got, err := engine.ResolveGroupRepoPath("{{repo_dir}}/app", repoVars(t, repoPathParentWS, map[string]any{"repo_dir": "src"}), repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "src/app", got, "the template is resolved from the PARENT's variables at launch")
}

func TestResolveGroupRepoPath_WorkspaceTemplateBecomesRelative(t *testing.T) {
	vars := repoVars(t, repoPathParentWS, nil)
	got, err := engine.ResolveGroupRepoPath("{{workspace}}/src", vars, repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "src", got)

	got, err = engine.ResolveGroupRepoPath("{{workspace}}", vars, repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, ".", got, "the whole working area is a valid hint")
}

func TestResolveGroupRepoPath_RejectsPathsOutsideTheImmediateParent(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		inputs map[string]any
	}{
		{"blank", "   ", nil},
		{"absolute outside", "/etc", nil},
		{"literal climb", "../sibling", nil},
		{"climb inside", "src/../../sibling", nil},
		{"templated climb", "{{up}}/x", map[string]any{"up": ".."}},
		{"workspace climb", "{{workspace}}/../other", nil},
		{"absolute internal climb", "{{workspace}}/src/../app", nil},
		{"rooted backslash", `\share\repo`, nil},
		{"blank after resolution", "{{empty}}", map[string]any{"empty": "  "}},
		{"nul byte", "src\x00x", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.ResolveGroupRepoPath(tc.raw, repoVars(t, repoPathParentWS, tc.inputs), repoPathParentWS)
			require.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
		})
	}
}

func TestResolveGroupRepoPath_UnresolvedReferenceIsActionable(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("{{missing}}/x", repoVars(t, repoPathParentWS, nil), repoPathParentWS)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "{{missing}}/x", "the message names the author's template")
}

func TestResolveGroupRepoPath_NonStringTemplateIsRejected(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("{{count}}", repoVars(t, repoPathParentWS, map[string]any{"count": 3}), repoPathParentWS)
	require.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
	assert.Contains(t, err.Error(), "not a path")
}

func TestResolveGroupRepoPath_AbsoluteWithoutAParentAreaIsRejected(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("/ws/instances/parent/src", repoVars(t, "", nil), "")
	require.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
}

func TestResolveGroupRepoPath_TemplateWithoutAStoreIsRejected(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("{{repo_dir}}", nil, repoPathParentWS)
	require.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
}
