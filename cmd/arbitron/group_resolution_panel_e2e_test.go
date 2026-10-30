package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// paired:19 (Area 3 ↔ Area 8). The panel's "Open resolution session" link is
// the persisted ActiveSession, read through graph-state. It must name the
// parent at the group node (the instance the session socket and controls
// address) and always the newest open attempt: after a still-conflicted end,
// the link moves to the fresh attempt and the earlier session is closed, so
// neither history hydration nor controls can reach it.
func TestGroupFilesE2E_PanelSessionLinkFollowsTheOpenAttempt(t *testing.T) {
	t.Parallel()
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	srv := gsServeAPI(t, ds)
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)

	link := func() *instance.ResolutionSessionRef {
		code, gs, err := gsFetchGraphState(srv, parentID)
		require.NoError(t, err)
		require.Equal(t, 200, code)
		for _, p := range gs.Groups {
			if p.InstanceID == parentID && p.NodeID == "group-1" {
				require.NotNil(t, p.Gather, "the panel carries the gather report")
				if p.ActiveSession != nil {
					require.NotNil(t, p.Gather.ActiveSession)
					assert.Equal(t, *p.ActiveSession, *p.Gather.ActiveSession, "both panel fields name one session")
				}
				return p.ActiveSession
			}
		}
		t.Fatalf("graph-state has no group-1 panel on the parent")
		return nil
	}
	newestSession := func() engine.GroupResolutionIdentity {
		exec, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, parentID, "group-1")
		require.NoError(t, err)
		id, ok := engine.GroupResolutionIdentityOf(exec.LLMConversation)
		require.True(t, ok, "the newest group-1 record is a resolution session")
		return id
	}

	first := link()
	require.NotNil(t, first, "an open session shows the session button")
	assert.Equal(t, instance.ResolutionSessionRef{GroupRunID: run.ID, InstanceID: parentID, NodeID: "group-1", MergeIndex: run.Gather.ActiveSession.MergeIndex, Attempt: 1}, *first,
		"the link addresses the parent at the group node, attempt 1")
	id := newestSession()
	assert.True(t, id.Open)
	assert.Equal(t, first.Attempt, id.Attempt, "the record the session socket hydrates is the linked attempt")
	assert.Equal(t, first.MergeIndex, id.MergeIndex)

	// End without resolving: verification fails and a fresh attempt opens.
	_, err := ds.conversationHandler.EndSession(adminCtxForLocalUser(t, ds), parentID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r, gerr := ds.groupRunRepo.GetByParentNode(ctx, parentID, "group-1")
		return gerr == nil && r != nil && r.State == instance.GroupRunResolvingConflicts &&
			r.Gather != nil && r.Gather.ActiveSession != nil && r.Gather.ActiveSession.Attempt == 2
	}, 30*time.Second, 100*time.Millisecond, "a still-conflicted end opens attempt 2")
	waitForState(t, ds, parentID, instance.StateWaiting)

	second := link()
	require.NotNil(t, second, "the button stays for the fresh attempt")
	assert.Equal(t, 2, second.Attempt, "the link never points at the ended attempt")
	assert.Equal(t, first.MergeIndex, second.MergeIndex)
	assert.Equal(t, parentID, second.InstanceID)
	id = newestSession()
	assert.True(t, id.Open)
	assert.Equal(t, 2, id.Attempt, "history hydrates the fresh attempt, not the closed one")

	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 2)
	open := 0
	for _, r := range rows {
		if engine.IsOpenGroupResolutionSession(r.LLMConversation) {
			open++
		}
	}
	assert.Equal(t, 1, open, "the earlier attempt's session is closed")

	resolveConflictInStaging(t, groupRunOf(t, ds, parentID, "group-1"), "resolved\n")
	_, err = ds.conversationHandler.EndSession(adminCtxForLocalUser(t, ds), parentID)
	require.NoError(t, err, "ending the fresh attempt verifies and continues")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Nil(t, link(), "a finished gather shows no session button")
}
