package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// Only the external LLM is replaced. Session persistence, emitters, roster
// authorization, composition-root bridge and socket writes remain real.
type ssResolver struct {
	engine.ChatProvider
	t       *testing.T
	workDir string
}

func (s ssResolver) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	require.Equal(s.t, "resolver", alias)
	return provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "fixture"}, nil
}

func (s ssResolver) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	require.Equal(s.t, provider.ProfileKindAgent, b.Kind)
	return s, nil
}

func (s ssResolver) StreamChat(_ context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	require.Equal(s.t, s.workDir, req.WorkDir)
	require.NotEmpty(s.t, req.Prompt)
	ch := make(chan engine.ChatChunk, 2)
	ch <- engine.ChatChunk{Token: "PRIVATE/other-task.txt", SessionID: "resolver-session"}
	ch <- engine.ChatChunk{Done: true}
	close(ch)
	return ch, nil
}

// Removing either emitter provenance or the roster guard must expose resolver
// text at this actual wire sink, including text about a NON-conflicting task.
func TestSubtreeStreamE2E_ResolverReauthorizesAllContributorsAndHistory(t *testing.T) {
	ds := bootGraphStateStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	node, spawnNode := "g", "spawn"
	// The group's parent is itself a descendant, so a grandparent socket must
	// hydrate the nested resolution session (paired:14).
	grandparent := ssSeed(t, ds, nil, instance.StateRunning)
	parent := &instance.Instance{ID: uuid.New(), OwnerID: localDefaultUser, WorkflowName: "resolver-fixture",
		ParentInstanceID: &grandparent, ParentNodeID: &spawnNode, SpawnMode: instance.SpawnModeFireAndForget, SpawnDepth: 1,
		State: instance.StateWaiting, CurrentNodeID: &node, Priority: instance.PriorityNormal,
		Visibility: instance.VisibilityPublic, Variables: map[string]any{},
		WorkflowSnapshot: map[string]any{"name": "resolver-fixture", "nodes": []any{
			map[string]any{"id": "g", "type": "parallel_group", "entrypoint": true},
		}},
	}
	require.NoError(t, ds.instanceRepo.Create(ctx, parent))
	conflicting := ssSeed(t, ds, &parent.ID, instance.StateRunning)
	other := ssSeed(t, ds, &parent.ID, instance.StateRunning)
	_, err := ds.db.ExecContext(ctx, "UPDATE instances SET visibility = 'private' WHERE id = ?", other.String())
	require.NoError(t, err)
	run := &instance.GroupRun{ID: uuid.New(), ParentInstanceID: parent.ID, NodeID: node, Form: "workflows",
		CompletionRule: "all_succeed", UnfinishedBehavior: "wait_for_remaining", GatherMode: "results_and_files",
		State: instance.GroupRunRunning, Config: map[string]any{"resolverProfile": "resolver"},
		Tasks: []instance.GroupTask{
			{Key: "conflicting", Position: 0, Kind: instance.GroupTaskKindWorkflow, Origin: "public", ChildInstanceID: conflicting},
			{Key: "other", Position: 1, Kind: instance.GroupTaskKindWorkflow, Origin: "private", ChildInstanceID: other},
		},
	}
	require.NoError(t, ds.groupRunRepo.CreateWithTasks(ctx, run))
	changed, err := ds.groupRunRepo.TransitionState(ctx, run.ID, instance.GroupRunRunning, instance.GroupRunResolvingConflicts)
	require.NoError(t, err)
	require.True(t, changed)
	llm := ssResolver{t: t, workDir: t.TempDir()}
	bridge := engine.NewGroupResolutionBridge(ds.groupRunRepo, ds.instanceRepo, ds.nodeExecRepo, llm, llm,
		ds.fileReader, ds.eng, ds.bus, ds.queueRepo.(instance.NodeAddressedWaker), ds.queueMgr, time.Second)
	conversation := engine.NewConversationHandler(ds.instanceRepo, ds.nodeExecRepo, llm, llm, ds.bus, ds.queueMgr)
	conversation.(interface {
		SetGroupResolution(instance.GroupRunRepository, engine.GroupResolutionCompleter)
	}).SetGroupResolution(ds.groupRunRepo, bridge)
	deps := ds.routerDeps()
	deps.Auth = nil
	deps.ConversationHandler = conversation
	router := adapthttp.NewRouterFromDeps(deps)
	viewer := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	serveAs := func(who identity.Identity) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			router.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), who)))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	viewerSrv := serveAs(viewer)
	owner := identity.Identity{UserID: localDefaultUser, Role: identity.RoleUser}
	ownerSrv := serveAs(owner)
	viewerConn, ownerConn := ssDial(t, ctx, viewerSrv, parent.ID), ssDial(t, ctx, ownerSrv, parent.ID)
	drain := func(conn *websocket.Conn) []ssFrame {
		var frames []ssFrame
		for {
			f := ssRead(t, ctx, conn)
			if f.Type == engine.EventTypeNodeStarted && f.Data["token"] == "barrier" {
				return frames
			}
			require.Equal(t, parent.ID, f.Source)
			frames = append(frames, f)
		}
	}
	barrier := func() { ssPublish(ds, parent.ID, engine.EventTypeNodeStarted, "barrier") }
	ref, err := bridge.StartResolution(ctx, run, parent, engine.ResolutionRequest{
		Handle: engine.GatherHandle{StagingDir: llm.workDir}, MergeIndex: 0, Attempt: 1,
		TaskKey: "conflicting", ConflictPaths: []string{"public/path.txt"},
	})
	require.NoError(t, err)
	barrier()
	frames := drain(viewerConn)
	require.Len(t, frames, 1, "a hidden non-conflicting contributor must withhold resolver tokens and message")
	require.Equal(t, engine.EventTypeGroupConflict, frames[0].Type)
	frames = drain(ownerConn)
	require.Len(t, frames, 3, "owner receives autonomous tokens, message and conflict")
	require.Equal(t, "PRIVATE/other-task.txt", frames[0].Data["token"])

	report := instance.GatherReport{Mode: "results_and_files", State: "resolving", ActiveSession: ref,
		Handle: map[string]any{"gatherId": uuid.NewString(), "snapshotHandle": "snapshot", "rollbackRef": "rollback", "stagingDir": llm.workDir, "parentDir": t.TempDir()},
	}
	require.NoError(t, ds.groupRunRepo.SetGatherReport(ctx, run.ID, report))
	ownerCtx := identity.WithIdentity(ctx, owner)
	for _, visibility := range []string{"public", "private", "public"} {
		_, err = ds.db.ExecContext(ctx, "UPDATE instances SET visibility = ? WHERE id = ?", visibility, other.String())
		require.NoError(t, err)
		require.NoError(t, conversation.HandleMessage(ownerCtx, parent.ID, "Discuss PRIVATE/other-task.txt"))
		barrier()
		got := drain(viewerConn)
		if visibility == "private" {
			require.Empty(t, got, "revocation must withhold user message, tokens and assistant message")
		} else {
			require.Len(t, got, 3, "restored visibility must resume delivery on the same socket")
		}
		require.Len(t, drain(ownerConn), 3, "another viewer's filter must not mutate shared frames")
	}
	// Hydration bypasses the bus: reconnect under each visibility and observe
	// the first wire frame rather than checking an intermediate history helper.
	for _, visibility := range []string{"private", "public"} {
		_, err = ds.db.ExecContext(ctx, "UPDATE instances SET visibility = ? WHERE id = ?", visibility, other.String())
		require.NoError(t, err)
		reconnected := ssDial(t, ctx, viewerSrv, parent.ID)
		fromGrandparent := ssDial(t, ctx, viewerSrv, grandparent)
		barrier()
		for _, conn := range []*websocket.Conn{reconnected, fromGrandparent} {
			got := drain(conn)
			if visibility == "private" {
				require.Empty(t, got, "reconnect history must not disclose hidden contributor text")
			} else {
				require.Len(t, got, 1, "the descendant session's history is hydrated on the ancestor socket too")
				require.Equal(t, engine.EventTypeInteractiveHistory, got[0].Type)
				raw, err := json.Marshal(got[0].Data)
				require.NoError(t, err)
				require.Contains(t, string(raw), "PRIVATE/other-task.txt")
			}
		}
		drain(viewerConn)
		drain(ownerConn)
	}
	_, err = ds.db.ExecContext(ctx, "UPDATE instances SET visibility = 'private' WHERE id = ?", other.String())
	require.NoError(t, err)
	require.NoError(t, bridge.CloseSession(ctx, *ref, "PRIVATE/other-task.txt"))
	barrier()
	require.Empty(t, drain(viewerConn), "ended frames carry resolver details too")
	frames = drain(ownerConn)
	require.Len(t, frames, 1)
	require.Equal(t, engine.EventTypeInteractiveEnded, frames[0].Type)
}
