package workflow_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type commitFixture struct {
	db        *sql.DB
	base      string
	repo      *filesystem.WorkflowRepo
	journal   *filesystem.RevisionJournal
	source    *workflow.CommitSource
	pubs      *adaptsqlite.PublishedWorkflowRepo
	registry  *adaptsqlite.WorkflowIdentityRepo
	requester *layout.Requester
	store     *adaptsqlite.LayoutRepo
	deps      workflow.CommitSourceDeps
}

func newCommitFixture(t *testing.T, registered bool) *commitFixture {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	base := t.TempDir()
	f := &commitFixture{db: db, base: base, repo: filesystem.NewWorkflowRepo(base), journal: filesystem.NewRevisionJournal(base), pubs: adaptsqlite.NewPublishedWorkflowRepo(db), registry: adaptsqlite.NewWorkflowIdentityRepo(db), store: adaptsqlite.NewLayoutRepo(db)}
	f.requester = layout.NewRequester(f.store, 3, nil)
	f.deps = workflow.CommitSourceDeps{Journal: f.journal, Repo: f.repo, Mutations: f.repo, Trees: f.repo, Relocator: f.requester}
	if registered {
		f.deps.Publications = f.pubs
		f.deps.Registry = f.registry
	}
	f.source, err = workflow.NewCommitSource(f.deps)
	require.NoError(t, err)
	f.repo.SetReadSettler(f.source)
	return f
}
func committedInput(t *testing.T, name string, s workflow.Scope) workflow.ArrangementRequest {
	t.Helper()
	wf := &workflow.Workflow{Name: name, ID: uuid.New(), Nodes: []workflow.Node{{ID: "a"}}}
	rev := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC()
	ret := workflow.RetainedLayout{Source: workflow.RetainedNone}
	wf.Presentation = &workflow.Presentation{Revision: rev, SavedAt: now, Scope: workflow.ScopeLabel(s), OwnerID: s.OwnerID, Name: name, Reason: workflow.SaveReasonCreate, Retained: ret}
	data, err := json.Marshal(wf)
	require.NoError(t, err)
	return workflow.ArrangementRequest{Revision: rev, Scope: s, Name: name, WorkflowID: wf.ID, Reason: workflow.SaveReasonCreate, Manifest: data, Retained: ret, RequestedAt: now}
}
func writeCommittedInput(t *testing.T, f *commitFixture, r workflow.ArrangementRequest) {
	t.Helper()
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(r.Manifest, &wf))
	ctx := context.Background()
	exists, err := f.repo.Exists(ctx, r.Scope, r.Name)
	require.NoError(t, err)
	if exists {
		require.NoError(t, f.repo.Update(ctx, r.Scope, r.Name, &wf))
	} else {
		require.NoError(t, f.repo.Create(ctx, r.Scope, r.Name, &wf))
	}
}

type noCatalogRepo struct{ workflow.Repository }

func (noCatalogRepo) List(context.Context, workflow.Scope) ([]workflow.WorkflowSummary, error) {
	panic("recovery scanned catalog")
}

type unavailablePublications struct {
	workflow.RevisionPublicationStore
	err error
}

func (p unavailablePublications) PublicationOf(context.Context, string) (workflow.Publication, bool, error) {
	return workflow.Publication{}, false, p.err
}

