package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revisionLocks models the entire operation, not independent repository calls.
// The context marker makes dropping a target or the callback context observable.
type revisionLockKey struct{}
type revisionLocks struct {
	real   workflow.MutationCoordinator
	mu     sync.Mutex
	held   []workflow.MutationTarget
	before func()
	err    error
}

func (m *revisionLocks) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.before != nil {
		f := m.before
		m.before = nil
		f()
	}
	if m.err != nil {
		return m.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unique := map[workflow.MutationTarget]bool{}
	for _, target := range targets {
		unique[target] = true
	}
	m.held = nil
	for target := range unique {
		m.held = append(m.held, target)
	}
	sort.Slice(m.held, func(i, j int) bool {
		return scopeKey(m.held[i].Scope)+m.held[i].Name < scopeKey(m.held[j].Scope)+m.held[j].Name
	})
	owned := context.WithValue(ctx, revisionLockKey{}, m)
	if m.real != nil {
		return m.real.WithMutations(owned, targets, fn)
	}
	return fn(owned)
}
func requireHeld(ctx context.Context, scope workflow.Scope, name string) error {
	m, ok := ctx.Value(revisionLockKey{}).(*revisionLocks)
	if !ok {
		return errors.New("operation is outside mutation callback")
	}
	for _, target := range m.held {
		if target.Scope == scope && target.Name == name {
			return nil
		}
	}
	return errors.New("destination is not locked")
}

type revisionRepo struct {
	*filesystem.WorkflowRepo
	base       string
	events     *[]string
	fail       map[string]error
	afterWrite func()
}

func (r *revisionRepo) Create(ctx context.Context, scope workflow.Scope, name string, wf *workflow.Workflow) error {
	if err := requireHeld(ctx, scope, name); err != nil {
		return err
	}
	*r.events = append(*r.events, "create")
	if err := r.fail["write"]; err != nil {
		return err
	}
	if err := r.WorkflowRepo.Create(ctx, scope, name, wf); err != nil {
		return err
	}
	if r.afterWrite != nil {
		r.afterWrite()
	}
	return nil
}
func (r *revisionRepo) Update(ctx context.Context, scope workflow.Scope, name string, wf *workflow.Workflow) error {
	if err := requireHeld(ctx, scope, name); err != nil {
		return err
	}
	*r.events = append(*r.events, "update")
	if err := r.fail["write"]; err != nil {
		return err
	}
	if err := r.WorkflowRepo.Update(ctx, scope, name, wf); err != nil {
		return err
	}
	if r.afterWrite != nil {
		r.afterWrite()
	}
	return nil
}
func (r *revisionRepo) Delete(ctx context.Context, scope workflow.Scope, name string) error {
	if err := requireHeld(ctx, scope, name); err != nil {
		return err
	}
	*r.events = append(*r.events, "delete:"+name)
	if err := r.fail["delete:"+name]; err != nil {
		return err
	}
	return r.WorkflowRepo.Delete(ctx, scope, name)
}
func (r *revisionRepo) CopyTreeStamped(ctx context.Context, src workflow.Scope, sn string, dst workflow.Scope, dn string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	if err := requireHeld(ctx, src, sn); err != nil {
		return err
	}
	if err := requireHeld(ctx, dst, dn); err != nil {
		return err
	}
	if err := r.fail["copy"]; err != nil {
		return err
	}
	return r.WorkflowRepo.CopyTreeStamped(ctx, src, sn, dst, dn, overwrite, stamp)
}
func revisionFixture(t *testing.T) (*workflow.Service, *revisionRepo, *stampJournal, *stampRequester, *stampSettler, *revisionLocks) {
	t.Helper()
	_, j, requester, settler, _ := stamperFixture(t)
	base := t.TempDir()
	repo := &revisionRepo{WorkflowRepo: filesystem.NewWorkflowRepo(base), base: base, events: j.events, fail: map[string]error{}}
	settler.settle = func(ctx context.Context, scope workflow.Scope, name string) error {
		return requireHeld(ctx, scope, name)
	}
	st, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: j, Requester: requester, Settler: settler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(repo, nil)
	locks := &revisionLocks{real: repo.WorkflowRepo}
	svc.SetRevisionStamper(st)
	svc.SetMutationCoordinator(locks)
	return svc, repo, j, requester, settler, locks
}
func assertSavedRequest(t *testing.T, repo workflow.Repository, r *stampRequester, scope workflow.Scope, name string, reason workflow.SaveReason) *workflow.Workflow {
	t.Helper()
	require.NotEmpty(t, r.requests)
	req := r.requests[len(r.requests)-1]
	saved, err := repo.Load(context.Background(), scope, name)
	require.NoError(t, err)
	assert.Equal(t, scope, req.Scope)
	assert.Equal(t, name, req.Name)
	assert.Equal(t, name, saved.Name)
	assert.Equal(t, saved.ID, req.WorkflowID)
	assert.Equal(t, reason, req.Reason)
	assert.Equal(t, saved.SavedRevision(), req.Revision)
	assert.NotEmpty(t, req.Revision)
	assert.JSONEq(t, jsonValue(t, saved), string(req.Manifest))
	assert.Equal(t, saved.Presentation.Retained, req.Retained)
	return saved
}
func TestServiceRevisionCreateUpdate(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "registry"}[registered], func(t *testing.T) {
			svc, repo, j, r, _, _ := revisionFixture(t)
			if registered {
				svc.SetRegistry(newFakeRegistry())
			}
			owner := uuid.New()
			ctx := ctxWith(owner, false)
			scope := workflow.Private(owner)
			input := stampWorkflow()
			input.Name = "client-name"
			input.Nodes[0].Position = &workflow.NodePosition{X: 12, Y: 34}
			input.Presentation = &workflow.Presentation{Revision: "client-token", Retained: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, Positions: map[string]workflow.NodePosition{"a": {X: 999}}}}
			require.NoError(t, svc.Create(ctx, "destination", input))
			saved := assertSavedRequest(t, repo, r, scope, "destination", workflow.SaveReasonCreate)
			require.Len(t, r.requests, 1)
			assert.Empty(t, j.entries)
			assert.Equal(t, registered, saved.ID != uuid.Nil)
			assert.Equal(t, workflow.NodePosition{X: 12, Y: 34}, saved.Presentation.Retained.Positions["a"])
			stableID := saved.ID
			revisions := map[string]bool{saved.SavedRevision(): true}
			for _, description := range []string{"B", "A", "A", "A"} {
				update := stampWorkflow()
				update.Name = "wrong"
				update.ID = uuid.New()
				update.Description = description
				update.Nodes[0].Position = &workflow.NodePosition{X: 80, Y: 90}
				update.Nodes = append(update.Nodes, workflow.Node{ID: "new"})
				require.NoError(t, svc.Update(ctx, "destination", update))
				saved = assertSavedRequest(t, repo, r, scope, "destination", workflow.SaveReasonUpdate)
				assert.Equal(t, stableID, saved.ID)
				assert.False(t, revisions[saved.SavedRevision()])
				revisions[saved.SavedRevision()] = true
				assert.Equal(t, map[string]workflow.NodePosition{"a": {X: 12, Y: 34}}, saved.Presentation.Retained.Positions)
			}
			require.Len(t, r.requests, 5)
			// Inputs and queued snapshots cannot mutate persisted bytes or prior requests.
			input.Nodes[0].Position.X = 1000
			var first workflow.Workflow
			require.NoError(t, json.Unmarshal(r.requests[0].Manifest, &first))
			assert.Equal(t, 12.0, first.Nodes[0].Position.X)
		})
	}
}
func TestServiceRevisionUpdateDeniedBeforePrepare(t *testing.T) {
	for _, mode := range []string{"missing", "shared", "other-owner", "unauthenticated"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, j, r, _, _ := revisionFixture(t)
			owner := uuid.New()
			ctx := ctxWith(owner, false)
			if mode == "shared" {
				require.NoError(t, repo.WorkflowRepo.Create(ctx, workflow.Shared(), "flow", stampWorkflow()))
			}
			if mode == "other-owner" {
				require.NoError(t, repo.WorkflowRepo.Create(ctx, workflow.Private(uuid.New()), "flow", stampWorkflow()))
			}
			if mode == "unauthenticated" {
				ctx = context.Background()
			}
			require.Error(t, svc.Update(ctx, "flow", stampWorkflow()))
			assert.Empty(t, j.entries)
			assert.Empty(t, r.requests)
			assert.NotContains(t, *j.events, "record")
			assert.NotContains(t, *j.events, "update")
		})
	}
}
func TestServiceRevisionUpdateReloadsAfterLockAndSettlement(t *testing.T) {
	svc, repo, _, r, settler, locks := revisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, false)
	scope := workflow.Private(owner)
	prior := stampWorkflow()
	prior.ID = uuid.New()
	require.NoError(t, repo.WorkflowRepo.Create(ctx, scope, "flow", prior))
	locks.before = func() {
		newer := stampWorkflow()
		newer.ID = uuid.New()
		newer.Nodes[0].Position = &workflow.NodePosition{X: 40, Y: 50}
		prior = newer
		require.NoError(t, repo.WorkflowRepo.Update(ctx, scope, "flow", newer))
	}
	// Settlement may restore an earlier durable tree. The load must follow it.
	settler.settle = func(op context.Context, s workflow.Scope, n string) error {
		if err := requireHeld(op, s, n); err != nil {
			return err
		}
		prior.Nodes[0].Position = &workflow.NodePosition{X: 70, Y: 80}
		return repo.WorkflowRepo.Update(op, s, n, prior)
	}
	require.NoError(t, svc.Update(ctx, "flow", stampWorkflow()))
	saved := assertSavedRequest(t, repo, r, scope, "flow", workflow.SaveReasonUpdate)
	assert.Equal(t, prior.ID, saved.ID)
	assert.Equal(t, workflow.NodePosition{X: 70, Y: 80}, saved.Presentation.Retained.Positions["a"])
}

