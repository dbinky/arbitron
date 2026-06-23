package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- error: SetWorkflowID failure propagates from Publish. ---

// TestService_Publish_SetWorkflowIDError_Propagates verifies that a storage
// error from SetWorkflowID (the id-link step after the tree + publication record
// are written) surfaces to the Publish caller. The failure must not be silently
// swallowed — the id link is metadata, but a broken id link should be loud so
// operators can re-publish or investigate.
func TestService_Publish_SetWorkflowIDError_Propagates(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	setErr := errors.New("db write failed")
	pubs.setWorkflowIDErr = setErr
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)

	owner := uuid.New()
	require.NoError(t, repo.Create(ctxWith(owner, true), workflow.Private(owner), "src", wf("src")))

	err := svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, "")
	require.Error(t, err, "SetWorkflowID failure must cause Publish to return an error")
	assert.ErrorIs(t, err, setErr, "the publication id-link error must be wrapped and reachable via errors.Is")
}

// --- happy: publishing links the publication record to the shared name's
// stable workflow id (published_workflows.workflow_id). ---
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