func TestCommitSource_PartialErrorsAndCancellation(t *testing.T) {
	ctx := context.Background()
	f := newCommitFixture(t, false)
	good := committedInput(t, "good", workflow.Private(uuid.New()))
	bad := committedInput(t, "bad", good.Scope)
	require.NoError(t, f.journal.Record(ctx, good))
	require.NoError(t, f.journal.MarkCommitted(ctx, good.Revision))
	require.NoError(t, f.journal.Record(ctx, bad))
	writeCommittedInput(t, f, bad)
	// A malformed visible manifest is undecided, not proof of abandonment.
	require.NoError(t, os.WriteFile(filepath.Join(f.base, "users", bad.Scope.OwnerID.String(), bad.Name, "workflow.json"), []byte("partial"), 0600))
	f.deps.Repo = noCatalogRepo{f.repo}
	source, err := workflow.NewCommitSource(f.deps)
	require.NoError(t, err)
	f.repo.SetReadSettler(source)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = source.CommittedRequests(canceled)
	require.ErrorIs(t, err, context.Canceled)
	got, err := source.CommittedRequests(ctx)
	require.Error(t, err)
	require.Equal(t, []workflow.ArrangementRequest{good}, got)
	entries, err := f.journal.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, workflow.JournalPrepared, entries[1].State)
	got[0].Manifest[0] = 'x'
	fresh, err := source.CommittedRequests(ctx)
	require.Error(t, err)
	require.Equal(t, good.Manifest, fresh[0].Manifest)
}

func TestCommitSource_PublicationOutageIsUndecided(t *testing.T) {
	ctx := context.Background()
	f := newCommitFixture(t, true)
	good := committedInput(t, "history", workflow.Shared())
	req := committedInput(t, "pending", workflow.Shared())
	require.NoError(t, f.journal.Record(ctx, good))
	require.NoError(t, f.journal.MarkCommitted(ctx, good.Revision))
	require.NoError(t, f.journal.Record(ctx, req))
	writeCommittedInput(t, f, req)
	boom := errors.New("publication unavailable")
	f.deps.Publications = unavailablePublications{f.pubs, boom}
	f.deps.Repo = noCatalogRepo{f.repo}
	source, err := workflow.NewCommitSource(f.deps)
	require.NoError(t, err)
	got, err := source.CommittedRequests(ctx)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []workflow.ArrangementRequest{good}, got)
	entries, err := f.journal.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, workflow.JournalPrepared, entries[1].State)
	f.repo.SetReadSettler(source)
	_, err = f.repo.Load(ctx, req.Scope, req.Name)
	require.ErrorIs(t, err, boom)
}

func TestCommitSource_Constructor(t *testing.T) {
	f := newCommitFixture(t, false)
	cases := []workflow.CommitSourceDeps{f.deps, f.deps, f.deps, f.deps, f.deps}
	cases[0].Journal = nil
	cases[1].Repo = nil
	cases[2].Mutations = nil
	cases[3].Trees = nil
	cases[4].Relocator = nil
	for i, d := range cases {
		_, err := workflow.NewCommitSource(d)
		require.ErrorIs(t, err, workflow.ErrCommitSourceMisconfigured)
		require.ErrorContains(t, err, []string{"journal", "repository", "mutation coordinator", "tree committer", "relocator"}[i])
	}
	d := f.deps
	d.Registry = f.registry
	_, err := workflow.NewCommitSource(d)
	require.ErrorIs(t, err, workflow.ErrCommitSourceMisconfigured)
	require.ErrorContains(t, err, "publication/registry pair")
	d = f.deps
	d.Publications = f.pubs
	_, err = workflow.NewCommitSource(d)
	require.ErrorIs(t, err, workflow.ErrCommitSourceMisconfigured)
	require.ErrorContains(t, err, "publication/registry pair")
}
func TestCommitSource_PrivateAndHistory(t *testing.T) {
	ctx := context.Background()
	f := newCommitFixture(t, false)
	s := workflow.Private(uuid.New())
	first := committedInput(t, "demo", s)
	require.NoError(t, f.journal.Record(ctx, first))
	writeCommittedInput(t, f, first)
	got, err := f.source.CommittedRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.ArrangementRequest{first}, got)
	require.NoError(t, f.repo.Delete(ctx, s, "demo"))
	got, err = f.source.CommittedRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.ArrangementRequest{first}, got)
	abandoned := committedInput(t, "missing", s)
	require.NoError(t, f.journal.Record(ctx, abandoned))
	got, err = f.source.CommittedRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.ArrangementRequest{first}, got)
	for _, r := range got {
		require.NoError(t, f.requester.RequestArrangement(ctx, r))
	}
	row, err := f.store.Get(ctx, first.Revision)
	require.NoError(t, err)
	require.Equal(t, first.Manifest, row.Manifest)
	require.Equal(t, 3, row.AttemptLimit)
}
func TestCommitSource_SettleBeforeOverwritePreservesMissedReceipt(t *testing.T) {
	ctx := context.Background()
	f := newCommitFixture(t, false)
	scope := workflow.Private(uuid.New())
	st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.source})
	require.NoError(t, err)
	var first, second workflow.ArrangementRequest
	require.NoError(t, f.repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: "demo"}}, func(owned context.Context) error {
		wf := &workflow.Workflow{Name: "demo", Nodes: []workflow.Node{{ID: "a"}}}
		var err error
		first, err = st.Stamp(owned, scope, "demo", wf, nil, workflow.SaveReasonCreate)
		if err != nil {
			return err
		}
		if err = f.repo.Create(owned, scope, "demo", wf); err != nil {
			return err
		}
		next := *wf
		next.Description = "second save"
		second, err = st.Stamp(owned, scope, "demo", &next, wf, workflow.SaveReasonUpdate)
		if err != nil {
			return err
		}
		return f.repo.Update(owned, scope, "demo", &next)
	}))
	got, err := f.source.CommittedRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, []workflow.ArrangementRequest{first, second}, got)
	for _, req := range got {
		require.NoError(t, f.requester.RequestArrangement(ctx, req))
		row, err := f.store.Get(ctx, req.Revision)
		require.NoError(t, err)
		require.Equal(t, req.Manifest, row.Manifest)
		require.Equal(t, uuid.Nil, row.Destination.WorkflowID)
	}
}