// treeRevisionRepo exercises service rollback against actual tree bytes; crash
// durability and cross-process locks belong to P2T7, not this test adapter.
type treeRevisionRepo struct {
	*revisionRepo
	commits map[string]workflow.TreeCommit
}

func (r *treeRevisionRepo) PrepareTreeCommit(ctx context.Context, d workflow.MutationTarget, rev string) (workflow.TreeCommit, error) {
	if err := requireHeld(ctx, d.Scope, d.Name); err != nil {
		return workflow.TreeCommit{}, err
	}
	*r.events = append(*r.events, "backup")
	if err := r.fail["backup"]; err != nil {
		return workflow.TreeCommit{}, err
	}
	c, err := r.WorkflowRepo.PrepareTreeCommit(ctx, d, rev)
	if err != nil {
		return workflow.TreeCommit{}, err
	}
	r.commits[c.ID] = c
	return c, nil
}
func (r *treeRevisionRepo) FinishTreeCommit(ctx context.Context, id string) error {
	*r.events = append(*r.events, "finish")
	if err := r.fail["finish"]; err != nil {
		return err
	}
	if err := r.WorkflowRepo.FinishTreeCommit(ctx, id); err != nil {
		return err
	}
	delete(r.commits, id)
	return nil
}
func (r *treeRevisionRepo) RollbackTreeCommit(ctx context.Context, id string) error {
	*r.events = append(*r.events, "rollback")
	if err := r.fail["rollback"]; err != nil {
		return err
	}
	if err := r.WorkflowRepo.RollbackTreeCommit(ctx, id); err != nil {
		return err
	}
	return r.FinishTreeCommit(ctx, id)
}
func (r *treeRevisionRepo) PendingTreeCommits(ctx context.Context) ([]workflow.TreeCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []workflow.TreeCommit
	for _, c := range r.commits {
		out = append(out, c)
	}
	return out, nil
}

type revisionPublications struct {
	*fakePublishers
	reg       *fakeRegistry
	events    *[]string
	commitErr error
}

