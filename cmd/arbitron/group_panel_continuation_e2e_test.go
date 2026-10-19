package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// paired:18 — Area 1 decide-and-continue ↔ Area 8 gather panel, through the
// real composition root. The coordinator's writes (state, blocking release,
// continuing_independently, continuation_delivered, the frozen decision) are
// read back by graph-state's panelFor/waitingReasons at each step of a
// first_success + finish_independently run: before the decision, after the
// continuation with the loser still running, and after the loser's late
// completion. The late completion moves live status only.
func TestGroupPanelE2E_FinishIndependentlyPanelTracksDecisionContinuationAndLateLoser(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-fs-indep": fsIndependentManifest},
		map[string][]string{"grp-fs-indep": {"slow-hold", "fast-set"}})
	srv := gsServeAPI(t, ds)

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-indep")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	waitForState(t, ds, parentID, instance.StateWaiting)
	slowID := taskChildID(t, ds, parentID, "race", "slow")

	// Before the decision: both tasks block and are named as outstanding.
	before := panelWhen(t, srv, parentID, func(p engine.GroupPanel) bool {
		return p.State == string(instance.GroupRunRunning) && p.Progress.Active == 2
	}, "running panel with two active tasks")
	assert.Equal(t, []string{"waiting_for_tasks:slow,fast"}, before.WaitingReasons)
	assert.Nil(t, before.Decision)
	for _, card := range before.Tasks {
		assert.True(t, card.Blocking, card.Key)
		assert.Empty(t, card.FrozenStatus, card.Key)
	}

	gates.release(t, "fast-set")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	require.False(t, stateOf(t, ds, slowID).IsTerminal(), "the loser must still be running")
	run := groupRunOf(t, ds, parentID, "race")
	require.True(t, run.ContinuationDelivered, "the parent continued, so delivery is recorded")

	// After the continuation: done with nothing to wait on; the loser is live,
	// released from blocking, continuing independently, frozen unfinished.
	decided := panelWhen(t, srv, parentID, func(p engine.GroupPanel) bool {
		return p.State == string(instance.GroupRunDone)
	}, "done panel")
	assert.Empty(t, decided.WaitingReasons, "a delivered continuation leaves nothing to wait on")
	assert.Equal(t, "fast", decided.Winner)
	require.NotNil(t, decided.Decision)
	slow, fast := panelCard(t, decided, "slow"), panelCard(t, decided, "fast")
	assert.Equal(t, string(instance.GroupTaskStatusRunning), slow.LiveStatus)
	assert.Equal(t, string(instance.GroupTaskStatusUnfinished), slow.FrozenStatus)
	assert.False(t, slow.Blocking, "the decision releases every blocking flag")
	assert.True(t, slow.ContinuingIndependently)
	assert.False(t, slow.CancelRequested)
	require.NotNil(t, slow.ChildInstanceID)
	assert.Equal(t, slowID, *slow.ChildInstanceID)
	assert.Equal(t, string(instance.GroupTaskStatusCompleted), fast.LiveStatus)
	assert.Equal(t, string(instance.GroupTaskStatusCompleted), fast.FrozenStatus)

	gates.release(t, "slow-hold")
	waitForState(t, ds, slowID, instance.StateCompleted)
	waitForLiveStatus(t, ds, parentID, "race", "slow", instance.GroupTaskStatusCompleted)

	// After the late completion: live status moves, the frozen record does not.
	late := panelWhen(t, srv, parentID, func(p engine.GroupPanel) bool {
		for _, c := range p.Tasks {
			if c.Key == "slow" {
				return c.LiveStatus == string(instance.GroupTaskStatusCompleted)
			}
		}
		return false
	}, "late loser completed live")
	assert.Equal(t, string(instance.GroupRunDone), late.State)
	assert.Empty(t, late.WaitingReasons)
	assert.Equal(t, "fast", late.Winner)
	assert.Equal(t, string(instance.GroupTaskStatusUnfinished), panelCard(t, late, "slow").FrozenStatus)
	assert.Equal(t, decided.Decision, late.Decision, "a late completion never alters the frozen decision")
	assert.Equal(t, decided.FinalOutput, late.FinalOutput, "a late completion never alters the frozen output")
	assert.Equal(t, 2, late.Progress.Completed)
}

// panelWhen polls graph-state until the single group panel satisfies ok.
func panelWhen(t *testing.T, srv *httptest.Server, parentID uuid.UUID, ok func(engine.GroupPanel) bool, what string) engine.GroupPanel {
	t.Helper()
	var got engine.GroupPanel
	require.Eventually(t, func() bool {
		code, gs, err := gsFetchGraphState(srv, parentID)
		if err != nil || code != http.StatusOK || gs == nil || len(gs.Groups) != 1 {
			return false
		}
		got = gs.Groups[0]
		return ok(got)
	}, 40*time.Second, 100*time.Millisecond, "graph-state never showed %s; last panel: %+v", what, got)
	return got
}

func panelCard(t *testing.T, p engine.GroupPanel, key string) engine.GroupPanelTask {
	t.Helper()
	for _, c := range p.Tasks {
		if c.Key == key {
			return c
		}
	}
	require.Failf(t, "missing task card", "panel has no task %q", key)
	return engine.GroupPanelTask{}
}