func TestCommitSource_PublicationExactness(t *testing.T) {
	for _, mode := range []string{"old proof", "wrong identity", "exact proof"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newCommitFixture(t, true)
			req := committedInput(t, "demo", workflow.Shared())
			old := committedInput(t, "demo", workflow.Shared())
			old.WorkflowID = req.WorkflowID
			// Preserve actual old tree bytes, including its supporting file.
			writeCommittedInput(t, f, old)
			require.NoError(t, f.repo.WriteFile(ctx, old.Scope, old.Name, "support", []byte("old")))
			require.NoError(t, f.journal.Record(ctx, req))
			tree, err := f.repo.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: req.Scope, Name: req.Name}, req.Revision)
			require.NoError(t, err)
			writeCommittedInput(t, f, req)
			proof := workflow.Publication{Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic, Revision: req.Revision, WorkflowID: req.WorkflowID}
			if mode == "old proof" {
				proof.Revision = old.Revision
			}
			if mode == "wrong identity" {
				proof.WorkflowID = uuid.New()
			}
			require.NoError(t, f.pubs.CommitPublication(ctx, req.Name, proof))
			got, err := f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			if mode == "exact proof" {
				require.Equal(t, []workflow.ArrangementRequest{req}, got)
			} else {
				require.Empty(t, got)
				saved, err := f.repo.Load(ctx, req.Scope, req.Name)
				require.NoError(t, err)
				require.Equal(t, old.Revision, saved.SavedRevision())
				files, err := f.repo.ListFiles(ctx, req.Scope, req.Name)
				require.NoError(t, err)
				require.Equal(t, []string{"support"}, files)
			}
			pending, err := f.repo.PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, pending, "tree %s", tree.ID)
		})
	}
}

