package filesystem

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A real incompatible SQLite prior must not block saving an ordinary workflow.
// No node has manifest coordinates: only Presentation.Retained can supply them.
func TestLayoutV1_OrdinaryResaveRetainsPositionsThroughSQLite(t *testing.T) {
	base := t.TempDir()
	db, err := adaptsqlite.Open(filepath.Join(base, "arbitron.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	pubs := adaptsqlite.NewPublishedWorkflowRepo(db)
	registry := adaptsqlite.NewWorkflowIdentityRepo(db)
	store := adaptsqlite.NewLayoutRepo(db)
	requester := layout.NewRequester(store, 3, nil)
	workspace := filepath.Join(base, "workflows")
	repo := NewWorkflowRepo(workspace)
	journal := NewRevisionJournal(workspace)
	commits, err := workflow.NewCommitSource(workflow.CommitSourceDeps{
		Repo: repo, Journal: journal, Mutations: repo, Trees: repo,
		Publications: pubs, Registry: registry, Relocator: requester,
	})
	require.NoError(t, err)
	repo.SetReadSettler(commits)
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: commits, Displayed: store})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(registry)
	svc.SetRevisionStamper(stamper)
	svc.SetMutationCoordinator(repo)
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
	scope, now := workflow.Private(owner), time.Now().UTC()
	prior := layouttest.SampleWorkflow()
	prior.ID, prior.Name = uuid.New(), "ordinary-v1"
	for i := range prior.Nodes {
		prior.Nodes[i].Position = nil
	}
	retained := workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"start": {X: 19, Y: 23}}}
	prior.Presentation = &workflow.Presentation{Revision: uuid.Must(uuid.NewV7()).String(), SavedAt: now, Reason: workflow.SaveReasonUpdate, Scope: workflow.ScopeLabelPrivate, OwnerID: owner, Name: prior.Name, Retained: retained}
	require.NoError(t, registry.Register(ctx, workflow.RegistryEntry{ID: prior.ID, Name: prior.Name, Scope: workflow.ScopePrivate, OwnerID: owner}))
	require.NoError(t, repo.Create(ctx, scope, prior.Name, prior))
	req := workflow.ArrangementRequest{Revision: prior.SavedRevision(), Scope: scope, Name: prior.Name, WorkflowID: prior.ID, Reason: workflow.SaveReasonUpdate, Manifest: layouttest.ManifestJSON(t, prior), Retained: retained, RequestedAt: now}
	require.NoError(t, requester.RequestArrangement(ctx, req))
	claimed, err := store.Claim(ctx, req.Revision, "old-worker", now, now.Add(time.Minute))
	require.NoError(t, err)
	geo := layouttest.Arranged(t, prior)
	geo.Format = layout.FormatV1
	best := layout.StoredResult{Revision: req.Revision, Generation: claimed.JobGeneration, Geometry: geo, Verdict: layout.VerdictPassed, CreatedAt: now}
	require.NoError(t, store.Complete(ctx, layout.Fence{Revision: req.Revision, Generation: claimed.JobGeneration}, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, now))
	before, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	_, ok, err := store.DisplayedLayout(ctx, req.Revision)
	require.ErrorContains(t, err, "stored geometry is incompatible with captured manifest")
	require.False(t, ok)

	input := layouttest.SampleWorkflow()
	input.Nodes[0].Label = "Explicit authoring update after upgrade"
	for i := range input.Nodes {
		input.Nodes[i].Position = nil
	}
	require.NoError(t, svc.Update(ctx, prior.Name, input))
	saved, err := repo.Load(ctx, scope, prior.Name)
	require.NoError(t, err)
	require.NotEqual(t, req.Revision, saved.SavedRevision())
	require.Equal(t, retained, saved.Presentation.Retained)
	require.Equal(t, input.Nodes[0].Label, saved.Nodes[0].Label)
	queued, err := store.Get(ctx, saved.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, layout.StatePending, queued.State)
	require.Equal(t, retained, queued.Retained)
	require.JSONEq(t, string(layouttest.ManifestJSON(t, saved)), string(queued.Manifest))
	after, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	require.Equal(t, before, after, "saving must preserve historical status")
	stored, err := store.Result(ctx, req.Revision, claimed.JobGeneration)
	require.NoError(t, err)
	require.Equal(t, best, stored, "saving must preserve immutable v1 history")
}