func (p *revisionPublications) CommitPublication(ctx context.Context, name string, pub workflow.Publication) error {
	*p.events = append(*p.events, "publication")
	if err := requireHeld(ctx, workflow.Shared(), name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.commitErr != nil {
		return p.commitErr
	}
	if pub.Revision == "" || pub.WorkflowID == uuid.Nil {
		return errors.New("missing exact publication proof")
	}
	p.pub[name] = pub
	return nil
}
func (p *revisionPublications) RenamePublication(ctx context.Context, id uuid.UUID, oldName, newName string, pub workflow.Publication) error {
	*p.events = append(*p.events, "rename-publication")
	if err := requireHeld(ctx, workflow.Shared(), oldName); err != nil {
		return err
	}
	if err := requireHeld(ctx, workflow.Shared(), newName); err != nil {
		return err
	}
	if p.commitErr != nil {
		return p.commitErr
	}
	if pub.WorkflowID != id || pub.Revision == "" {
		return errors.New("invalid rename proof")
	}
	if err := p.reg.Rename(ctx, id, newName); err != nil {
		return err
	}
	delete(p.pub, oldName)
	p.pub[newName] = pub
	return nil
}
func sharedRevisionFixture(t *testing.T) (*workflow.Service, *treeRevisionRepo, *revisionPublications, *stampJournal, *stampRequester, *stampSettler, *revisionLocks) {
	t.Helper()
	_, repo, j, r, settler, locks := revisionFixture(t)
	trees := &treeRevisionRepo{revisionRepo: repo, commits: map[string]workflow.TreeCommit{}}
	pubs := &revisionPublications{fakePublishers: newFakePublishers(), reg: newFakeRegistry(), events: j.events}
	st, err := workflow.NewRevisionStamper(trees, workflow.StamperDeps{Journal: j, Requester: r, Settler: settler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(trees, pubs)
	svc.SetRegistry(pubs.reg)
	svc.SetRevisionStamper(st)
	svc.SetMutationCoordinator(locks)
	return svc, trees, pubs, j, r, settler, locks
}
func TestServiceRevisionUpdateAdminShared(t *testing.T) {
	svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	prior := stampWorkflow()
	prior.ID = uuid.New()
	prior.Nodes[0].Position = &workflow.NodePosition{X: 10, Y: 20}
	require.NoError(t, repo.WorkflowRepo.Create(ctx, workflow.Shared(), "flow", prior))
	require.NoError(t, pubs.reg.Register(ctx, workflow.RegistryEntry{ID: prior.ID, Name: "flow", Scope: workflow.ScopeShared}))
	pub := workflow.Publication{Publisher: uuid.New(), SourceName: "origin", Visibility: workflow.VisibilityDepartment, Department: "eng", Revision: "prior-proof", WorkflowID: prior.ID}
	pubs.pub["flow"] = pub
	require.NoError(t, svc.Update(ctx, "flow", stampWorkflow()))
	saved := assertSavedRequest(t, repo, r, workflow.Shared(), "flow", workflow.SaveReasonUpdate)
	assert.Equal(t, prior.ID, saved.ID)
	assert.Equal(t, workflow.NodePosition{X: 10, Y: 20}, saved.Presentation.Retained.Positions["a"])
	pub.Revision = saved.SavedRevision()
	assert.Equal(t, pub, pubs.pub["flow"])
	assert.Empty(t, repo.commits)
	assert.Empty(t, j.entries)
	assert.Equal(t, []string{"settle", "settle", "record", "backup", "update", "publication", "receipt", "request", "remove", "finish"}, *j.events)
}

func TestServiceRevisionFailureOrdering(t *testing.T) {
	for _, boundary := range []string{"settle", "record", "write", "registry", "receipt", "enqueue", "remove", "cancelled-after-commit"} {
		t.Run(boundary, func(t *testing.T) {
			svc, repo, j, r, settler, _ := revisionFixture(t)
			reg := newFakeRegistry()
			svc.SetRegistry(reg)
			boom := errors.New("injected-" + boundary)
			owner := uuid.New()
			ctx, cancel := context.WithCancel(ctxWith(owner, false))
			defer cancel()
			switch boundary {
			case "settle":
				settler.err = boom
			case "record", "receipt", "remove":
				j.fail[boundary] = boom
			case "write":
				repo.fail["write"] = boom
			case "registry":
				reg.failReg = boom
			case "enqueue":
				r.err = boom
			case "cancelled-after-commit":
				repo.afterWrite = cancel
			}
			err := svc.Create(ctx, "flow", stampWorkflow())
			committed := boundary == "receipt" || boundary == "enqueue" || boundary == "remove" || boundary == "cancelled-after-commit"
			if !committed {
				require.ErrorIs(t, err, boom)
				assert.Empty(t, r.requests)
				assert.Empty(t, j.entries)
				assert.Equal(t, 0, reg.count())
				_, err := repo.Load(context.Background(), workflow.Private(owner), "flow")
				assert.ErrorIs(t, err, workflow.ErrNotFound)
			} else {
				require.NoError(t, err)
				saved, err := repo.Load(context.Background(), workflow.Private(owner), "flow")
				require.NoError(t, err)
				require.NotEmpty(t, saved.SavedRevision())
				if boundary == "receipt" {
					require.Empty(t, r.requests)
					assert.Equal(t, workflow.JournalPrepared, j.entries[saved.SavedRevision()].State)
				}
				if boundary == "enqueue" {
					require.Empty(t, r.requests)
					assert.Equal(t, workflow.JournalCommitted, j.entries[saved.SavedRevision()].State)
					assert.NotContains(t, *j.events, "remove")
				}
				if boundary == "remove" {
					require.Len(t, r.requests, 1)
					assert.Equal(t, workflow.JournalCommitted, j.entries[saved.SavedRevision()].State)
				}
				if boundary == "cancelled-after-commit" {
					require.Len(t, r.requests, 1)
					assert.Empty(t, j.entries)
				}
				if boundary == "receipt" || boundary == "enqueue" {
					entry := j.entries[saved.SavedRevision()]
					assert.JSONEq(t, jsonValue(t, saved), string(entry.Request.Manifest))
				}
			}
		})
	}
}
func TestServiceRevisionSharedFailureRestoresBeforeAbandon(t *testing.T) {
	for _, boundary := range []string{"backup", "write", "publication", "rollback", "finish", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
			owner := uuid.New()
			ctx := ctxWith(owner, true)
			prior := stampWorkflow()
			prior.ID = uuid.New()
			prior.Description = "A"
			require.NoError(t, repo.WorkflowRepo.Create(ctx, workflow.Shared(), "flow", prior))
			require.NoError(t, pubs.reg.Register(ctx, workflow.RegistryEntry{ID: prior.ID, Name: "flow", Scope: workflow.ScopeShared}))
			pubs.pub["flow"] = workflow.Publication{Publisher: owner, Visibility: workflow.VisibilityPublic, WorkflowID: prior.ID, Revision: "A-proof"}
			boom := errors.New("injected-" + boundary)
			switch boundary {
			case "publication":
				pubs.commitErr = boom
			case "rollback":
				pubs.commitErr = boom
				repo.fail["rollback"] = errors.New("rollback also failed")
			case "receipt":
				j.fail["receipt"] = boom
			default:
				repo.fail[boundary] = boom
			}
			update := stampWorkflow()
			update.Description = "B"
			err := svc.Update(ctx, "flow", update)
			saved, lerr := repo.Load(ctx, workflow.Shared(), "flow")
			require.NoError(t, lerr)
			switch boundary {
			case "finish", "receipt":
				require.NoError(t, err)
				assert.Equal(t, "B", saved.Description)
				assert.NotEmpty(t, repo.commits)
				if boundary == "receipt" {
					assert.Empty(t, r.requests)
					assert.NotContains(t, *j.events, "finish")
				} else {
					require.Len(t, r.requests, 1)
				}
			case "rollback":
				require.ErrorIs(t, err, boom)
				assert.Contains(t, err.Error(), "rollback also failed")
				assert.NotEmpty(t, j.entries)
				assert.NotEmpty(t, repo.commits)
				assert.NotContains(t, *j.events, "remove")
				assert.Empty(t, r.requests)
			default:
				require.ErrorIs(t, err, boom)
				assert.JSONEq(t, jsonValue(t, prior), jsonValue(t, saved))
				assert.Empty(t, j.entries)
				assert.Empty(t, r.requests)
				assert.Equal(t, "A-proof", pubs.pub["flow"].Revision)
				if boundary == "publication" {
					assert.Less(t, indexOf(*j.events, "rollback"), indexOf(*j.events, "remove"))
				}
			}
		})
	}
}
func indexOf(events []string, event string) int {
	for i, s := range events {
		if s == event {
			return i
		}
	}
	return -1
}
func TestServiceRevisionUpdateAdoptsRegisteredIdentity(t *testing.T) {
	svc, repo, _, r, _, _ := revisionFixture(t)
	reg := newFakeRegistry()
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)
	id := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: id, Name: "flow", Scope: workflow.ScopePrivate, OwnerID: owner}))
	require.NoError(t, repo.WorkflowRepo.Create(ctx, workflow.Private(owner), "flow", stampWorkflow()))
	require.NoError(t, svc.Update(ctx, "flow", stampWorkflow()))
	saved := assertSavedRequest(t, repo, r, workflow.Private(owner), "flow", workflow.SaveReasonUpdate)
	assert.Equal(t, id, saved.ID)
}

