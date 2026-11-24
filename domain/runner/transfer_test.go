package runner_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type memSessions struct {
	rows map[string]runner.TransferSession
}

func (m *memSessions) Create(_ context.Context, s runner.TransferSession) error {
	m.rows[s.TokenHash] = s
	return nil
}
func (m *memSessions) GetByHash(_ context.Context, h string) (runner.TransferSession, error) {
	s, ok := m.rows[h]
	if !ok {
		return s, runner.ErrTransferTokenInvalid
	}
	return s, nil
}
func (m *memSessions) RecordPart(_ context.Context, h string, i int, sha string) error {
	s, ok := m.rows[h]
	if !ok {
		return runner.ErrTransferTokenInvalid
	}
	s.Parts[i] = sha
	m.rows[h] = s
	return nil
}
func (m *memSessions) SetCheckpointRef(_ context.Context, h, ref string) error {
	s, ok := m.rows[h]
	if !ok {
		return runner.ErrTransferTokenInvalid
	}
	s.CheckpointRef = ref
	m.rows[h] = s
	return nil
}
func (m *memSessions) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	var n int64
	for h, s := range m.rows {
		if !now.Before(s.ExpiresAt) {
			delete(m.rows, h)
			n++
		}
	}
	return n, nil
}

type liveFences struct {
	current runner.Fence
	calls   int
}

func (l *liveFences) FenceCheck(_ context.Context, f runner.Fence) error {
	l.calls++
	if f != l.current {
		return runner.ErrFenced
	}
	return nil
}

type liveLeases struct {
	current engine.ResourceFenceRef
	calls   int
}

