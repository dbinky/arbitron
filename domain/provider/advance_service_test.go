package provider_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

type fakeAdvanceStore struct {
	err   error
	calls []provider.AdvanceRequest
}

func (f *fakeAdvanceStore) ReplayAdvance(context.Context, provider.AdvanceRequest) (provider.Advancement, bool, error) {
	return provider.Advancement{}, false, nil
}

func (f *fakeAdvanceStore) CommitAdvance(_ context.Context, r provider.AdvanceRequest) (provider.Advancement, error) {
	f.calls = append(f.calls, r)
	if f.err != nil {
		return provider.Advancement{}, f.err
	}
	return provider.Advancement{ID: r.ID, Alias: r.Expected.Alias, FromModel: r.Expected.Model, ToModel: r.Model, Revision: r.Expected.Revision + 1}, nil
}

type fakePublisher struct {
	err  error
	seen [][]provider.Advancement
}

func (f *fakePublisher) PublishCommitted(_ context.Context, a []provider.Advancement) error {
	f.seen = append(f.seen, a)
	return f.err
}

func advanceFixture(t *testing.T, current string) (*provider.ProfileService, *fakeAdvanceStore, *fakePublisher, provider.AdvanceRequest) {
	t.Helper()
	gate := provider.NewExecutionGate()
	gate.SetCurrentRevision(func(context.Context) (string, error) { return current, nil })
	store, pub := &fakeAdvanceStore{}, &fakePublisher{}
	svc := provider.NewProfileService(nil, rules(), nil)
	svc.SetAdvancementDependencies(store, pub, gate)
	svc.SetPublicationJournal(&checkingPublicationJournal{store: store, publisher: pub})
	exp := provider.Profile{Alias: "sonnet-latest", SelectionMode: provider.SelectionFollowing, FollowTarget: sonnetTarget, Model: "claude-sonnet-4-5", Revision: 3, Kind: provider.ProfileKindAgent, Backend: "claude-code"}
	req := provider.AdvanceRequest{ID: "adv-1", Expected: exp, Model: "claude-sonnet-4-6", PathRevision: "rev-A",
		ExecutionRevision: provider.ExecutionRevision{ID: "rev-A", PolicyRevision: 1}}
	next := exp.Clone()
	next.Model = req.Model
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &advanceRoutes{want: next, requests: []provider.VersionRequest{provider.VersionRequestFor(next, "")}})
	return svc, store, pub, req
}

func TestAdvanceModelCommitsThenPublishes(t *testing.T) {
	svc, store, pub, req := advanceFixture(t, "rev-A")
	adv, err := svc.AdvanceModel(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", adv.ToModel)
	assert.Equal(t, int64(4), adv.Revision)
	require.Len(t, store.calls, 1)
	require.Len(t, pub.seen, 1)
	assert.Equal(t, []provider.Advancement{adv}, pub.seen[0], "the publisher must receive the committed advancement")
}

func TestAdvanceModelStaleEvidenceNeitherCommitsNorPublishes(t *testing.T) {
	svc, store, pub, req := advanceFixture(t, "rev-B")
	_, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
	assert.Empty(t, store.calls)
	assert.Empty(t, pub.seen)
}

func TestAdvanceModelEmptyEvidenceFailsClosed(t *testing.T) {
	svc, store, pub, req := advanceFixture(t, "rev-A")
	req.PathRevision = ""
	_, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)
	assert.Empty(t, store.calls)
	assert.Empty(t, pub.seen)
}

// The token the store validates in SQL must be the one the gate rechecked: a
// token from another inventory snapshot never reaches the store.
func TestAdvanceModelRejectsTokenForAnotherRevision(t *testing.T) {
	for name, id := range map[string]string{"other snapshot": "rev-B", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			svc, store, pub, req := advanceFixture(t, "rev-A")
			req.ExecutionRevision.ID = id
			_, err := svc.AdvanceModel(context.Background(), req)
			require.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)
			assert.Empty(t, store.calls)
			assert.Empty(t, pub.seen)
		})
	}
}

func TestAdvanceModelConflictDoesNotPublish(t *testing.T) {
	svc, store, pub, req := advanceFixture(t, "rev-A")
	store.err = provider.ErrAdvanceConflict
	_, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrAdvanceConflict)
	assert.Empty(t, pub.seen)
}

func TestAdvanceModelPublishFailureReportsCommittedAdvancement(t *testing.T) {
	svc, _, pub, req := advanceFixture(t, "rev-A")
	pub.err = errors.New("reload down")
	adv, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrPublicationPending)
	assert.Equal(t, "claude-sonnet-4-6", adv.ToModel, "the durable change must be reported so publication can be retried")
}

func TestAdvanceModelRejectsPinnedProfileBeforeStore(t *testing.T) {
	svc, store, _, req := advanceFixture(t, "rev-A")
	req.Expected.SelectionMode, req.Expected.FollowTarget = provider.SelectionPinned, nil
	_, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrAdvanceConflict)
	assert.Empty(t, store.calls)
}

type checkingPublicationJournal struct {
	store     *fakeAdvanceStore
	publisher *fakePublisher
	err       error
}

func (j *checkingPublicationJournal) Unpublished(context.Context) ([]provider.Advancement, error) {
	return nil, errors.New("unexpected recovery read during direct advancement")
}
func (j *checkingPublicationJournal) Acknowledge(_ context.Context, ids []string) error {
	if len(j.store.calls) != 1 || len(j.publisher.seen) != 1 || len(j.publisher.seen[0]) != 1 || j.publisher.err != nil || !reflect.DeepEqual(ids, []string{j.store.calls[0].ID}) || j.publisher.seen[0][0].ID != ids[0] {
		return errors.New("acknowledgement must follow publication of the exact committed journal ID")
	}
	return j.err
}

func TestAdvanceModelMissingPublicationJournalFailsBeforeCAS(t *testing.T) {
	svc, store, pub, req := advanceFixture(t, "rev-A")
	svc.SetPublicationJournal(nil)
	_, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrAdvancementPortsMissing)
	assert.Empty(t, store.calls)
	assert.Empty(t, pub.seen)
}
func TestAdvanceModelAcknowledgementFailureReturnsCommittedChange(t *testing.T) {
	svc, store, pub, req := advanceFixture(t, "rev-A")
	svc.SetPublicationJournal(&checkingPublicationJournal{store: store, publisher: pub, err: errors.New("ack fault")})
	adv, err := svc.AdvanceModel(context.Background(), req)
	require.ErrorIs(t, err, provider.ErrPublicationPending)
	assert.Equal(t, req.ID, adv.ID)
	assert.Len(t, store.calls, 1)
}

func TestAdvanceModelWithoutDependenciesFailsClosed(t *testing.T) {
	svc := provider.NewProfileService(nil, provider.CapabilityRules{}, nil)
	_, err := svc.AdvanceModel(context.Background(), provider.AdvanceRequest{})
	require.ErrorIs(t, err, provider.ErrAdvancementPortsMissing)
}