// The operation's own relocation intent must not refuse its own Stamp, but a
// foreign operation still cannot settle a destination the intent covers.
func TestCommitSource_OwnRelocationIntentIsExempt(t *testing.T) {
	f := newCommitFixture(t, true)
	ctx := ctxWith(uuid.MustParse("00000000-0000-0000-0000-000000000001"), true)
	st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.source})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(f.repo, f.pubs)
	svc.SetRegistry(f.registry)
	svc.SetRevisionStamper(st)
	svc.SetMutationCoordinator(f.repo)
	require.NoError(t, svc.Create(ctx, "original", stampWorkflow()))
	before, err := svc.Load(ctx, "original")
	require.NoError(t, err)
	require.NoError(t, svc.Rename(ctx, "original", "renamed"))
	_, err = svc.Load(ctx, "original")
	require.ErrorIs(t, err, workflow.ErrNotFound)
	after, err := svc.Load(ctx, "renamed")
	require.NoError(t, err)
	require.Equal(t, before.ID, after.ID)
	require.NotEqual(t, before.SavedRevision(), after.SavedRevision())
	_, err = f.store.Get(ctx, before.SavedRevision())
	require.NoError(t, err, "the prior revision row survives a rename")
	intents, err := f.journal.RelocationIntents(ctx)
	require.NoError(t, err)
	require.Empty(t, intents, "a completed rename leaves no relocation intent")

	// A foreign intent over the destination still fails closed.
	owner := workflow.Private(uuid.MustParse("00000000-0000-0000-0000-000000000001"))
	_, err = f.journal.BeginRelocation(ctx, owner, "renamed", owner, "elsewhere", after.ID)
	require.NoError(t, err)
	require.ErrorIs(t, f.source.Settle(ctx, owner, "renamed"), workflow.ErrMutationPending)
}

func TestCommitSource_RelocationAuthority(t *testing.T) {
	for _, toWins := range []bool{false, true} {
		t.Run(map[bool]string{false: "from", true: "to"}[toWins], func(t *testing.T) {
			ctx := context.Background()
			f := newCommitFixture(t, true)
			from := workflow.Private(uuid.New())
			to := workflow.Private(uuid.New())
			req := committedInput(t, "source", from)
			require.NoError(t, f.journal.Record(ctx, req))
			writeCommittedInput(t, f, req)
			require.NoError(t, f.journal.MarkCommitted(ctx, req.Revision))
			require.NoError(t, f.registry.Register(ctx, workflow.RegistryEntry{ID: req.WorkflowID, Name: req.Name, Scope: from.Kind, OwnerID: from.OwnerID}))
			intent, err := f.journal.BeginRelocation(ctx, from, req.Name, to, "target", req.WorkflowID)
			require.NoError(t, err)
			require.NoError(t, f.repo.CopyTree(ctx, from, req.Name, to, "target", false))
			_, err = f.repo.Load(ctx, to, "target")
			require.ErrorIs(t, err, workflow.ErrMutationPending)
			if toWins {
				require.NoError(t, f.registry.Rename(ctx, req.WorkflowID, "target"))
				require.NoError(t, f.registry.SetOwner(ctx, req.WorkflowID, to.OwnerID))
			}
			got, err := f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			require.Len(t, got, 1)
			if toWins {
				require.Equal(t, "target", got[0].Name)
				require.Equal(t, to, got[0].Scope)
				exists, err := f.repo.Exists(ctx, from, req.Name)
				require.NoError(t, err)
				require.False(t, exists)
			} else {
				require.Equal(t, req, got[0])
				exists, err := f.repo.Exists(ctx, to, "target")
				require.NoError(t, err)
				require.False(t, exists)
			}
			require.Equal(t, req.Manifest, got[0].Manifest)
			intents, err := f.journal.RelocationIntents(ctx)
			require.NoError(t, err)
			require.Empty(t, intents, "intent %s", intent.ID)
		})
	}
}

// Trusting registry location alone must not delete the last readable copy when
// the authoritative endpoint is unavailable or belongs to a different workflow.
func TestCommitSource_RelocationUndecidedPreservesEvidence(t *testing.T) {
	for _, toWins := range []bool{false, true} {
		for _, fault := range []string{"missing", "read-error", "malformed", "wrong-id", "third-destination", "missing-registry", "deadline"} {
			t.Run(map[bool]string{false: "from/", true: "to/"}[toWins]+fault, func(t *testing.T) {
				ctx := context.Background()
				f := newCommitFixture(t, true)
				from, to := workflow.Private(uuid.New()), workflow.Private(uuid.New())
				req := committedInput(t, "source", from)
				writeCommittedInput(t, f, req)
				require.NoError(t, f.journal.Record(ctx, req))
				require.NoError(t, f.journal.MarkCommitted(ctx, req.Revision))
				require.NoError(t, f.requester.RequestArrangement(ctx, req))
				row, err := f.store.Get(ctx, req.Revision)
				require.NoError(t, err)
				entry := workflow.RegistryEntry{ID: req.WorkflowID, Name: req.Name, Scope: from.Kind, OwnerID: from.OwnerID}
				require.NoError(t, f.registry.Register(ctx, entry))
				intent, err := f.journal.BeginRelocation(ctx, from, "source", to, "target", req.WorkflowID)
				require.NoError(t, err)
				require.NoError(t, f.repo.CopyTree(ctx, from, "source", to, "target", false))
				sourceDir := filepath.Join(f.base, "users", from.OwnerID.String(), "source")
				targetDir := filepath.Join(f.base, "users", to.OwnerID.String(), "target")
				winnerDir, loserDir := sourceDir, targetDir
				if toWins {
					require.NoError(t, f.registry.Rename(ctx, req.WorkflowID, "target"))
					require.NoError(t, f.registry.SetOwner(ctx, req.WorkflowID, to.OwnerID))
					winnerDir, loserDir = targetDir, sourceDir
				}
				winnerPath := filepath.Join(winnerDir, "workflow.json")
				winnerBytes, err := os.ReadFile(winnerPath)
				require.NoError(t, err)
				loserPath := filepath.Join(loserDir, "workflow.json")
				loserBytes, err := os.ReadFile(loserPath)
				require.NoError(t, err)
				opCtx := ctx
				switch fault {
				case "missing":
					require.NoError(t, os.Rename(winnerDir, winnerDir+".unavailable"))
				case "read-error":
					require.NoError(t, os.Remove(winnerPath))
					require.NoError(t, os.Mkdir(winnerPath, 0700))
				case "malformed":
					require.NoError(t, os.WriteFile(winnerPath, []byte("partial"), 0600))
				case "wrong-id":
					var other workflow.Workflow
					require.NoError(t, json.Unmarshal(winnerBytes, &other))
					other.ID = uuid.New()
					data, err := json.Marshal(other)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(winnerPath, data, 0600))
				case "third-destination":
					require.NoError(t, f.registry.Rename(ctx, req.WorkflowID, "third"))
				case "missing-registry":
					require.NoError(t, f.registry.Delete(ctx, req.WorkflowID))
				case "deadline":
					var cancel context.CancelFunc
					opCtx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				_, recoveryErr := f.source.CommittedRequests(opCtx)
				// Assert bytes first: a success/error assertion alone can hide data loss.
				afterBytes, err := os.ReadFile(loserPath)
				require.NoError(t, err, "undecided recovery must preserve the surviving endpoint")
				require.Equal(t, loserBytes, afterBytes)
				require.Error(t, recoveryErr)
				intents, err := f.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Equal(t, []workflow.RelocationIntent{intent}, intents)
				entries, err := f.journal.Entries(ctx)
				require.NoError(t, err)
				require.Equal(t, []workflow.JournalEntry{{Request: req, State: workflow.JournalCommitted}}, entries)
				afterRow, err := f.store.Get(ctx, req.Revision)
				require.NoError(t, err)
				require.Equal(t, row, afterRow, "an undecided move must not relocate layout history")
			})
		}
	}
}

