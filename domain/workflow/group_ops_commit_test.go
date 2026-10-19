package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep the real folder write, but change an external dependency immediately
// after its commit to expose any validation performed after persistence.
type opsAfterCommitRepo struct {
	workflow.Repository
	afterCommit func()
}

func (r opsAfterCommitRepo) Update(ctx context.Context, scope workflow.Scope, name string, wf *workflow.Workflow) error {
	if err := r.Repository.Update(ctx, scope, name, wf); err != nil {
		return err
	}
	r.afterCommit()
	return nil
}

func TestGroupOps_Upsert_UsesAcceptedFindingsAfterCommit(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		t.Run(form, func(t *testing.T) {
			root := t.TempDir()
			repo := filesystem.NewWorkflowRepo(root)
			ctx := ctxWith(sgAlice, false)
			scope := workflow.Private(sgAlice)
			require.NoError(t, repo.Create(ctx, scope, "wf", opsBase("wf")))
			up := opsFanout()
			var unavailable func()
			var wantCurrentCode string
			if form == workflow.GroupFormBranches {
				up.Config.GatherMode = workflow.GroupGatherResultsAndFiles
				up.Config.ResolverProfile = "agenty"
				up.Config.ResolverPromptFile = "resolve.md"
				require.NoError(t, repo.WriteFile(ctx, scope, "wf", "resolve.md", []byte("resolve conflicts")))
				unavailable = func() {
					require.NoError(t, os.Remove(filepath.Join(root, "users", sgAlice.String(), "wf", "resolve.md")))
				}
				wantCurrentCode = "GROUP_MISSING_PROMPT_FILE"
			} else {
				require.NoError(t, repo.Create(ctx, scope, "reviewer", sgChild("reviewer", sgTopic)))
				up.Config = workflow.GroupConfig{
					Form:               workflow.GroupFormWorkflows,
					Tasks:              []workflow.GroupTask{{Key: "review", Workflow: "reviewer"}},
					CompletionRule:     workflow.GroupRuleAllSucceed,
					UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
					GatherMode:         workflow.GroupGatherResults,
				}
				unavailable = func() {
					// A parse failure is a storage/checker failure, not an unknown child.
					require.NoError(t, os.WriteFile(filepath.Join(root, "users", sgAlice.String(), "reviewer", "workflow.json"), []byte("{"), 0o644))
				}
				wantCurrentCode = "GROUP_CHECKER_UNAVAILABLE"
			}
			svc := workflow.NewWorkflowService(opsAfterCommitRepo{Repository: repo, afterCommit: unavailable}, nil)
			svc.SetGroupCheckers(sgProfiles{"agenty": "agent"}, filesystem.NewFileReader(root))

			view, err := svc.UpsertGroup(ctx, "wf", up)
			require.NoError(t, err)
			require.NotNil(t, view)
			assert.Empty(t, sgHard(view.Advisories), "a committed upsert must return the accepted findings, not new hard failures")
			if form == workflow.GroupFormWorkflows {
				require.Len(t, view.Advisories, 1, "preserve the accepted runtime-input advisory")
				assert.Equal(t, "GROUP_REQUIRED_INPUT_RUNTIME", view.Advisories[0].Code)
				assert.Equal(t, "review", view.Advisories[0].TaskKey)
				assert.Equal(t, "warning", view.Advisories[0].Severity)
			}
			stored, err := repo.Load(ctx, scope, "wf")
			require.NoError(t, err)
			assert.Equal(t, up.Config, *opsNode(t, stored, "fanout").Config.Group)

			// Inspection still consults the current dependencies, independently of
			// the write receipt. This also proves the injected failure took effect.
			current, err := svc.GetGroup(ctx, "wf", "fanout")
			require.NoError(t, err)
			assert.Contains(t, sgCodes(current.Advisories), wantCurrentCode)
		})
	}
}
