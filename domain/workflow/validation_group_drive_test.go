package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

// A permissive file checker must not legitimize a non-relative prompt path.
// These literals must be rejected even when validation runs on Unix.
func TestValidateGroups_WindowsDrivePathsRejectedOnEveryHost(t *testing.T) {
	for _, path := range []string{`C:\repo`, `c:/repo`, `Z:repo`, `z:`} {
		for _, field := range []string{"repoPath", "resolverPromptFile"} {
			t.Run(field+"/"+path, func(t *testing.T) {
				wf := grpValidBranches()
				code := "GROUP_INVALID_REPO_PATH"
				if field == "repoPath" {
					grpCfg(wf).RepoPath = path
				} else {
					grpCfg(wf).ResolverPromptFile = path
					code = "GROUP_MISSING_PROMPT_FILE"
				}
				for _, files := range []workflow.FileChecker{nil, sgAnyFile{}} {
					errs := workflow.Validate(wf, files, nil, nil)
					assert.True(t, grpHas(errs, code, "g", ""), "%s %q: %v", field, path, errs)
				}
			})
		}
	}
}