func TestServiceRevisionDeleteRecreateKeepsHistory(t *testing.T) {
	for _, unpublish := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "unpublish"}[unpublish], func(t *testing.T) {
			f := newCommitFixture(t, true)
			owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			ctx := ctxWith(owner, true)
			st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.source})
			require.NoError(t, err)
			svc := workflow.NewWorkflowService(f.repo, f.pubs)
			svc.SetRegistry(f.registry)
			svc.SetRevisionStamper(st)
			svc.SetMutationCoordinator(f.repo)
			svc.SetActiveInstanceCounter(&fakeActiveCounter{})
			create := func() *workflow.Workflow {
				t.Helper()
				if unpublish {
					require.NoError(t, svc.Publish(ctx, "source", "reused", workflow.VisibilityPublic, ""))
				} else {
					require.NoError(t, svc.Create(ctx, "reused", stampWorkflow()))
				}
				saved, err := svc.Load(ctx, "reused")
				require.NoError(t, err)
				return saved
			}
			if unpublish {
				require.NoError(t, svc.Create(ctx, "source", stampWorkflow()))
			}
			old := create()
			oldRow, err := f.store.Get(ctx, old.SavedRevision())
			require.NoError(t, err)
			if unpublish {
				require.NoError(t, svc.Unpublish(ctx, "reused"))
			} else {
				require.NoError(t, svc.Delete(ctx, "reused"))
			}
			fresh := create()
			require.NotEqual(t, old.ID, fresh.ID)
			require.NotEqual(t, old.SavedRevision(), fresh.SavedRevision())
			_, err = f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			current, err := svc.Load(ctx, "reused")
			require.NoError(t, err)
			require.Equal(t, fresh, current)
			row, err := f.store.Get(ctx, old.SavedRevision())
			require.NoError(t, err)
			require.Equal(t, oldRow, row, "old history survives unchanged")
			row, err = f.store.Get(ctx, fresh.SavedRevision())
			require.NoError(t, err)
			require.Equal(t, fresh.ID, row.Destination.WorkflowID)
			var captured workflow.Workflow
			require.NoError(t, json.Unmarshal(row.Manifest, &captured))
			require.Equal(t, *fresh, captured, "current input belongs to the recreated workflow")
		})
	}
}

// errRenameCrash stands in for process death at an identity-commit boundary:
// it unwinds the rename without running rollbackMove or committed.
var errRenameCrash = errors.New("simulated crash during rename")

type crashingRenameRegistry struct {
	*adaptsqlite.WorkflowIdentityRepo
	afterCommit bool
}

func (r crashingRenameRegistry) Rename(ctx context.Context, id uuid.UUID, name string) error {
	if r.afterCommit {
		if err := r.WorkflowIdentityRepo.Rename(ctx, id, name); err != nil {
			return err
		}
	}
	panic(errRenameCrash)
}

type crashingRenamePublications struct {
	*adaptsqlite.PublishedWorkflowRepo
	armed       *bool
	afterCommit bool
}

func (p crashingRenamePublications) RenamePublication(ctx context.Context, id uuid.UUID, from, to string, pub workflow.Publication) error {
	if !*p.armed {
		return p.PublishedWorkflowRepo.RenamePublication(ctx, id, from, to, pub)
	}
	if p.afterCommit {
		if err := p.PublishedWorkflowRepo.RenamePublication(ctx, id, from, to, pub); err != nil {
			return err
		}
	}
	panic(errRenameCrash)
}

