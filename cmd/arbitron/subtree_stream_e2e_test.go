package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// PROVENANCE PROBE for the subtree wiring. If initDomain does not build the
// bridge, routerDeps does not pass it, or NewRouterFromDeps does not hand it to
// the stream handler, the upgrade is refused with 501 and Dial fails. The sink
// is the WebSocket frame sequence a live view consumes.
func TestSubtreeStreamE2E_ParentStreamSeesBranchesAndClosesOnlyWhenSubtreeInactive(t *testing.T) {
	t.Parallel()
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "gs-fanout", map[string]any{}, "normal")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/v1/instances/"+parentID.String()+"/ws?subtree=1", nil)
	code := 0
	if resp != nil {
		code = resp.StatusCode
	}
	require.NoError(t, err, "subtree streaming must be wired through the composition root (HTTP %d)", code)
	defer conn.Close(websocket.StatusNormalClosure, "")

	type frame struct {
		Type   string    `json:"type"`
		Source uuid.UUID `json:"sourceInstanceId"`
	}
	var frames []frame
	for {
		_, msg, rerr := conn.Read(ctx)
		if rerr != nil {
			require.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(rerr), "the stream must end with a normal close, got: %v", rerr)
			break
		}
		var f frame
		require.NoError(t, json.Unmarshal(msg, &f))
		frames = append(frames, f)
	}

	require.NotEmpty(t, frames)
	assert.Equal(t, adapthttp.SubtreeInactiveEventType, frames[len(frames)-1].Type, "the stream closes only after the subtree-inactive frame")

	childSources := map[uuid.UUID]bool{}
	firstChildTerminal, rootTerminal := -1, -1
	for i, f := range frames {
		if f.Source == parentID {
			if f.Type == engine.EventTypeInstanceCompleted {
				rootTerminal = i
			}
			continue
		}
		child, gerr := ds.instanceRepo.Get(context.Background(), f.Source)
		require.NoError(t, gerr)
		require.NotNil(t, child.ParentInstanceID)
		assert.Equal(t, parentID, *child.ParentInstanceID, "only the parent's own descendants are streamed")
		childSources[f.Source] = true
		if f.Type == engine.EventTypeInstanceCompleted && firstChildTerminal < 0 {
			firstChildTerminal = i
		}
	}
	assert.Len(t, childSources, 2, "both concurrently running branches stream through the parent's subscription")
	require.GreaterOrEqual(t, firstChildTerminal, 0, "a branch's terminal event is forwarded")
	require.Greater(t, rootTerminal, firstChildTerminal, "a terminal descendant did not close the parent's stream")

	parent, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, parent.State)
}

// Persist real rows and ancestry, rather than mocking either the bridge or its
// GraphState activity authority. No engine execution is scheduled for these rows.
func ssSeed(t *testing.T, ds *domainStack, parent *uuid.UUID, state instance.State) uuid.UUID {
	t.Helper()
	node := "spawn"
	inst := &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(), OwnerID: localDefaultUser, WorkflowName: "stream-fixture",
		WorkflowSnapshot: map[string]any{"name": "stream-fixture", "nodes": []any{
			map[string]any{"id": "spawn", "type": "transform", "entrypoint": true},
		}},
		State: state, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: instance.VisibilityPublic,
	}
	if parent != nil {
		inst.ParentInstanceID, inst.ParentNodeID = parent, &node
		inst.SpawnMode, inst.SpawnDepth = instance.SpawnModeFireAndForget, 1
	}
	require.NoError(t, ds.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

type ssFrame struct {
	Type   string         `json:"type"`
	Source uuid.UUID      `json:"sourceInstanceId"`
	Data   map[string]any `json:"data"`
}

func ssRead(t *testing.T, ctx context.Context, conn *websocket.Conn) ssFrame {
	t.Helper()
	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame ssFrame
	require.NoError(t, json.Unmarshal(raw, &frame))
	return frame
}

func ssPublish(ds *domainStack, id uuid.UUID, typ, token string) {
	ds.bus.Publish(id, engine.Event{Type: typ, Timestamp: time.Now().UTC(), Data: map[string]any{"token": token}})
}

func ssDial(t *testing.T, ctx context.Context, srv *httptest.Server, root uuid.UUID) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/v1/instances/"+root.String()+"/ws?subtree=1", nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func TestSubtreeStreamE2E_VisibilityRevocationAndRestorationOnOneSocket(t *testing.T) {
	ds := bootGraphStateStack(t)
	root := ssSeed(t, ds, nil, instance.StateWaiting)
	child := ssSeed(t, ds, &root, instance.StateRunning)
	// Supply a non-owner identity at the HTTP boundary, leaving the real
	// routerDeps, Engine authorization, repositories and bridge intact.
	deps := ds.httpDeps()
	deps.Auth = nil
	router := adapthttp.NewRouterFromDeps(deps)
	viewer := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), viewer)))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := ssDial(t, ctx, srv, root)

	ssPublish(ds, child, engine.EventTypeInteractiveToken, "public-before")
	f := ssRead(t, ctx, conn)
	require.Equal(t, child, f.Source)
	require.Equal(t, "public-before", f.Data["token"])

	// Await the root marker after the private token: the serial bridge pump
	// has processed the revoked source before visibility is restored.
	_, err := ds.db.ExecContext(ctx, "UPDATE instances SET visibility = 'private' WHERE id = ?", child.String())
	require.NoError(t, err)
	ssPublish(ds, child, engine.EventTypeInteractiveToken, "PRIVATE-TOKEN")
	ssPublish(ds, root, engine.EventTypeNodeStarted, "revocation-barrier")
	f = ssRead(t, ctx, conn)
	require.Equal(t, root, f.Source, "no revoked token may reach this socket")
	require.Equal(t, "revocation-barrier", f.Data["token"])

	_, err = ds.db.ExecContext(ctx, "UPDATE instances SET visibility = 'public' WHERE id = ?", child.String())
	require.NoError(t, err)
	ssPublish(ds, child, engine.EventTypeInteractiveToken, "public-after")
	f = ssRead(t, ctx, conn)
	require.Equal(t, child, f.Source)
	require.Equal(t, "public-after", f.Data["token"], "a denied source must not stay negatively cached")
}