func TestServiceRevisionPublishAndCopy(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "overwrite"}[overwrite], func(t *testing.T) {
			svc, repo, pubs, j, r, _, locks := sharedRevisionFixture(t)
			owner := uuid.New()
			ctx := ctxWith(owner, true)
			require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
			source := assertSavedRequest(t, repo, r, workflow.Private(owner), "flow", workflow.SaveReasonCreate)
			require.NoError(t, repo.WorkflowRepo.WriteFile(ctx, workflow.Private(owner), "flow", "notes.txt", []byte("source-file")))
			require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
			saved := assertSavedRequest(t, repo, r, workflow.Shared(), "public", workflow.SaveReasonPublish)
			assert.NotEqual(t, source.SavedRevision(), saved.SavedRevision())
			assert.NotEqual(t, source.ID, saved.ID)
			assert.Equal(t, saved.SavedRevision(), pubs.pub["public"].Revision)
			assert.Equal(t, saved.ID, pubs.pub["public"].WorkflowID)
			sharedID := saved.ID
			if overwrite {
				update := stampWorkflow()
				update.Description = "B"
				require.NoError(t, svc.Update(ctx, "flow", update))
				require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
				newer := assertSavedRequest(t, repo, r, workflow.Shared(), "public", workflow.SaveReasonPublish)
				assert.Equal(t, sharedID, newer.ID)
				assert.NotEqual(t, saved.SavedRevision(), newer.SavedRevision())
				assert.Equal(t, "B", newer.Description)
			}
			require.NoError(t, svc.Copy(ctx, "public", "copied"))
			copied := assertSavedRequest(t, repo, r, workflow.Private(owner), "copied", workflow.SaveReasonCopy)
			assert.NotEqual(t, saved.SavedRevision(), copied.SavedRevision())
			sourceNow, err := repo.Load(ctx, workflow.Private(owner), "flow")
			require.NoError(t, err)
			if !overwrite {
				assert.JSONEq(t, jsonValue(t, source), jsonValue(t, sourceNow))
			}
			assert.Contains(t, locks.held, workflow.MutationTarget{Scope: workflow.Shared(), Name: "public"})
			assert.Contains(t, locks.held, workflow.MutationTarget{Scope: workflow.Private(owner), Name: "copied"})
			assert.Empty(t, j.entries)
			assert.Empty(t, repo.commits)
		})
	}
}
func TestServiceRevisionPublishFailureRestoresWholeTree(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "overwrite"}[overwrite], func(t *testing.T) {
			svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
			owner := uuid.New()
			ctx := ctxWith(owner, true)
			require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
			var prior *workflow.Workflow
			var proof workflow.Publication
			var priorBytes []byte
			destinationDir := filepath.Join(repo.base, "shared", "public")
			if overwrite {
				require.NoError(t, repo.WorkflowRepo.WriteFile(ctx, workflow.Private(owner), "flow", "notes.txt", []byte("A")))
				require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
				prior, _ = repo.Load(ctx, workflow.Shared(), "public")
				proof = pubs.pub["public"]
				var err error
				priorBytes, err = os.ReadFile(filepath.Join(destinationDir, "workflow.json"))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(destinationDir, "layout.json"), []byte("prior cache bytes\n"), 0600))
			}
			require.NoError(t, repo.WorkflowRepo.WriteFile(ctx, workflow.Private(owner), "flow", "notes.txt", []byte("B")))
			require.NoError(t, os.WriteFile(filepath.Join(repo.base, "users", owner.String(), "flow", "layout.json"), []byte("source cache bytes\n"), 0600))
			before := len(r.requests)
			boom := errors.New("publication unavailable")
			pubs.commitErr = boom
			require.ErrorIs(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""), boom)
			assert.Len(t, r.requests, before)
			assert.Empty(t, j.entries)
			assert.Empty(t, repo.commits)
			saved, err := repo.Load(ctx, workflow.Shared(), "public")
			if overwrite {
				require.NoError(t, err)
				assert.JSONEq(t, jsonValue(t, prior), jsonValue(t, saved))
				assert.Equal(t, proof, pubs.pub["public"])
				restoredBytes, err := os.ReadFile(filepath.Join(destinationDir, "workflow.json"))
				require.NoError(t, err)
				assert.Equal(t, priorBytes, restoredBytes, "restore the exact prior manifest bytes")
				cache, err := os.ReadFile(filepath.Join(destinationDir, "layout.json"))
				require.NoError(t, err)
				assert.Equal(t, []byte("prior cache bytes\n"), cache, "restore the destination cache, not the source cache")
				// Re-copy the restored tree through the real repository to reread files.
				dst := workflow.Private(uuid.New())
				require.NoError(t, repo.WorkflowRepo.CopyTree(ctx, workflow.Shared(), "public", dst, "check", false))
				b, err := os.ReadFile(filepath.Join(repo.base, "users", dst.OwnerID.String(), "check", "notes.txt"))
				require.NoError(t, err)
				assert.Equal(t, "A", string(b))
			} else {
				assert.ErrorIs(t, err, workflow.ErrNotFound)
				_, err := pubs.reg.ResolveID(ctx, "public")
				assert.ErrorIs(t, err, workflow.ErrNotFound)
			}
		})
	}
}

func TestServiceRevisionRenameDuplicateTransfer(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "shared"}[shared], func(t *testing.T) {
			svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
			owner := uuid.New()
			ctx := ctxWith(owner, true)
			scope := workflow.Private(owner)
			require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
			name := "flow"
			if shared {
				name = "public"
				scope = workflow.Shared()
				require.NoError(t, svc.Publish(ctx, "flow", name, workflow.VisibilityPublic, ""))
			}
			before, err := repo.Load(ctx, scope, name)
			require.NoError(t, err)
			count := len(r.requests)
			require.NoError(t, svc.Duplicate(ctx, name, "duplicate"))
			duplicated := assertSavedRequest(t, repo, r, workflow.Private(owner), "duplicate", workflow.SaveReasonDuplicate)
			assert.NotEqual(t, before.ID, duplicated.ID)
			assert.NotEqual(t, before.SavedRevision(), duplicated.SavedRevision())
			unchanged, err := repo.Load(ctx, scope, name)
			require.NoError(t, err)
			assert.JSONEq(t, jsonValue(t, before), jsonValue(t, unchanged))
			assert.Len(t, r.requests, count+1)
			require.NoError(t, svc.Rename(ctx, name, "renamed"))
			renamed := assertSavedRequest(t, repo, r, scope, "renamed", workflow.SaveReasonRename)
			assert.Equal(t, before.ID, renamed.ID)
			assert.NotEqual(t, before.SavedRevision(), renamed.SavedRevision())
			_, err = repo.Load(ctx, scope, name)
			assert.ErrorIs(t, err, workflow.ErrNotFound)
			if shared {
				assert.Equal(t, renamed.SavedRevision(), pubs.pub["renamed"].Revision)
				_, exists := pubs.pub[name]
				assert.False(t, exists)
			}
			newOwner := uuid.New()
			count = len(r.requests)
			require.NoError(t, svc.TransferOwnership(ctx, "renamed", newOwner))
			target := scope
			if !shared {
				target = workflow.Private(newOwner)
			}
			moved, err := repo.Load(ctx, target, "renamed")
			require.NoError(t, err)
			assert.JSONEq(t, jsonValue(t, renamed), jsonValue(t, moved))
			assert.Len(t, r.requests, count)
			assert.Empty(t, j.intents)
			entry, err := pubs.reg.Resolve(ctx, "renamed")
			require.NoError(t, err)
			assert.Equal(t, newOwner, entry.OwnerID)
			// Already enqueued historical records follow a private owner move without
			// changing the immutable manifest captured at the original destination.
			if !shared {
				for _, req := range r.requests {
					if req.WorkflowID == renamed.ID {
						assert.Equal(t, target, req.Scope)
						assert.Equal(t, "renamed", req.Name)
					}
				}
			}
		})
	}
}
func TestServiceRevisionMoveRollbackAndPostcommitCleanup(t *testing.T) {
	for _, operation := range []string{"rename", "duplicate", "transfer"} {
		for _, failure := range []string{"registry", "cleanup", "cancelled"} {
			t.Run(operation+"-"+failure, func(t *testing.T) {
				svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
				owner := uuid.New()
				ctx, cancel := context.WithCancel(ctxWith(owner, true))
				defer cancel()
				require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
				before, err := repo.Load(ctx, workflow.Private(owner), "flow")
				require.NoError(t, err)
				count := len(r.requests)
				boom := errors.New("registry failure")
				target := workflow.Private(owner)
				targetName := "target"
				if operation == "transfer" {
					target = workflow.Private(uuid.New())
					targetName = "flow"
				}
				switch failure {
				case "registry":
					if operation == "rename" {
						pubs.reg.failRename = boom
					}
					if operation == "duplicate" {
						pubs.reg.failReg = boom
					}
					if operation == "transfer" {
						pubs.reg.failSetOwner = boom
					}
				case "cleanup":
					repo.fail["delete:flow"] = boom
				case "cancelled":
					cancel()
				}
				switch operation {
				case "rename":
					err = svc.Rename(ctx, "flow", targetName)
				case "duplicate":
					err = svc.Duplicate(ctx, "flow", targetName)
				case "transfer":
					err = svc.TransferOwnership(ctx, "flow", target.OwnerID)
				}
				if failure == "registry" || failure == "cancelled" {
					require.Error(t, err)
					assert.Len(t, r.requests, count)
					assert.Empty(t, j.entries)
					_, err = repo.Load(context.Background(), target, targetName)
					assert.ErrorIs(t, err, workflow.ErrNotFound)
					source, err := repo.Load(context.Background(), workflow.Private(owner), "flow")
					require.NoError(t, err)
					assert.JSONEq(t, jsonValue(t, before), jsonValue(t, source))
				} else if operation != "duplicate" {
					require.ErrorIs(t, err, boom)
					assert.NotEmpty(t, j.intents)
					saved, err := repo.Load(ctx, target, targetName)
					require.NoError(t, err)
					assert.NotEmpty(t, saved.SavedRevision())
					if operation == "rename" {
						assert.Len(t, r.requests, count+1)
						assertSavedRequest(t, repo, r, target, targetName, workflow.SaveReasonRename)
						assert.Equal(t, before.ID, saved.ID)
						assert.NotEqual(t, before.SavedRevision(), saved.SavedRevision())
					} else {
						assert.Len(t, r.requests, count)
						assert.JSONEq(t, jsonValue(t, before), jsonValue(t, saved))
					}
					for _, intent := range j.intents {
						assert.Equal(t, before.ID, intent.WorkflowID)
						assert.Equal(t, workflow.Private(owner), intent.From)
						assert.Equal(t, "flow", intent.FromName)
						assert.Equal(t, target, intent.To)
						assert.Equal(t, targetName, intent.ToName)
					}
				}
			})
		}
	}
}

func TestServiceRevisionNonAuthoringAndDelete(t *testing.T) {
	svc, repo, pubs, j, r, settler, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	svc.SetActiveInstanceCounter(&fakeActiveCounter{})
	require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
	saved, err := repo.Load(ctx, workflow.Private(owner), "flow")
	require.NoError(t, err)
	count := len(r.requests)
	*j.events = nil
	_, err = svc.Load(ctx, "flow")
	require.NoError(t, err)
	_, err = svc.List(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.WriteFile(ctx, "flow", "notes.txt", []byte("notes")))
	_, err = svc.ListFiles(ctx, "flow")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteFile(ctx, "flow", "notes.txt"))
	require.NoError(t, svc.Archive(ctx, "flow"))
	require.NoError(t, svc.Unarchive(ctx, "flow"))
	require.NoError(t, svc.BackfillRegistry(ctx))
	after, err := repo.Load(ctx, workflow.Private(owner), "flow")
	require.NoError(t, err)
	assert.JSONEq(t, jsonValue(t, saved), jsonValue(t, after))
	assert.Len(t, r.requests, count)
	assert.NotContains(t, *j.events, "record")
	// A missed receipt must be settled before any destructive removal.
	pending := workflow.ArrangementRequest{Revision: saved.SavedRevision(), Scope: workflow.Private(owner), Name: "flow", WorkflowID: saved.ID, Manifest: []byte(jsonValue(t, saved))}
	j.entries[pending.Revision] = workflow.JournalEntry{Request: pending, State: workflow.JournalPrepared}
	settler.settle = func(op context.Context, scope workflow.Scope, name string) error {
		if err := requireHeld(op, scope, name); err != nil {
			return err
		}
		if scope == pending.Scope && name == pending.Name {
			return j.MarkCommitted(op, pending.Revision)
		}
		return nil
	}
	require.NoError(t, svc.Delete(ctx, "flow"))
	assert.Equal(t, workflow.JournalCommitted, j.entries[pending.Revision].State)
	assert.Less(t, indexOf(*j.events, "receipt"), indexOf(*j.events, "delete:flow"))
	assert.Len(t, r.requests, count)
	_, err = pubs.reg.ResolveID(ctx, "flow")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}
func TestServiceRevisionBackfillPreservesPresentation(t *testing.T) {
	svc, repo, pubs, j, r, _, locks := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	// Produce a real saved manifest in local mode, then reconcile its identity.
	local := workflow.NewWorkflowService(repo, nil)
	st, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: j, Requester: r, Settler: &stampSettler{events: j.events, settle: func(op context.Context, s workflow.Scope, n string) error { return requireHeld(op, s, n) }}})
	require.NoError(t, err)
	local.SetRevisionStamper(st)
	local.SetMutationCoordinator(locks)
	require.NoError(t, local.Create(ctx, "legacy", stampWorkflow()))
	before, err := repo.Load(ctx, workflow.Private(owner), "legacy")
	require.NoError(t, err)
	count := len(r.requests)
	*j.events = nil
	require.NoError(t, svc.BackfillRegistry(ctx))
	after, err := repo.Load(ctx, workflow.Private(owner), "legacy")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, after.ID)
	before.ID = after.ID
	assert.JSONEq(t, jsonValue(t, before), jsonValue(t, after))
	assert.Len(t, r.requests, count)
	assert.NotContains(t, *j.events, "record")
	id, err := pubs.reg.ResolveID(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, after.ID, id)
}
func TestServiceRevisionVisibilityAndUnpublish(t *testing.T) {
	svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	svc.SetActiveInstanceCounter(&fakeActiveCounter{})
	require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
	require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
	before, err := repo.Load(ctx, workflow.Shared(), "public")
	require.NoError(t, err)
	proof := pubs.pub["public"]
	count := len(r.requests)
	*j.events = nil
	require.NoError(t, svc.SetVisibility(ctx, "public", workflow.VisibilityDepartment, "engineering"))
	saved, err := repo.Load(ctx, workflow.Shared(), "public")
	require.NoError(t, err)
	assert.JSONEq(t, jsonValue(t, before), jsonValue(t, saved))
	assert.Equal(t, proof.Revision, pubs.pub["public"].Revision)
	assert.Equal(t, proof.WorkflowID, pubs.pub["public"].WorkflowID)
	require.NoError(t, svc.Unpublish(ctx, "public"))
	assert.Len(t, r.requests, count)
	assert.NotContains(t, *j.events, "record")
	assert.Less(t, indexOf(*j.events, "settle"), indexOf(*j.events, "delete:public"))
}

func TestServiceRevisionRollbackFailureKeepsPrepare(t *testing.T) {
	svc, repo, j, r, _, _ := revisionFixture(t)
	reg := newFakeRegistry()
	svc.SetRegistry(reg)
	writeErr, rollbackErr := errors.New("write failed"), errors.New("registry rollback failed")
	repo.fail["write"] = writeErr
	reg.failDel = rollbackErr
	err := svc.Create(ctxWith(uuid.New(), false), "flow", stampWorkflow())
	require.ErrorIs(t, err, writeErr)
	require.ErrorIs(t, err, rollbackErr)
	assert.Len(t, j.entries, 1)
	assert.Empty(t, r.requests)
	assert.NotContains(t, *j.events, "remove")
}
func TestServiceRevisionSharedCancellationRestoresPrior(t *testing.T) {
	svc, repo, _, j, r, _, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx, cancel := context.WithCancel(ctxWith(owner, true))
	defer cancel()
	require.NoError(t, svc.Create(ctx, "source", stampWorkflow()))
	require.NoError(t, svc.Publish(ctx, "source", "public", workflow.VisibilityPublic, ""))
	prior, err := repo.Load(ctx, workflow.Shared(), "public")
	require.NoError(t, err)
	count := len(r.requests)
	repo.afterWrite = cancel
	changed := stampWorkflow()
	changed.Description = "uncommitted"
	require.ErrorIs(t, svc.Update(ctx, "public", changed), context.Canceled)
	saved, err := repo.Load(context.Background(), workflow.Shared(), "public")
	require.NoError(t, err)
	assert.JSONEq(t, jsonValue(t, prior), jsonValue(t, saved))
	assert.Empty(t, j.entries)
	assert.Empty(t, repo.commits)
	assert.Len(t, r.requests, count)
}

