package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errRDHUnused = errors.New("rdh fake: unexpected call")

// rdhHomes is a HomeStore keyed by runner. ListByRunner honours its id
// argument and records every id it was asked for; err fails every listing.
type rdhHomes struct {
	mu       sync.Mutex
	byRunner map[RunnerID][]Home
	err      error
	asked    []RunnerID
}

func (h *rdhHomes) ListByRunner(_ context.Context, id RunnerID) ([]Home, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, id)
	if h.err != nil {
		return nil, h.err
	}
	return append([]Home(nil), h.byRunner[id]...), nil
}

func (h *rdhHomes) askedIDs() []RunnerID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]RunnerID(nil), h.asked...)
}

func (h *rdhHomes) GetHome(context.Context, uuid.UUID) (Home, error) { return Home{}, errRDHUnused }
func (h *rdhHomes) AcquireResource(context.Context, uuid.UUID, RunnerID, int64) (Home, error) {
	return Home{}, errRDHUnused
}
func (h *rdhHomes) HandOff(context.Context, uuid.UUID, RunnerID, string, int64, int64, HomeMoveCause) (Home, error) {
	return Home{}, errRDHUnused
}
func (h *rdhHomes) SetNonPortable(context.Context, uuid.UUID, string) error { return errRDHUnused }
func (h *rdhHomes) SetCheckpoint(context.Context, uuid.UUID, int64) error   { return errRDHUnused }
func (h *rdhHomes) MarkDirty(context.Context, uuid.UUID, bool) error        { return errRDHUnused }
func (h *rdhHomes) History(context.Context, uuid.UUID) ([]HomeMove, error)  { return nil, errRDHUnused }
func (h *rdhHomes) Release(context.Context, uuid.UUID, int64) error         { return errRDHUnused }

// rdhFixture is the P2 T12 fixture (runners "builtin" and "mac-a") with the
// given home store. A nil homes leaves ServiceDeps.Homes a nil interface.
func rdhFixture(t *testing.T, homes HomeStore) *svFixture {
	t.Helper()
	f := rdtFixture(t, nil)
	d := f.svc.d
	d.Homes = homes
	f.svc = NewService(d)
	return f
}

var rdhMacRun = uuid.New()

func rdhStore() *rdhHomes {
	return &rdhHomes{byRunner: map[RunnerID][]Home{
		BuiltInRunnerID: {
			{RunID: svVisRun, RunnerID: BuiltInRunnerID, Generation: 3, CheckpointVersion: 7, Dirty: true},
			{RunID: svHidRun, RunnerID: BuiltInRunnerID, Generation: 5, CheckpointVersion: 2},
		},
		"mac-a": {{RunID: rdhMacRun, RunnerID: "mac-a", Generation: 1}},
	}}
}

// Happy path + success: the runner's homes, in store order; the visible run
// is linked, the hidden run keeps its generation/dirty flag but not its id.
func TestServiceGet_HomesAreTheRunnersHomesWithVisibility(t *testing.T) {
	homes := rdhStore()
	f := rdhFixture(t, homes)

	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	require.Len(t, d.Homes, 2)
	require.NotNil(t, d.Homes[0].RunID)
	assert.Equal(t, svVisRun, *d.Homes[0].RunID)
	assert.Equal(t, HomeView{RunID: d.Homes[0].RunID, Hidden: false, Generation: 3, Dirty: true}, d.Homes[0])
	assert.Equal(t, HomeView{RunID: nil, Hidden: true, Generation: 5, Dirty: false}, d.Homes[1], "a hidden run never leaks its id")
	assert.Equal(t, []RunnerID{BuiltInRunnerID}, homes.askedIDs(), "homes are listed for the requested runner")
}

// Success: another runner's detail lists only its own homes.
func TestServiceGet_HomesOfOtherRunnersAreNotListed(t *testing.T) {
	homes := rdhStore()
	f := rdhFixture(t, homes)
	d, err := f.svc.Get(svCtx(identity.RoleUser), "mac-a")
	require.NoError(t, err)
	require.Len(t, d.Homes, 1)
	assert.True(t, d.Homes[0].Hidden, "svVisibility only admits svVisRun")
	assert.Equal(t, int64(1), d.Homes[0].Generation)
	assert.Equal(t, []RunnerID{"mac-a"}, homes.askedIDs())
}

// Edge: no home store (Phase 1/2 wiring) lists nothing, as an empty list.
func TestServiceGet_NilHomesLeavesHomesEmpty(t *testing.T) {
	f := rdhFixture(t, nil)
	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	require.NotNil(t, d.Homes)
	assert.Empty(t, d.Homes)
	assert.Len(t, d.Assignments, 2)
}

// Error: a failing home store fails the read; it is never shown as "no homes".
func TestServiceGet_HomeStoreErrorFailsTheRead(t *testing.T) {
	disk := errors.New("disk gone")
	f := rdhFixture(t, &rdhHomes{err: disk})
	_, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.Error(t, err)
	assert.ErrorIs(t, err, disk)
	assert.Contains(t, err.Error(), "runner: list homes of builtin")
}

// Failure: unauthenticated callers and unknown runners fail before any home
// is listed.
func TestServiceGet_FailuresNeverListHomes(t *testing.T) {
	homes := rdhStore()
	f := rdhFixture(t, homes)
	_, err := f.svc.Get(context.Background(), BuiltInRunnerID)
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.svc.Get(svCtx(identity.RoleAdmin), "laptop")
	assert.ErrorIs(t, err, ErrRunnerNotFound)
	assert.Empty(t, homes.askedIDs())
}
