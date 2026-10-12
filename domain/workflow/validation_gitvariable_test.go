package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitVariableErrors(gitVariable string) []workflow.ValidationError {
	wf := &workflow.Workflow{Name: "w", GitVariable: gitVariable}
	var out []workflow.ValidationError
	for _, e := range workflow.Validate(wf, nil, nil, nil) {
		if e.Code == "invalid_git_variable" {
			out = append(out, e)
		}
	}
	return out
}

func TestValidate_GitVariableAccepted(t *testing.T) {
	for _, v := range []string{"", "branch", "pr_branch", "init-pr-workspace.pr_branch"} {
		assert.Empty(t, gitVariableErrors(v), "gitVariable %q should be valid", v)
	}
}

func TestValidate_GitVariableRejected(t *testing.T) {
	for _, v := range []string{"bad var", ".x", "x.", "a..b", "{{branch}}"} {
		errs := gitVariableErrors(v)
		require.Len(t, errs, 1, "gitVariable %q should be rejected", v)
		assert.Empty(t, errs[0].Severity, "validation errors carry no severity (lint warnings do)")
		assert.True(t, strings.Contains(errs[0].Message, v), "message should name the bad value")
	}
}

func TestWorkflow_GitVariableJSONRoundTrip(t *testing.T) {
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(`{"name":"w","gitVariable":"pr_branch"}`), &wf))
	assert.Equal(t, "pr_branch", wf.GitVariable)

	out, err := json.Marshal(workflow.Workflow{Name: "w"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "gitVariable")
}