func TestServiceRevisionWiring(t *testing.T) {
	t.Run("missing-coordinator", func(t *testing.T) {
		svc, repo, j, r, _, _ := revisionFixture(t)
		svc.SetMutationCoordinator(nil)
		err := svc.Create(ctxWith(uuid.New(), false), "flow", stampWorkflow())
		require.ErrorIs(t, err, workflow.ErrMutationMisconfigured)
		assert.Empty(t, j.entries)
		assert.Empty(t, r.requests)
		summaries, err := repo.List(context.Background(), workflow.Shared())
		require.NoError(t, err)
		assert.Empty(t, summaries)
	})
	t.Run("legacy-isolated", func(t *testing.T) {
		_, repo, j, r, _, _ := revisionFixture(t)
		svc := workflow.NewWorkflowService(repo.WorkflowRepo, nil)
		owner := uuid.New()
		require.NoError(t, svc.Create(ctxWith(owner, false), "flow", stampWorkflow()))
		saved, err := repo.Load(context.Background(), workflow.Private(owner), "flow")
		require.NoError(t, err)
		assert.Nil(t, saved.Presentation)
		assert.Empty(t, j.entries)
		assert.Empty(t, r.requests)
	})
	t.Run("missing-publication-proof", func(t *testing.T) {
		calls := 0
		err := workflow.WriteAndRecord(context.Background(), newFakePublishers(), workflow.PublishRequest{
			PublishedName: "public", Publisher: uuid.New(), WriteTree: func(context.Context, bool) error { calls++; return nil },
			Rollback: func(context.Context) error { return nil }, PreparedPublication: func() (workflow.Publication, error) { return workflow.Publication{}, nil },
		})
		require.ErrorIs(t, err, workflow.ErrMutationMisconfigured)
		assert.Zero(t, calls)
	})
	t.Run("missing-trees", func(t *testing.T) {
		svc, repo, j, r, _, locks := revisionFixture(t)
		pubs := &revisionPublications{fakePublishers: newFakePublishers(), reg: newFakeRegistry(), events: j.events}
		repositoryOnly := struct{ workflow.Repository }{repo}
		svc = workflow.NewWorkflowService(repositoryOnly, pubs)
		st, err := workflow.NewRevisionStamper(repositoryOnly, workflow.StamperDeps{Journal: j, Requester: r, Settler: &stampSettler{events: j.events}})
		require.NoError(t, err)
		svc.SetRevisionStamper(st)
		svc.SetMutationCoordinator(locks)
		require.ErrorIs(t, svc.Publish(ctxWith(uuid.New(), true), "flow", "public", workflow.VisibilityPublic, ""), workflow.ErrMutationMisconfigured)
		assert.Empty(t, j.entries)
		assert.Empty(t, r.requests)
	})
}
func TestServiceRevisionCopySourceIdentityAndFailure(t *testing.T) {
	for _, sourceID := range []uuid.UUID{uuid.Nil, uuid.New()} {
		for _, stampFails := range []bool{false, true} {
			t.Run(sourceID.String()+map[bool]string{false: "-success", true: "-failed"}[stampFails], func(t *testing.T) {
				svc, repo, j, r, _, _ := revisionFixture(t)
				owner := uuid.New()
				ctx := ctxWith(owner, false)
				source := stampWorkflow()
				source.ID = sourceID
				source.Presentation = &workflow.Presentation{Revision: "foreign-token"}
				require.NoError(t, repo.WorkflowRepo.Create(ctx, workflow.Shared(), "flow", source))
				if stampFails {
					j.fail["record"] = errors.New("journal unavailable")
				}
				err := svc.Copy(ctx, "flow", "copy")
				if stampFails {
					require.Error(t, err)
					assert.Empty(t, r.requests)
					_, err = repo.Load(ctx, workflow.Private(owner), "copy")
					assert.ErrorIs(t, err, workflow.ErrNotFound)
				} else {
					require.NoError(t, err)
					require.Len(t, r.requests, 1)
					saved := assertSavedRequest(t, repo, r, workflow.Private(owner), "copy", workflow.SaveReasonCopy)
					assert.NotEqual(t, uuid.Nil, saved.ID)
					assert.NotEqual(t, sourceID, saved.ID, "an independent copy must not inherit the source identity")
					assert.NotEqual(t, "foreign-token", saved.SavedRevision())
					require.NoError(t, svc.Copy(ctx, "flow", "second-copy"))
					second := assertSavedRequest(t, repo, r, workflow.Private(owner), "second-copy", workflow.SaveReasonCopy)
					assert.NotEqual(t, saved.ID, second.ID, "each destination needs its own identity")
					assert.NotEqual(t, saved.SavedRevision(), second.SavedRevision())
				}
				unchanged, err := repo.Load(ctx, workflow.Shared(), "flow")
				require.NoError(t, err)
				assert.JSONEq(t, jsonValue(t, source), jsonValue(t, unchanged))
			})
		}
	}
}
func TestServiceRevisionCopyPublishAuthorizationAndCollision(t *testing.T) {
	for _, scenario := range []string{"hidden-source", "other-publisher", "same-name", "copy-collision", "missing-source"} {
		t.Run(scenario, func(t *testing.T) {
			svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
			owner := uuid.New()
			ctx := ctxWith(owner, true)
			require.NoError(t, svc.Create(ctx, "source", stampWorkflow()))
			var err error
			switch scenario {
			case "hidden-source":
				require.NoError(t, svc.Publish(ctx, "source", "hidden", workflow.VisibilityDepartment, "secret"))
				before := len(r.requests)
				err = svc.Copy(ctxWith(uuid.New(), false), "hidden", "copy")
				assert.Len(t, r.requests, before)
				require.ErrorIs(t, err, workflow.ErrNotFound)
			case "other-publisher":
				require.NoError(t, svc.Publish(ctx, "source", "public", workflow.VisibilityPublic, ""))
				other := uuid.New()
				otherCtx := ctxWith(other, true)
				require.NoError(t, svc.Create(otherCtx, "other", stampWorkflow()))
				before := len(r.requests)
				err = svc.Publish(otherCtx, "other", "public", workflow.VisibilityPublic, "")
				assert.Len(t, r.requests, before)
				require.ErrorIs(t, err, workflow.ErrUnauthorized)
			case "same-name":
				err = svc.Publish(ctx, "source", "source", workflow.VisibilityPublic, "")
				require.ErrorIs(t, err, workflow.ErrAlreadyExists)
			case "copy-collision":
				require.NoError(t, svc.Publish(ctx, "source", "public", workflow.VisibilityPublic, ""))
				err = svc.Copy(ctx, "public", "source")
				require.ErrorIs(t, err, workflow.ErrAlreadyExists)
			case "missing-source":
				err = svc.Publish(ctx, "missing", "public", workflow.VisibilityPublic, "")
				require.ErrorIs(t, err, workflow.ErrNotFound)
			}
			assert.Empty(t, j.entries)
			assert.Empty(t, repo.commits)
			assert.Equal(t, "source", r.requests[0].Name)
			_ = pubs
		})
	}
}
func TestServiceRevisionOperationFailureBoundaries(t *testing.T) {
	for _, operation := range []string{"update", "publish", "copy", "duplicate", "rename", "transfer", "delete", "unpublish"} {
		for _, boundary := range []string{"settle", "record", "write", "receipt", "enqueue", "remove", "relocate", "begin", "end"} {
			t.Run(operation+"-"+boundary, func(t *testing.T) {
				authoring := operation != "transfer" && operation != "delete" && operation != "unpublish"
				relocation := operation == "transfer" || operation == "rename"
				if !authoring && (boundary == "record" || boundary == "receipt" || boundary == "enqueue" || boundary == "remove") || !relocation && (boundary == "relocate" || boundary == "begin" || boundary == "end") {
					t.Skip("not an operation boundary")
				}
				svc, repo, _, j, r, settler, _ := sharedRevisionFixture(t)
				owner := uuid.New()
				ctx := ctxWith(owner, true)
				svc.SetActiveInstanceCounter(&fakeActiveCounter{})
				require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
				require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
				boom := errors.New("boundary-" + boundary)
				count := len(r.requests)
				*j.events = nil
				switch boundary {
				case "settle":
					settler.err = boom
				case "record", "receipt", "remove", "relocate", "begin", "end":
					j.fail[boundary] = boom
				case "enqueue":
					r.err = boom
				case "write":
					switch operation {
					case "update":
						repo.fail["write"] = boom
					case "delete", "transfer":
						repo.fail["delete:flow"] = boom
					case "unpublish":
						repo.fail["delete:public"] = boom
					default:
						repo.fail["copy"] = boom
					}
				}
				var err error
				switch operation {
				case "update":
					err = svc.Update(ctx, "flow", stampWorkflow())
				case "publish":
					err = svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, "")
				case "copy":
					err = svc.Copy(ctx, "public", "target")
				case "duplicate":
					err = svc.Duplicate(ctx, "flow", "target")
				case "rename":
					err = svc.Rename(ctx, "flow", "target")
				case "transfer":
					err = svc.TransferOwnership(ctx, "flow", uuid.New())
				case "delete":
					err = svc.Delete(ctx, "flow")
				case "unpublish":
					err = svc.Unpublish(ctx, "public")
				}
				if boundary == "settle" || boundary == "record" || boundary == "begin" || boundary == "write" {
					require.ErrorIs(t, err, boom)
					assert.Len(t, r.requests, count)
				} else {
					require.NoError(t, err)
					if boundary == "enqueue" || boundary == "receipt" {
						assert.Len(t, r.requests, count)
						assert.NotEmpty(t, j.entries)
					}
					if relocation && (boundary == "relocate" || boundary == "end") {
						assert.NotEmpty(t, j.intents)
					}
				}
			})
		}
	}
}