// A rename killed after its own Stamp is decided at the next boot by registry
// authority alone: before the identity commit the source survives and the
// rename's prepared revision is never arranged; after it the destination keeps
// that exact revision, which is committed and requested once. The stale
// endpoint is gone and no reader is left facing a half-moved tree.
func TestCommitSource_CrashedRenameRecoversByRegistryAuthority(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, afterCommit := range []bool{false, true} {
			name := map[bool]string{false: "private/", true: "shared/"}[shared] + map[bool]string{false: "before-identity", true: "after-identity"}[afterCommit]
			t.Run(name, func(t *testing.T) {
				f := newCommitFixture(t, true)
				// The migration-seeded local admin satisfies the publisher foreign key.
				owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
				ctx := ctxWith(owner, true)
				st, err := workflow.NewRevisionStamper(f.repo, workflow.StamperDeps{Journal: f.journal, Requester: f.requester, Settler: f.source})
				require.NoError(t, err)
				armed := false
				pubs := crashingRenamePublications{PublishedWorkflowRepo: f.pubs, armed: &armed, afterCommit: afterCommit}
				svc := workflow.NewWorkflowService(f.repo, pubs)
				svc.SetRegistry(f.registry)
				svc.SetRevisionStamper(st)
				svc.SetMutationCoordinator(f.repo)
				require.NoError(t, svc.Create(ctx, "original", stampWorkflow()))
				from, source := workflow.Private(owner), "original"
				if shared {
					require.NoError(t, svc.Publish(ctx, "original", "public", workflow.VisibilityPublic, ""))
					from, source = workflow.Shared(), "public"
					armed = true
				} else {
					svc.SetRegistry(crashingRenameRegistry{WorkflowIdentityRepo: f.registry, afterCommit: afterCommit})
				}
				before, err := f.repo.Load(ctx, from, source)
				require.NoError(t, err)
				func() {
					defer func() { require.Equal(t, errRenameCrash, recover()) }()
					_ = svc.Rename(ctx, source, "renamed")
				}()

				// The crash left the intent, both trees and the rename's prepared input.
				intents, err := f.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Len(t, intents, 1)
				entries, err := f.journal.Entries(ctx)
				require.NoError(t, err)
				var renamed workflow.ArrangementRequest
				for _, e := range entries {
					if e.Request.Name == "renamed" {
						require.Equal(t, workflow.JournalPrepared, e.State)
						renamed = e.Request
					}
				}
				require.NotEmpty(t, renamed.Revision, "the rename's own Stamp must have prepared its input")
				require.Equal(t, workflow.SaveReasonRename, renamed.Reason)
				require.Equal(t, before.ID, renamed.WorkflowID)
				for _, n := range []string{source, "renamed"} {
					_, err = f.repo.Load(ctx, from, n)
					require.ErrorIs(t, err, workflow.ErrMutationPending, "%s is covered by the unresolved intent", n)
				}

				got, err := f.source.CommittedRequests(context.Background())
				require.NoError(t, err)
				intents, err = f.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Empty(t, intents)
				winner, loser, winnerRevision := source, "renamed", before.SavedRevision()
				if afterCommit {
					winner, loser, winnerRevision = "renamed", source, renamed.Revision
				}
				wf, err := f.repo.Load(ctx, from, winner)
				require.NoError(t, err)
				require.Equal(t, before.ID, wf.ID)
				require.Equal(t, winnerRevision, wf.SavedRevision())
				_, err = f.repo.Load(ctx, from, loser)
				require.ErrorIs(t, err, workflow.ErrNotFound)
				var arranged []string
				for _, r := range got {
					arranged = append(arranged, r.Revision)
				}
				if afterCommit {
					require.Contains(t, arranged, renamed.Revision)
					for _, r := range got {
						if r.Revision == renamed.Revision {
							require.Equal(t, renamed.Manifest, r.Manifest)
							require.Equal(t, "renamed", r.Name)
						}
					}
				} else {
					require.NotContains(t, arranged, renamed.Revision, "an uncommitted rename is never arranged")
				}
				if shared {
					pub, ok, err := f.pubs.PublicationOf(ctx, winner)
					require.NoError(t, err)
					require.True(t, ok)
					require.Equal(t, winnerRevision, pub.Revision)
					_, ok, err = f.pubs.PublicationOf(ctx, loser)
					require.NoError(t, err)
					require.False(t, ok)
					pending, err := f.repo.PendingTreeCommits(ctx)
					require.NoError(t, err)
					require.Empty(t, pending)
				}
				resolved, err := f.registry.ResolveName(ctx, before.ID)
				require.NoError(t, err)
				require.Equal(t, winner, resolved)
			})
		}
	}
}
