package identity

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDeviceRepo is an in-memory DeviceCodeRepository.
type fakeDeviceRepo struct {
	byHash     map[string]*DeviceCode
	byUserCode map[string]*DeviceCode
}

func newFakeDeviceRepo() *fakeDeviceRepo {
	return &fakeDeviceRepo{byHash: map[string]*DeviceCode{}, byUserCode: map[string]*DeviceCode{}}
}

func (r *fakeDeviceRepo) Create(_ context.Context, dc *DeviceCode) error {
	cp := *dc
	r.byHash[dc.HashedCode] = &cp
	r.byUserCode[dc.UserCode] = &cp
	return nil
}

func (r *fakeDeviceRepo) GetByHash(_ context.Context, h string) (*DeviceCode, error) {
	if dc, ok := r.byHash[h]; ok {
		cp := *dc
		return &cp, nil
	}
	return nil, ErrNotFound
}

func (r *fakeDeviceRepo) GetByUserCode(_ context.Context, uc string) (*DeviceCode, error) {
	if dc, ok := r.byUserCode[uc]; ok {
		cp := *dc
		return &cp, nil
	}
	return nil, ErrNotFound
}

func (r *fakeDeviceRepo) Approve(_ context.Context, uc string, uid uuid.UUID) error {
	dc, ok := r.byUserCode[uc]
	if !ok || dc.Status != DeviceCodePending {
		return ErrNotFound
	}
	dc.Status = DeviceCodeApproved
	dc.UserID = &uid
	return nil
}

func (r *fakeDeviceRepo) Deny(_ context.Context, uc string) error {
	dc, ok := r.byUserCode[uc]
	if !ok || dc.Status != DeviceCodePending {
		return ErrNotFound
	}
	dc.Status = DeviceCodeDenied
	return nil
}

func (r *fakeDeviceRepo) Consume(_ context.Context, h string) error {
	dc, ok := r.byHash[h]
	if !ok || dc.Status != DeviceCodeApproved {
		return ErrNotFound
	}
	dc.Status = DeviceCodeConsumed
	return nil
}

func (r *fakeDeviceRepo) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	n := 0
	for h, dc := range r.byHash {
		if dc.ExpiresAt.Before(now) {
			delete(r.byHash, h)
			delete(r.byUserCode, dc.UserCode)
			n++
		}
	}
	return n, nil
}

// countingTokenRepo records how many tokens were minted and for which user.
type countingTokenRepo struct {
	created []*Token
}

func (c *countingTokenRepo) Create(_ context.Context, t *Token) error {
	c.created = append(c.created, t)
	return nil
}
func (c *countingTokenRepo) ListByUser(context.Context, uuid.UUID) ([]Token, error) { return nil, nil }
func (c *countingTokenRepo) GetByHash(context.Context, string) (*Token, error)      { return nil, ErrNotFound }
func (c *countingTokenRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error     { return nil }
func (c *countingTokenRepo) TouchLastUsed(context.Context, uuid.UUID) error         { return nil }

func newDeviceSvc(repo DeviceCodeRepository, tokens *TokenService, now func() time.Time) *DeviceCodeService {
	s := NewDeviceCodeService(repo, tokens, 10*time.Minute, 5)
	if now != nil {
		s.now = now
	}
	return s
}

// Happy path: Start issues a code; after Approve, Poll mints a token owned by
// the approver and consumes the row.
func TestDeviceCodeService_HappyPath(t *testing.T) {
	ctx := context.Background()
	repo := newFakeDeviceRepo()
	tokRepo := &countingTokenRepo{}
	svc := newDeviceSvc(repo, NewTokenService(tokRepo), nil)

	start, err := svc.Start(ctx, "https://app.example.com/auth/device")
	require.NoError(t, err)
	assert.NotEmpty(t, start.DeviceCode)
	assert.NotEmpty(t, start.UserCode)
	assert.Equal(t, 5, start.Interval)
	assert.Equal(t, 600, start.ExpiresIn)

	approver := uuid.New()
	approveCtx := WithIdentity(ctx, Identity{UserID: approver, Role: RoleUser})
	require.NoError(t, svc.Approve(approveCtx, start.UserCode))

	plaintext, tok, err := svc.Poll(ctx, start.DeviceCode)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plaintext, "arb_"))
	require.NotNil(t, tok)
	assert.Equal(t, approver, tok.UserID)

	require.Len(t, tokRepo.created, 1)
	assert.Equal(t, approver, tokRepo.created[0].UserID, "token owned by the approver")

	// Row is now consumed; a second poll yields invalid_grant.
	_, _, err = svc.Poll(ctx, start.DeviceCode)
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

// Input validation: empty/garbage device codes -> invalid_grant; Approve with no
// identity -> unauthenticated.
func TestDeviceCodeService_Validation(t *testing.T) {
	ctx := context.Background()
	repo := newFakeDeviceRepo()
	svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), nil)

	_, _, err := svc.Poll(ctx, "")
	assert.ErrorIs(t, err, ErrInvalidGrant)
	_, _, err = svc.Poll(ctx, "garbage")
	assert.ErrorIs(t, err, ErrInvalidGrant)

	start, err := svc.Start(ctx, "uri")
	require.NoError(t, err)
	err = svc.Approve(ctx, start.UserCode) // no identity in ctx
	assert.ErrorIs(t, err, ErrUnauthenticated)
}