func TestServiceRevisionIdenticalAndPositionOnlySaves(t *testing.T) {
	svc, repo, _, r, _, _ := revisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, false)
	scope := workflow.Private(owner)
	input := stampWorkflow()
	input.Description = "A"
	require.NoError(t, svc.Create(ctx, "flow", input))
	revisions := map[string]bool{input.SavedRevision(): true}
	for _, change := range []func(*workflow.Workflow){
		func(w *workflow.Workflow) { w.Description = "B" },
		func(w *workflow.Workflow) { w.Description = "A" },
		func(*workflow.Workflow) {},
		func(w *workflow.Workflow) { w.Nodes[0].Position = &workflow.NodePosition{X: 50, Y: 60} },
	} {
		current, err := repo.Load(ctx, scope, "flow")
		require.NoError(t, err)
		change(current)
		require.NoError(t, svc.Update(ctx, "flow", current))
		saved := assertSavedRequest(t, repo, r, scope, "flow", workflow.SaveReasonUpdate)
		assert.False(t, revisions[saved.SavedRevision()])
		revisions[saved.SavedRevision()] = true
	}
	require.Len(t, r.requests, 5)
}

type gatedRevisionLocks struct {
	base    *revisionLocks
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *gatedRevisionLocks) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	first := false
	g.once.Do(func() { first = true })
	if first {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.base.WithMutations(ctx, targets, fn)
}
func TestServiceRevisionBackfillRacesUpdate(t *testing.T) {
	svc, repo, _, j, r, _, locks := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	local := workflow.NewWorkflowService(repo, nil)
	st, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: j, Requester: r, Settler: &stampSettler{events: j.events, settle: func(op context.Context, s workflow.Scope, n string) error { return requireHeld(op, s, n) }}})
	require.NoError(t, err)
	local.SetRevisionStamper(st)
	local.SetMutationCoordinator(locks)
	require.NoError(t, local.Create(ctx, "flow", stampWorkflow()))
	gate := &gatedRevisionLocks{base: locks, entered: make(chan struct{}), release: make(chan struct{})}
	svc.SetMutationCoordinator(gate)
	done := make(chan error, 1)
	go func() { done <- svc.BackfillRegistry(ctx) }()
	<-gate.entered
	changed := stampWorkflow()
	changed.Description = "newer-authoring-save"
	require.NoError(t, svc.Update(ctx, "flow", changed))
	newest, err := repo.Load(ctx, workflow.Private(owner), "flow")
	require.NoError(t, err)
	count := len(r.requests)
	close(gate.release)
	require.NoError(t, <-done)
	after, err := repo.Load(ctx, workflow.Private(owner), "flow")
	require.NoError(t, err)
	newest.ID = after.ID
	assert.JSONEq(t, jsonValue(t, newest), jsonValue(t, after))
	assert.Len(t, r.requests, count)
	assert.Equal(t, changed.SavedRevision(), after.SavedRevision())
}

type cancellingRevisionRegistry struct {
	*fakeRegistry
	cancel context.CancelFunc
}

func (r *cancellingRevisionRegistry) Rename(ctx context.Context, id uuid.UUID, name string) error {
	if err := r.fakeRegistry.Rename(ctx, id, name); err != nil {
		return err
	}
	r.cancel()
	return nil
}
func (r *cancellingRevisionRegistry) SetOwner(ctx context.Context, id, owner uuid.UUID) error {
	if err := r.fakeRegistry.SetOwner(ctx, id, owner); err != nil {
		return err
	}
	r.cancel()
	return nil
}
func TestServiceRevisionMoveSurvivesCancellationAfterIdentityCommit(t *testing.T) {
	for _, rename := range []bool{false, true} {
		t.Run(map[bool]string{false: "transfer", true: "rename"}[rename], func(t *testing.T) {
			svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
			owner := uuid.New()
			ctx, cancel := context.WithCancel(ctxWith(owner, true))
			defer cancel()
			require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
			count := len(r.requests)
			svc.SetRegistry(&cancellingRevisionRegistry{fakeRegistry: pubs.reg, cancel: cancel})
			target, targetName := workflow.Private(uuid.New()), "flow"
			if rename {
				target, targetName = workflow.Private(owner), "target"
				require.NoError(t, svc.Rename(ctx, "flow", targetName))
			} else {
				require.NoError(t, svc.TransferOwnership(ctx, "flow", target.OwnerID))
			}
			saved, err := repo.Load(context.Background(), target, targetName)
			require.NoError(t, err)
			assert.NotEmpty(t, saved.SavedRevision())
			assert.Empty(t, j.intents)
			assert.Empty(t, j.entries)
			if rename {
				assert.Len(t, r.requests, count+1)
			} else {
				assert.Len(t, r.requests, count)
			}
		})
	}
}
func TestServiceRevisionSharedRenameProofFailure(t *testing.T) {
	svc, repo, pubs, j, r, _, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
	require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
	before, err := repo.Load(ctx, workflow.Shared(), "public")
	require.NoError(t, err)
	proof := pubs.pub["public"]
	count := len(r.requests)
	pubs.commitErr = errors.New("atomic rename failed")
	require.ErrorIs(t, svc.Rename(ctx, "public", "target"), pubs.commitErr)
	unchanged, err := repo.Load(ctx, workflow.Shared(), "public")
	require.NoError(t, err)
	assert.JSONEq(t, jsonValue(t, before), jsonValue(t, unchanged))
	assert.Equal(t, proof, pubs.pub["public"])
	_, err = repo.Load(ctx, workflow.Shared(), "target")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	name, err := pubs.reg.ResolveName(ctx, before.ID)
	require.NoError(t, err)
	assert.Equal(t, "public", name)
	assert.Len(t, r.requests, count)
	assert.Empty(t, j.entries)
	assert.Empty(t, j.intents)
	assert.Empty(t, repo.commits)
}