// A parent-scoped payload needs the task's authorization too. Removing the
// persisted roster input or the per-event task read must fail this socket test.
// Events are injected here; autonomous resolver text is a separate open gap.
func TestSubtreeStreamE2E_ConflictPathsReauthorizeTheTaskOnOneSocket(t *testing.T) {
	ds := bootGraphStateStack(t)
	root := ssSeed(t, ds, nil, instance.StateWaiting)
	child := ssSeed(t, ds, &root, instance.StateRunning)
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: root, NodeID: "spawn", Form: "workflows",
		CompletionRule: "all_succeed", UnfinishedBehavior: "wait_for_remaining", GatherMode: "results_and_files",
		Tasks: []instance.GroupTask{{Key: "hidden", Kind: instance.GroupTaskKindWorkflow, Origin: "private-workflow", ChildInstanceID: child}},
	}
	require.NoError(t, ds.groupRunRepo.CreateWithTasks(context.Background(), run))
	deps := ds.httpDeps()
	deps.Auth = nil
	router := adapthttp.NewRouterFromDeps(deps)
	viewer := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), viewer)))
	}))
	t.Cleanup(srv.Close)
	ownerSrv := gsServeAPI(t, ds)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := ssDial(t, ctx, srv, root)
	ownerConn := ssDial(t, ctx, ownerSrv, root)
	publish := func() {
		ds.bus.Publish(root, engine.Event{Type: engine.EventTypeGroupConflict, Timestamp: time.Now().UTC(),
			Data: map[string]any{"groupRunId": run.ID.String(), "nodeId": "spawn", "taskKey": "hidden", "conflictPaths": []string{"PRIVATE/path.txt"}}})
	}
	publish()
	require.Equal(t, engine.EventTypeGroupConflict, ssRead(t, ctx, conn).Type)
	require.Equal(t, engine.EventTypeGroupConflict, ssRead(t, ctx, ownerConn).Type)

	_, err := ds.db.ExecContext(ctx, "UPDATE instances SET visibility = 'private' WHERE id = ?", child.String())
	require.NoError(t, err)
	publish()
	ssPublish(ds, root, engine.EventTypeNodeStarted, "revocation-barrier")
	f := ssRead(t, ctx, conn)
	require.Equal(t, engine.EventTypeNodeStarted, f.Type, "hidden task paths must not arrive on a visible parent's socket")
	require.Equal(t, "revocation-barrier", f.Data["token"])
	f = ssRead(t, ctx, ownerConn)
	require.Equal(t, engine.EventTypeGroupConflict, f.Type)
	require.Equal(t, []any{"PRIVATE/path.txt"}, f.Data["conflictPaths"], "filtering another viewer must not mutate the shared event")
	require.Equal(t, engine.EventTypeNodeStarted, ssRead(t, ctx, ownerConn).Type)

	_, err = ds.db.ExecContext(ctx, "UPDATE instances SET visibility = 'public' WHERE id = ?", child.String())
	require.NoError(t, err)
	publish()
	require.Equal(t, engine.EventTypeGroupConflict, ssRead(t, ctx, conn).Type, "task authorization must not cache the denial")
	require.Equal(t, engine.EventTypeGroupConflict, ssRead(t, ctx, ownerConn).Type)
}

func TestSubtreeStreamE2E_TerminalRootWithMoreThan1000ChildrenStaysOpenUntilLastFinishes(t *testing.T) {
	ds := bootGraphStateStack(t)
	root := ssSeed(t, ds, nil, instance.StateCompleted)
	const completedChildren = 1000
	for i := 0; i < completedChildren; i++ {
		ssSeed(t, ds, &root, instance.StateCompleted)
	}
	last := ssSeed(t, ds, &root, instance.StateWaiting)
	srv := gsServeAPI(t, ds)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := ssDial(t, ctx, srv, root)

	ssPublish(ds, root, engine.EventTypeInstanceCompleted, "root-terminal")
	ssPublish(ds, last, engine.EventTypeNodeStarted, "last-active")
	f := ssRead(t, ctx, conn)
	require.Equal(t, engine.EventTypeInstanceCompleted, f.Type)
	require.Equal(t, root, f.Source)
	f = ssRead(t, ctx, conn)
	require.Equal(t, engine.EventTypeNodeStarted, f.Type, "no inactive frame while child 1001 is active")
	require.Equal(t, last, f.Source)
	require.Equal(t, "last-active", f.Data["token"])

	require.NoError(t, ds.instanceRepo.UpdateState(ctx, last, instance.StateCompleted))
	ssPublish(ds, last, engine.EventTypeInstanceCompleted, "last-terminal")
	f = ssRead(t, ctx, conn)
	require.Equal(t, engine.EventTypeInstanceCompleted, f.Type)
	require.Equal(t, last, f.Source)
	f = ssRead(t, ctx, conn)
	require.Equal(t, adapthttp.SubtreeInactiveEventType, f.Type)
	require.Equal(t, root, f.Source)
	require.Equal(t, root.String(), f.Data["rootInstanceId"])
	require.Equal(t, string(instance.StateCompleted), f.Data["state"])
	_, _, err := conn.Read(ctx)
	require.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(err))
}

// paired:21 — the live view and the subtree stream agree on activity through
// a REAL finish_independently group. After the parent continues and completes,
// its loser is still running: graph-state (the client's subscribe gate) must
// still report active, and the parent's socket must stay open long enough to
// forward the loser's terminal frame before the subtree-inactive frame. A
// server that closed on the parent's own terminal event, or graph-state that
// stopped counting the independent task, never delivers that frame.
func TestSubtreeStreamE2E_IndependentLoserKeepsCompletedParentStreamAndViewLive(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-fs-indep": fsIndependentManifest},
		map[string][]string{"grp-fs-indep": {"slow-hold", "fast-set"}})
	srv := gsServeAPI(t, ds)

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-indep")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	slowID := taskChildID(t, ds, parentID, "race", "slow")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn := ssDial(t, ctx, srv, parentID)
	frames := make(chan ssFrame, 256)
	closed := make(chan websocket.StatusCode, 1)
	go func() {
		defer close(frames)
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				closed <- websocket.CloseStatus(err)
				return
			}
			var f ssFrame
			if json.Unmarshal(raw, &f) == nil {
				frames <- f
			}
		}
	}()
	next := func(what string) ssFrame {
		t.Helper()
		select {
		case f, ok := <-frames:
			require.True(t, ok, "the subtree stream closed while waiting for %s", what)
			return f
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		}
		return ssFrame{}
	}

	gates.release(t, "fast-set")
	for {
		f := next("the parent's own terminal frame")
		require.NotEqual(t, adapthttp.SubtreeInactiveEventType, f.Type, "the stream ended while the independent loser was running")
		if f.Source == parentID && f.Type == engine.EventTypeInstanceCompleted {
			break
		}
	}
	require.False(t, stateOf(t, ds, slowID).IsTerminal(), "fixture: the loser must outlive the parent")

	// The client keeps subscribing only while graph-state says active.
	code, gs, err := gsFetchGraphState(srv, parentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	assert.True(t, gs.Active, "a completed parent with a running independent task is still active to the live view")
	require.NotEmpty(t, gs.Contexts)
	assert.Equal(t, string(instance.StateCompleted), gs.Contexts[0].State)
	var slowCtx *engine.ExecutionContext
	for i := range gs.Contexts {
		if gs.Contexts[i].InstanceID == slowID {
			slowCtx = &gs.Contexts[i]
		}
	}
	require.NotNil(t, slowCtx, "the independent loser is a context of the parent's aggregate")
	assert.False(t, slowCtx.Redacted)
	assert.True(t, isActiveForTest(slowCtx.State), "loser context state %q", slowCtx.State)

	gates.release(t, "slow-hold")
	sawLoserTerminal := false
	for {
		f := next("the subtree-inactive frame")
		if f.Source == slowID && f.Type == engine.EventTypeInstanceCompleted {
			sawLoserTerminal = true
		}
		if f.Type == adapthttp.SubtreeInactiveEventType {
			assert.Equal(t, parentID, f.Source)
			assert.Equal(t, string(instance.StateCompleted), f.Data["state"])
			break
		}
	}
	assert.True(t, sawLoserTerminal, "the loser's terminal frame reaches the parent's socket before it closes")
	select {
	case status := <-closed:
		assert.Equal(t, websocket.StatusNormalClosure, status)
	case <-ctx.Done():
		t.Fatal("the stream did not close after the subtree-inactive frame")
	}

	code, gs, err = gsFetchGraphState(srv, parentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	assert.False(t, gs.Active, "the refetch the inactive frame triggers stops the client's subscription")
}

func isActiveForTest(state string) bool {
	switch instance.State(state) {
	case instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused:
		return true
	}
	return false
}
