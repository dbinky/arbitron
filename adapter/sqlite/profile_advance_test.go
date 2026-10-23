package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func followingProfile(alias string) provider.Profile {
	p := sampleProfile(alias)
	p.Model = "vendor/version-x"
	p.SelectionMode = provider.SelectionFollowing
	p.FollowTarget = &provider.FollowTarget{Provider: "vendor", Tier: "premium"}
	return p
}

func journalCount(t *testing.T, db testDB, alias string) int {
	t.Helper()
	var n int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal WHERE alias = ?`, alias).Scan(&n))
	return n
}

func advanceReq(id string, expected provider.Profile, model string) provider.AdvanceRequest {
	return provider.AdvanceRequest{
		ID: id, RefreshID: "refresh-1", Expected: expected, Model: model,
		CanonicalID: "vendor/" + model, PathRevision: "path-1",
		ExecutionRevision: provider.ExecutionRevision{ID: "path-1", PolicyRevision: 1},
		Evidence:          []provider.PathEvidenceRef{{PathKey: "path", ConfigRevision: "config-1"}},
		At:                time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestAdvanceCASAndJournal(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	cur, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	cache := adapterprovider.NewProfileCache(repo)
	require.NoError(t, cache.Load(ctx))

	adv, err := repo.CommitAdvance(ctx, advanceReq("adv-1", cur, "vendor/version-y"))
	require.NoError(t, err)
	assert.Equal(t, "worker", adv.Alias)
	assert.Equal(t, "vendor/version-x", adv.FromModel)
	assert.Equal(t, "vendor/version-y", adv.ToModel)
	assert.Equal(t, cur.Revision+1, adv.Revision)

	got, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, "vendor/version-y", got.Model)
	assert.Equal(t, adv.Revision, got.Revision)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	before, err := cache.SnapshotProfiles([]string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, cur, before["worker"], "durable commit alone must not change the live snapshot")
	require.NoError(t, cache.PublishCommitted(ctx, []provider.Advancement{adv}))
	after, err := cache.SnapshotProfiles([]string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, got, after["worker"], "publication must expose the entire authoritative row")
}

func TestCommitAdvanceConflictChangesNothing(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*provider.Profile){
		"model":     func(p *provider.Profile) { p.Model = "vendor/other" },
		"kind":      func(p *provider.Profile) { p.Kind = provider.ProfileKindChat },
		"provider":  func(p *provider.Profile) { p.FollowTarget = &provider.FollowTarget{Provider: "other", Tier: "premium"} },
		"tools":     func(p *provider.Profile) { p.AllowedTools = []string{"Read"} },
		"backend":   func(p *provider.Profile) { p.Backend = "opencode" },
		"connref":   func(p *provider.Profile) { p.ConnectionRef = "other" },
		"tier":      func(p *provider.Profile) { p.FollowTarget = &provider.FollowTarget{Provider: "vendor", Tier: "fast"} },
		"pinned":    func(p *provider.Profile) { p.SelectionMode = provider.SelectionPinned; p.FollowTarget = nil },
		"effort":    func(p *provider.Profile) { p.Effort = provider.EffortLow },
		"ctxwindow": func(p *provider.Profile) { p.ContextWindow = provider.Context200K },
		"revision":  func(p *provider.Profile) { p.Revision++ },
	} {
		t.Run(name, func(t *testing.T) {
			repo, db := newProfileRepo(t)
			require.NoError(t, repo.Create(ctx, followingProfile("worker")))
			cur, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			stale := cur
			mutate(&stale)

			_, err = repo.CommitAdvance(ctx, advanceReq("adv-1", stale, "vendor/version-y"))
			assert.ErrorIs(t, err, provider.ErrAdvanceConflict)

			got, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, cur, got)
			assert.Zero(t, journalCount(t, db, "worker"))
		})
	}
}

func TestCommitAdvanceABAEditIsConflict(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	seen, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	// Admin edits the model away and back: same visible fields, new revision.
	edit := seen
	edit.Model = "vendor/other"
	require.NoError(t, repo.Update(ctx, edit))
	edit.Model = seen.Model
	require.NoError(t, repo.Update(ctx, edit))

	_, err = repo.CommitAdvance(ctx, advanceReq("adv-1", seen, "vendor/version-y"))
	assert.ErrorIs(t, err, provider.ErrAdvanceConflict)
	assert.Zero(t, journalCount(t, db, "worker"))
}

func TestCommitAdvanceMissingProfileIsConflict(t *testing.T) {
	repo, _ := newProfileRepo(t)
	_, err := repo.CommitAdvance(context.Background(), advanceReq("adv-1", followingProfile("ghost"), "vendor/version-y"))
	assert.ErrorIs(t, err, provider.ErrAdvanceConflict)
}

func TestCommitAdvanceReplayReturnsOriginalWithoutNewRevision(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	cur, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	req := advanceReq("adv-1", cur, "vendor/version-y")
	first, err := repo.CommitAdvance(ctx, req)
	require.NoError(t, err)

	// The selector's stable ID can be retried in a later observation cycle.
	req.RefreshID = "refresh-2"
	req.At = req.At.Add(time.Hour)
	again, err := repo.CommitAdvance(ctx, req) // retry of the same request
	require.NoError(t, err)
	assert.Equal(t, first, again)
	got, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, first.Revision, got.Revision)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
}

// An ID collision must never return another request's durable result.
func TestCommitAdvanceReplayRejectsDifferentIntent(t *testing.T) {
	for name, mutate := range map[string]func(*provider.AdvanceRequest){
		"alias":             func(r *provider.AdvanceRequest) { r.Expected.Alias = "other" },
		"from model":        func(r *provider.AdvanceRequest) { r.Expected.Model = "vendor/other" },
		"to model":          func(r *provider.AdvanceRequest) { r.Model = "vendor/version-z" },
		"canonical model":   func(r *provider.AdvanceRequest) { r.CanonicalID = "vendor/other" },
		"expected revision": func(r *provider.AdvanceRequest) { r.Expected.Revision++ },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo, db := newProfileRepo(t)
			require.NoError(t, repo.Create(ctx, followingProfile("worker")))
			require.NoError(t, repo.Create(ctx, followingProfile("other")))
			cur, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			req := advanceReq("adv-1", cur, "vendor/version-y")
			original, err := repo.CommitAdvance(ctx, req)
			require.NoError(t, err)
			mutate(&req)
			got, err := repo.CommitAdvance(ctx, req)
			require.ErrorIs(t, err, provider.ErrAdvanceConflict)
			assert.Empty(t, got)
			worker, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, original.ToModel, worker.Model)
			assert.Equal(t, original.Revision, worker.Revision)
			other, err := repo.Get(ctx, "other")
			require.NoError(t, err)
			assert.Equal(t, "vendor/version-x", other.Model)
			assert.Equal(t, 1, journalCount(t, db, "worker"))
			assert.Zero(t, journalCount(t, db, "other"))
		})
	}
}

type publicationFaultStore struct {
	provider.ProfileStore
	err error
}

func (s *publicationFaultStore) List(ctx context.Context) ([]provider.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ProfileStore.List(ctx)
}

type replayValidationRoutes struct {
	provider.EligibleCandidateSource // Discovery methods must never be called.
	want                             provider.Profile
}

func (s replayValidationRoutes) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if !reflect.DeepEqual(p, s.want) {
		return nil, errors.New("lost replay test replacement configuration")
	}
	return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
}

// The model update itself changes the inventory revision. A retry must recover
// its original journal result before checking freshness for a NEW advancement.
func TestAdvanceModelReplayRepairsPublicationAfterRevisionChange(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	p := followingProfile("worker")
	p.Kind = provider.ProfileKindChat
	p.Model = "claude-sonnet-4-5"
	p.ContextWindow = provider.ContextDefault
	require.NoError(t, repo.Create(ctx, p))
	cur, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	store := &publicationFaultStore{ProfileStore: repo}
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(ctx))
	gate := provider.NewExecutionGate()
	gate.SetCurrentRevision(func(ctx context.Context) (string, error) {
		p, err := repo.Get(ctx, "worker")
		return fmt.Sprint(p.Revision), err
	})
	svc := provider.NewProfileService(repo, provider.CapabilityRules{}, cache)
	svc.SetAdvancementDependencies(repo, cache, gate)
	journal := NewModelCatalogRepo(db.DB)
	svc.SetPublicationJournal(journal)
	req := advanceReq("adv-1", cur, "claude-sonnet-4-6")
	next := cur.Clone()
	next.Model = req.Model
	svc.SetModelSelection(modelidentity.NewVersionResolver(), replayValidationRoutes{want: next})
	req.PathRevision = fmt.Sprint(cur.Revision)
	req.ExecutionRevision.ID = req.PathRevision
	store.err = errors.New("injected cache reload failure")
	original, err := svc.AdvanceModel(ctx, req)
	require.ErrorIs(t, err, provider.ErrPublicationPending)
	require.Equal(t, "claude-sonnet-4-6", original.ToModel)
	cached, err := cache.SnapshotProfiles([]string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", cached["worker"].Model)
	pending, err := journal.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, []provider.Advancement{original}, pending)

	// Repair must not depend on a currently available route/version source:
	// only publication of the already committed journal result is retried.
	svc.SetModelSelection(nil, nil)
	// A failed repair must keep returning the committed result and leave it
	// pending; freshness checks must not turn it into an uncommitted failure.
	again, err := svc.AdvanceModel(ctx, req)
	require.ErrorIs(t, err, provider.ErrPublicationPending)
	assert.Equal(t, original, again)
	assert.Equal(t, 1, journalCount(t, db, "worker"))

	store.err = nil
	again, err = svc.AdvanceModel(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, original, again)
	cached, err = cache.SnapshotProfiles([]string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cached["worker"].Model)
	assert.Equal(t, cur.Revision+1, cached["worker"].Revision)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	pending, err = journal.Unpublished(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)

	// Replaying an acknowledged request still returns the same result, while a
	// reused ID for another target remains a conflict and never changes the cache.
	again, err = svc.AdvanceModel(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, original, again)
	req.Model = "vendor/version-z"
	again, err = svc.AdvanceModel(ctx, req)
	require.ErrorIs(t, err, provider.ErrAdvanceConflict)
	assert.Empty(t, again)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	cached, err = cache.SnapshotProfiles([]string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cached["worker"].Model)

	// A subsequent admin edit must stay live when an old result is replayed.
	later, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	later.Model = "vendor/admin-model"
	require.NoError(t, repo.Update(ctx, later))
	req.Model = "claude-sonnet-4-6"
	again, err = svc.AdvanceModel(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, original, again)
	cached, err = cache.SnapshotProfiles([]string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, "vendor/admin-model", cached["worker"].Model)
	assert.Equal(t, cur.Revision+2, cached["worker"].Revision)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
}

func TestCommitAdvanceRejectsMalformedRequests(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	cur, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	for name, req := range map[string]provider.AdvanceRequest{
		"empty id":    advanceReq("", cur, "vendor/version-y"),
		"empty model": advanceReq("adv-1", cur, ""),
		"same model":  advanceReq("adv-1", cur, cur.Model),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repo.CommitAdvance(ctx, req)
			assert.Error(t, err)
			assert.Zero(t, journalCount(t, db, "worker"))
		})
	}
	got, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, cur, got)
}

func TestCommitAdvanceJournalFaultRollsBackModel(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	cur, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	var updatedAt string
	require.NoError(t, db.DB.QueryRow(`SELECT updated_at FROM profiles WHERE alias = 'worker'`).Scan(&updatedAt))
	// Leave the table readable for the initial replay lookup. Fire only after
	// the INSERT can see the new model, revision and timestamp in its transaction.
	// ABORT undoes this statement, so the earlier UPDATE needs the caller's rollback.
	_, err = db.DB.Exec(`CREATE TRIGGER fail_advance_journal
BEFORE INSERT ON profile_advancement_journal
WHEN EXISTS (SELECT 1 FROM profiles WHERE alias = NEW.alias
  AND model = NEW.to_model AND revision = NEW.revision AND updated_at = NEW.at)
BEGIN SELECT RAISE(ABORT, 'injected journal insert fault'); END`)
	require.NoError(t, err)

	req := advanceReq("adv-1", cur, "vendor/version-y")
	adv, err := repo.CommitAdvance(ctx, req)
	require.ErrorContains(t, err, "injected journal insert fault")
	assert.Equal(t, provider.Advancement{}, adv, "a rolled-back change must not be returned for publication")
	got, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, cur, got, "journal failure must roll back the model update")
	var afterUpdatedAt string
	require.NoError(t, db.DB.QueryRow(`SELECT updated_at FROM profiles WHERE alias = 'worker'`).Scan(&afterUpdatedAt))
	assert.Equal(t, updatedAt, afterUpdatedAt, "journal failure must roll back the timestamp too")
	assert.Zero(t, journalCount(t, db, "worker"))
	journal := NewModelCatalogRepo(db.DB)
	pending, err := journal.Unpublished(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "recovery must not publish a failed advancement")

	// Repair the fault and retry the identical request: no revision or request ID
	// was consumed by the failed transaction, and exactly one change is publishable.
	_, err = db.DB.Exec(`DROP TRIGGER fail_advance_journal`)
	require.NoError(t, err)
	adv, err = repo.CommitAdvance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "adv-1", adv.ID)
	assert.Equal(t, cur.Model, adv.FromModel)
	assert.Equal(t, "vendor/version-y", adv.ToModel)
	assert.Equal(t, cur.Revision+1, adv.Revision)
	got, err = repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, adv.ToModel, got.Model)
	assert.Equal(t, adv.Revision, got.Revision)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	pending, err = journal.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, []provider.Advancement{adv}, pending)
}