func TestServiceRevisionPublishRechecksSourceOwnerUnderLock(t *testing.T) {
	svc, repo, pubs, j, r, _, locks := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
	count := len(r.requests)
	entry, err := pubs.reg.Resolve(ctx, "flow")
	require.NoError(t, err)
	locks.before = func() { require.NoError(t, pubs.reg.SetOwner(ctx, entry.ID, uuid.New())) }
	require.ErrorIs(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""), workflow.ErrNotFound)
	assert.Len(t, r.requests, count)
	assert.Empty(t, j.entries)
	_, err = repo.Load(ctx, workflow.Shared(), "public")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}
func TestServiceRevisionDeleteRechecksIdentityAfterSettlement(t *testing.T) {
	svc, repo, pubs, j, r, settler, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	svc.SetActiveInstanceCounter(&fakeActiveCounter{})
	require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
	count := len(r.requests)
	entry, err := pubs.reg.Resolve(ctx, "flow")
	require.NoError(t, err)
	newOwner := uuid.New()
	require.NoError(t, repo.WorkflowRepo.CopyTree(ctx, workflow.Private(owner), "flow", workflow.Private(newOwner), "flow", false))
	settler.settle = func(op context.Context, s workflow.Scope, n string) error {
		return pubs.reg.SetOwner(op, entry.ID, newOwner)
	}
	require.ErrorIs(t, svc.Delete(ctx, "flow"), workflow.ErrMutationPending)
	_, err = repo.Load(ctx, workflow.Private(newOwner), "flow")
	require.NoError(t, err)
	assert.Empty(t, j.entries)
	assert.Len(t, r.requests, count)
}

func (r *revisionRepo) WriteFile(ctx context.Context, s workflow.Scope, name, path string, data []byte) error {
	if err := requireHeld(ctx, s, name); err != nil {
		return err
	}
	return r.WorkflowRepo.WriteFile(ctx, s, name, path, data)
}
func (r *revisionRepo) DeleteFile(ctx context.Context, s workflow.Scope, name, path string) error {
	if err := requireHeld(ctx, s, name); err != nil {
		return err
	}
	return r.WorkflowRepo.DeleteFile(ctx, s, name, path)
}
func (r *revisionRepo) CopyTree(ctx context.Context, src workflow.Scope, sn string, dst workflow.Scope, dn string, overwrite bool) error {
	if err := requireHeld(ctx, src, sn); err != nil {
		return err
	}
	if err := requireHeld(ctx, dst, dn); err != nil {
		return err
	}
	if err := r.fail["copy"]; err != nil {
		return err
	}
	return r.WorkflowRepo.CopyTree(ctx, src, sn, dst, dn, overwrite)
}

// publishing.go must itself refuse an invalid revision or nil WorkflowID before
// CommitPublication, so a permissive store cannot record unprovable proof.
func TestWriteAndRecordRejectsInvalidPreparedProof(t *testing.T) {
	validRev := uuid.Must(uuid.NewV7()).String()
	for name, pub := range map[string]workflow.Publication{
		"non-uuid-revision": {Revision: "not-a-revision", WorkflowID: uuid.New()},
		"v4-revision":       {Revision: uuid.NewString(), WorkflowID: uuid.New()},
		"nil-workflow-id":   {Revision: validRev},
		"empty-publication": {},
	} {
		t.Run(name, func(t *testing.T) {
			var events []string
			store := &revisionPublications{fakePublishers: newFakePublishers(), reg: newFakeRegistry(), events: &events}
			rolledBack := 0
			pub.Publisher = uuid.New()
			err := workflow.WriteAndRecord(context.Background(), store, workflow.PublishRequest{
				PublishedName: "public", Publisher: pub.Publisher,
				WriteTree:           func(context.Context, bool) error { return nil },
				Rollback:            func(context.Context) error { rolledBack++; return nil },
				PreparedPublication: func() (workflow.Publication, error) { return pub, nil },
			})
			require.ErrorContains(t, err, "missing prepared publication proof")
			assert.Equal(t, 1, rolledBack, "the written tree must be restored")
			assert.NotContains(t, events, "publication", "CommitPublication must not be reached")
			assert.Empty(t, store.pub)
		})
	}
}

func TestServiceRevisionPublishFallbackUsesSourceThenDestinationDisplay(t *testing.T) {
	svc, repo, _, j, r, settler, _ := sharedRevisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	source := stampWorkflow()
	source.Nodes[0].Position = &workflow.NodePosition{X: 10, Y: 20}
	require.NoError(t, svc.Create(ctx, "flow", source))
	changed := stampWorkflow()
	changed.Nodes[0].Position = &workflow.NodePosition{X: 80, Y: 90}
	require.NoError(t, svc.Update(ctx, "flow", changed))
	require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
	fresh := assertSavedRequest(t, repo, r, workflow.Shared(), "public", workflow.SaveReasonPublish)
	assert.Equal(t, workflow.NodePosition{X: 10, Y: 20}, fresh.Presentation.Retained.Positions["a"])
	// Displayed destination geometry must win on overwrite, not the source or
	// current input positions. The reader is keyed by the actual saved revision.
	display := &stampDisplayed{revision: fresh.SavedRevision(), ok: true, value: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: fresh.SavedRevision(), FromGeneration: 3, Positions: map[string]workflow.NodePosition{"a": {X: 200, Y: 300}}}}
	st, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: j, Requester: r, Settler: settler, Displayed: display})
	require.NoError(t, err)
	svc.SetRevisionStamper(st)
	require.NoError(t, svc.Publish(ctx, "flow", "public", workflow.VisibilityPublic, ""))
	overwrite := assertSavedRequest(t, repo, r, workflow.Shared(), "public", workflow.SaveReasonPublish)
	assert.Equal(t, display.value, overwrite.Presentation.Retained)
}
func TestServiceRevisionLocalDelete(t *testing.T) {
	svc, repo, j, r, _, _ := revisionFixture(t)
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	require.NoError(t, svc.Create(ctx, "flow", stampWorkflow()))
	require.NoError(t, svc.Delete(ctx, "flow"))
	_, err := repo.Load(ctx, workflow.Private(owner), "flow")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.Len(t, r.requests, 1)
	assert.Empty(t, j.entries)
}