// Error/failure: pending -> authorization_pending; denied -> access_denied;
// expired (even if approved) -> expired_token.
func TestDeviceCodeService_PollStates(t *testing.T) {
	ctx := context.Background()

	t.Run("pending", func(t *testing.T) {
		repo := newFakeDeviceRepo()
		svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), nil)
		start, _ := svc.Start(ctx, "uri")
		_, _, err := svc.Poll(ctx, start.DeviceCode)
		assert.ErrorIs(t, err, ErrAuthorizationPending)
	})

	t.Run("denied", func(t *testing.T) {
		repo := newFakeDeviceRepo()
		svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), nil)
		start, _ := svc.Start(ctx, "uri")
		require.NoError(t, repo.Deny(ctx, start.UserCode))
		_, _, err := svc.Poll(ctx, start.DeviceCode)
		assert.ErrorIs(t, err, ErrAccessDenied)
	})

	t.Run("expired approved", func(t *testing.T) {
		repo := newFakeDeviceRepo()
		clock := &fakeClock{t: time.Now()}
		svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), clock.Now)
		start, _ := svc.Start(ctx, "uri")
		approveCtx := WithIdentity(ctx, Identity{UserID: uuid.New(), Role: RoleUser})
		require.NoError(t, svc.Approve(approveCtx, start.UserCode))
		// jump past the TTL.
		clock.t = clock.t.Add(11 * time.Minute)
		_, _, err := svc.Poll(ctx, start.DeviceCode)
		assert.ErrorIs(t, err, ErrExpired)
	})
}

// Boundary: double-consume mints exactly once.
func TestDeviceCodeService_DoubleConsume(t *testing.T) {
	ctx := context.Background()
	repo := newFakeDeviceRepo()
	tokRepo := &countingTokenRepo{}
	svc := newDeviceSvc(repo, NewTokenService(tokRepo), nil)

	start, _ := svc.Start(ctx, "uri")
	approveCtx := WithIdentity(ctx, Identity{UserID: uuid.New(), Role: RoleUser})
	require.NoError(t, svc.Approve(approveCtx, start.UserCode))

	_, _, err := svc.Poll(ctx, start.DeviceCode)
	require.NoError(t, err)
	_, _, err = svc.Poll(ctx, start.DeviceCode)
	assert.ErrorIs(t, err, ErrInvalidGrant)

	assert.Len(t, tokRepo.created, 1, "Mint must be called exactly once")
}

// Isolation/security: token owned by approver; device code never logged; a
// pending poll never mints.
func TestDeviceCodeService_Security(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	ctx := context.Background()
	repo := newFakeDeviceRepo()
	tokRepo := &countingTokenRepo{}
	svc := newDeviceSvc(repo, NewTokenService(tokRepo), nil)

	start, _ := svc.Start(ctx, "uri")

	// A pending poll never mints.
	_, _, err := svc.Poll(ctx, start.DeviceCode)
	assert.ErrorIs(t, err, ErrAuthorizationPending)
	assert.Empty(t, tokRepo.created)

	approver := uuid.New()
	approveCtx := WithIdentity(ctx, Identity{UserID: approver, Role: RoleUser})
	require.NoError(t, svc.Approve(approveCtx, start.UserCode))
	_, tok, err := svc.Poll(ctx, start.DeviceCode)
	require.NoError(t, err)
	assert.Equal(t, approver, tok.UserID)

	assert.NotContains(t, buf.String(), start.DeviceCode, "raw device code must never be logged")
}

// Failure: Deny with no identity returns ErrUnauthenticated.
func TestDeviceCodeService_Deny_Unauthenticated(t *testing.T) {
	ctx := context.Background() // no identity
	repo := newFakeDeviceRepo()
	svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), nil)

	start, err := svc.Start(ctx, "uri")
	require.NoError(t, err)

	err = svc.Deny(ctx, start.UserCode)
	assert.ErrorIs(t, err, ErrUnauthenticated)

	// Row must not have been mutated.
	dc, _ := repo.GetByUserCode(ctx, start.UserCode)
	assert.Equal(t, DeviceCodePending, dc.Status, "Deny without auth must not mutate the row")
}

// Failure: Approve on an expired code returns ErrExpired before touching the repo.
func TestDeviceCodeService_Approve_Expired(t *testing.T) {
	ctx := context.Background()
	repo := newFakeDeviceRepo()
	clock := &fakeClock{t: time.Now()}
	svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), clock.Now)

	start, err := svc.Start(ctx, "uri")
	require.NoError(t, err)

	clock.t = clock.t.Add(11 * time.Minute) // past the 10-min TTL
	approveCtx := WithIdentity(ctx, Identity{UserID: uuid.New(), Role: RoleUser})
	err = svc.Approve(approveCtx, start.UserCode)
	assert.ErrorIs(t, err, ErrExpired)

	// Row must still be Pending.
	dc, _ := repo.GetByUserCode(ctx, start.UserCode)
	assert.Equal(t, DeviceCodePending, dc.Status, "Approve on expired code must not change status")
}

// Boundary/edge: DeleteExpired delegates count to the repo and returns it.
func TestDeviceCodeService_DeleteExpired(t *testing.T) {
	ctx := context.Background()
	repo := newFakeDeviceRepo()
	svc := newDeviceSvc(repo, NewTokenService(&countingTokenRepo{}), nil)

	now := time.Now()
	// Seed two rows — one that has already expired.
	_ = repo.Create(ctx, &DeviceCode{
		HashedCode: "hash-live", UserCode: "LIVE",
		Status: DeviceCodePending, ExpiresAt: now.Add(time.Hour),
	})
	_ = repo.Create(ctx, &DeviceCode{
		HashedCode: "hash-old", UserCode: "DEAD",
		Status: DeviceCodePending, ExpiresAt: now.Add(-time.Second),
	})

	n, err := svc.DeleteExpired(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one expired row should be pruned")

	// The live row must survive.
	dc, lookupErr := repo.GetByHash(ctx, "hash-live")
	require.NoError(t, lookupErr)
	assert.NotNil(t, dc)
}

// fakeClock is a controllable clock for the service.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }
