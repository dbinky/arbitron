package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Step 5 (happy): publishing links the publication record to the shared name's
// stable workflow id (published_workflows.workflow_id).
func TestService_Publish_LinksWorkflowID(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)

	owner := uuid.New()
	require.NoError(t, repo.Create(ctxWith(owner, true), workflow.Private(owner), "src", wf("src")))

	require.NoError(t, svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, ""))

	// The shared name was registered, allocating a stable id.
	publishedID, err := reg.ResolveID(context.Background(), "pub")
	require.NoError(t, err)

	// The publication record was linked to that same id.
	linked, ok := pubs.workflowIDs["pub"]
	require.True(t, ok, "publish must link the publication to the workflow id")
	assert.Equal(t, publishedID, linked,
		"published_workflows.workflow_id must reference the shared name's stable id")
}