func (l *liveLeases) Check(_ context.Context, f engine.ResourceFenceRef) error {
	l.calls++
	if f != l.current {
		return runner.ErrResourceFenced
	}
	return nil
}
func pullScope(f runner.Fence, run uuid.UUID) runner.TransferScope {
	return runner.TransferScope{Fence: f, RunID: run, Direction: runner.TransferPull, BaseRef: (runner.CheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("a", 64)}).String(), Resource: engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: string(f.RunnerID), Generation: 1, Token: uuid.New()}}
}
func newTokenService(t *testing.T) (*runner.TransferTokenService, *memSessions, *liveFences, *liveLeases, *time.Time, runner.TransferScope) {
	t.Helper()
	store := &memSessions{rows: map[string]runner.TransferSession{}}
	f := runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 3}
	scope := pullScope(f, uuid.New())
	fences := &liveFences{current: f}
	leases := &liveLeases{current: scope.Resource}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return runner.NewTransferTokenService(store, fences, leases, func() time.Time { return now }), store, fences, leases, &now, scope
}
func TestTransferTokens_RefusesWrongRunnerExpiryAndStaleGeneration(t *testing.T) {
	ctx := context.Background()
	svc, _, fences, _, now, scope := newTokenService(t)
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	_, err = svc.Validate(ctx, tok.Token, "mac-2")
	require.ErrorIs(t, err, runner.ErrTransferScope)
	_, err = svc.Validate(ctx, "art_not-a-token", "mac-1")
	require.ErrorIs(t, err, runner.ErrTransferTokenInvalid)
	fences.current.Generation++
	_, err = svc.Validate(ctx, tok.Token, "mac-1")
	require.ErrorIs(t, err, runner.ErrFenced)
	_, err = svc.Issue(ctx, scope, time.Minute)
	require.ErrorIs(t, err, runner.ErrFenced)
	fences.current = scope.Fence
	*now = now.Add(time.Minute)
	_, err = svc.Validate(ctx, tok.Token, "mac-1")
	require.ErrorIs(t, err, runner.ErrTransferTokenExpired)
}
func TestTransferTokens_StaleResourceCannotIssueOrValidate(t *testing.T) {
	ctx := context.Background()
	svc, store, _, leases, _, scope := newTokenService(t)
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	leases.current.Generation++
	_, err = svc.Issue(ctx, scope, time.Minute)
	require.ErrorIs(t, err, runner.ErrResourceFenced)
	require.Len(t, store.rows, 1)
	_, err = svc.Validate(ctx, tok.Token, "mac-1")
	require.ErrorIs(t, err, runner.ErrResourceFenced)
}
func TestTransferTokens_ScopeRequiresPinnedBaseOrHash(t *testing.T) {
	for name, change := range map[string]func(*runner.TransferScope){
		"missing base": func(s *runner.TransferScope) { s.BaseRef = "" },
		"foreign base": func(s *runner.TransferScope) {
			s.BaseRef = (runner.CheckpointRef{RunID: uuid.New(), Version: 1, ManifestHash: strings.Repeat("a", 64)}).String()
		},
		"bundle with base": func(s *runner.TransferScope) {
			s.Direction = runner.TransferBundle
			s.BundleHash = strings.Repeat("a", 64)
		},
		"invalid bundle hash": func(s *runner.TransferScope) {
			s.Direction = runner.TransferBundle
			s.BaseRef = ""
			s.BundleHash = strings.Repeat("X", 64)
		},
		"both authorities":  func(s *runner.TransferScope) { s.MaintenanceOperationID = "drain" },
		"no authority":      func(s *runner.TransferScope) { s.Fence.JobID = uuid.Nil },
		"other resource":    func(s *runner.TransferScope) { s.Resource.Key = "run:" + uuid.New().String() },
		"wrong owner":       func(s *runner.TransferScope) { s.Resource.Owner = "other" },
		"missing resource":  func(s *runner.TransferScope) { s.Resource = engine.ResourceFenceRef{} },
		"invalid direction": func(s *runner.TransferScope) { s.Direction = "upload" },
		"missing run":       func(s *runner.TransferScope) { s.RunID = uuid.Nil },
		"missing runner":    func(s *runner.TransferScope) { s.Fence.RunnerID = "" },
		"zero generation":   func(s *runner.TransferScope) { s.Fence.Generation = 0 },
		"pull with hash":    func(s *runner.TransferScope) { s.BundleHash = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			svc, store, _, _, _, scope := newTokenService(t)
			change(&scope)
			_, err := svc.Issue(context.Background(), scope, time.Minute)
			require.ErrorIs(t, err, runner.ErrTransferScope)
			require.Empty(t, store.rows)
		})
	}
	for _, ttl := range []time.Duration{0, -time.Minute} {
		svc, store, _, _, _, scope := newTokenService(t)
		_, err := svc.Issue(context.Background(), scope, ttl)
		require.ErrorIs(t, err, runner.ErrTransferScope)
		require.Empty(t, store.rows)
	}
}
func TestTransferTokens_MaintenanceScopeChecksResourceOnly(t *testing.T) {
	ctx := context.Background()
	svc, _, fences, leases, _, scope := newTokenService(t)
	scope.Fence.JobID = uuid.Nil
	scope.Fence.Generation = 0
	scope.MaintenanceOperationID = "drain-1"
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	got, err := svc.Validate(ctx, tok.Token, "mac-1")
	require.NoError(t, err)
	require.Equal(t, scope, got.Scope())
	require.Zero(t, fences.calls)
	require.Equal(t, 2, leases.calls)
	leases.current.Generation++
	_, err = svc.Validate(ctx, tok.Token, "mac-1")
	require.ErrorIs(t, err, runner.ErrResourceFenced)
}
func TestTransferTokens_ConstructionFailsClosed(t *testing.T) {
	store := &memSessions{}
	fences := &liveFences{}
	leases := &liveLeases{}
	require.Panics(t, func() { runner.NewTransferTokenService(nil, fences, leases, nil) })
	require.Panics(t, func() { runner.NewTransferTokenService(store, nil, leases, nil) })
	require.Panics(t, func() { runner.NewTransferTokenService(store, fences, nil, nil) })
	svc := runner.NewTransferTokenService(&memSessions{rows: map[string]runner.TransferSession{}}, fences, leases, nil)
	scope := pullScope(runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 1}, uuid.New())
	fences.current = scope.Fence
	leases.current = scope.Resource
	tok, err := svc.Issue(context.Background(), scope, time.Minute)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Minute), tok.ExpiresAt, time.Second)
}
func TestTransferTokens_SessionTracksPartsAndCommit(t *testing.T) {
	ctx := context.Background()
	svc, _, _, leases, now, scope := newTokenService(t)
	scope.Direction = runner.TransferPush
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	require.NoError(t, svc.RecordPart(ctx, tok.Token, 1, strings.Repeat("b", 64)))
	require.NoError(t, svc.RecordPart(ctx, tok.Token, 0, strings.Repeat("a", 64)))
	require.NoError(t, svc.RecordCommit(ctx, tok.Token, "ref"))
	row, err := svc.Session(ctx, tok.Token, "mac-1")
	require.NoError(t, err)
	require.Equal(t, map[int]string{0: strings.Repeat("a", 64), 1: strings.Repeat("b", 64)}, row.Parts)
	require.Equal(t, "ref", row.CheckpointRef)
	leases.current.Generation++
	require.ErrorIs(t, svc.RecordPart(ctx, tok.Token, 2, strings.Repeat("c", 64)), runner.ErrResourceFenced)
	require.ErrorIs(t, svc.RecordCommit(ctx, tok.Token, "other"), runner.ErrResourceFenced)
	*now = now.Add(time.Minute)
	n, err := svc.Sweep(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}
func TestTransferTokens_IssueStoresOnlyTheHashAndValidates(t *testing.T) {
	ctx := context.Background()
	svc, store, _, _, _, scope := newTokenService(t)
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(tok.Token, runner.TransferTokenPrefix))
	require.Len(t, tok.Token, len(runner.TransferTokenPrefix)+43)
	require.Len(t, store.rows, 1)
	row := store.rows[runner.HashTransferToken(tok.Token)]
	require.Equal(t, scope.BaseRef, row.BaseRef)
	require.Equal(t, scope.Resource, row.Resource)
	require.NotContains(t, row.TokenHash, tok.Token[len(runner.TransferTokenPrefix):])
	got, err := svc.Validate(ctx, tok.Token, scope.Fence.RunnerID)
	require.NoError(t, err)
	require.Equal(t, tok, got)
	require.Equal(t, scope, got.Scope())
	second, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, tok.Token, second.Token)
}
