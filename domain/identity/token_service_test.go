package identity

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTokenRepo is an in-memory TokenRepository capturing calls for assertions.
type fakeTokenRepo struct {
	created   []*Token
	createErr error
	deleteErr error

	deletedID     uuid.UUID
	deletedUserID uuid.UUID
	listedUserID  uuid.UUID
	listResult    []Token
	listCalls     int
	deleteCalls   int
	createCalls   int
}

func (f *fakeTokenRepo) Create(_ context.Context, t *Token) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, t)
	return nil
}

func (f *fakeTokenRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]Token, error) {
	f.listCalls++
	f.listedUserID = userID
	if f.listResult == nil {
		return []Token{}, nil
	}
	return f.listResult, nil
}

func (f *fakeTokenRepo) GetByHash(_ context.Context, _ string) (*Token, error) {
	return nil, ErrNotFound
}

func (f *fakeTokenRepo) Delete(_ context.Context, id, userID uuid.UUID) error {
	f.deleteCalls++
	f.deletedID = id
	f.deletedUserID = userID
	return f.deleteErr
}

func (f *fakeTokenRepo) TouchLastUsed(_ context.Context, _ uuid.UUID) error { return nil }

func ctxWithUser(uid uuid.UUID) context.Context {
	return WithIdentity(context.Background(), Identity{UserID: uid, Username: "u", Role: RoleUser})
}

// Happy path: Mint returns plaintext + a Token for the caller, persisting a hash
// that matches the plaintext; List and Revoke round-trip.
func TestTokenService_Mint_HappyPath(t *testing.T) {
	repo := &fakeTokenRepo{}
	svc := NewTokenService(repo)
	uid := uuid.New()
	ctx := ctxWithUser(uid)

	plaintext, tok, err := svc.Mint(ctx, "laptop")
	require.NoError(t, err)
	assert.NotEmpty(t, plaintext)
	assert.True(t, strings.HasPrefix(plaintext, "arb_"))
	require.NotNil(t, tok)
	assert.Equal(t, uid, tok.UserID)
	assert.Equal(t, "laptop", tok.Label)
	assert.NotEmpty(t, tok.Prefix)

	require.Len(t, repo.created, 1)
	assert.Equal(t, HashToken(plaintext), repo.created[0].TokenHash, "stored hash must match plaintext")
	assert.Empty(t, repo.created[0].ID == uuid.Nil, "id should be assigned")
}

// Input validation: blank/whitespace labels are rejected without a Create call.
func TestTokenService_Mint_InvalidLabel(t *testing.T) {
	repo := &fakeTokenRepo{}
	svc := NewTokenService(repo)
	ctx := ctxWithUser(uuid.New())

	for _, bad := range []string{"", "   ", "\t\n"} {
		plaintext, tok, err := svc.Mint(ctx, bad)
		assert.ErrorIs(t, err, ErrInvalidLabel)
		assert.Empty(t, plaintext)
		assert.Nil(t, tok)
	}
	assert.Equal(t, 0, repo.createCalls, "no Create on invalid label")
}

// Error/failure: a repo Create error propagates and NO plaintext leaks.
func TestTokenService_Mint_RepoError(t *testing.T) {
	boom := errors.New("db down")
	repo := &fakeTokenRepo{createErr: boom}
	svc := NewTokenService(repo)
	ctx := ctxWithUser(uuid.New())

	plaintext, tok, err := svc.Mint(ctx, "laptop")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, plaintext, "no plaintext on failure")
	assert.Nil(t, tok)
}

// Error/failure: Revoke surfaces repo ErrNotFound.
func TestTokenService_Revoke_NotFound(t *testing.T) {
	repo := &fakeTokenRepo{deleteErr: ErrNotFound}
	svc := NewTokenService(repo)
	ctx := ctxWithUser(uuid.New())

	err := svc.Revoke(ctx, uuid.New())
	assert.ErrorIs(t, err, ErrNotFound)
}

// Boundary: List with zero tokens is a non-nil empty slice.
func TestTokenService_List_Empty(t *testing.T) {
	repo := &fakeTokenRepo{}
	svc := NewTokenService(repo)
	ctx := ctxWithUser(uuid.New())

	list, err := svc.List(ctx)
	require.NoError(t, err)
	assert.NotNil(t, list)
	assert.Empty(t, list)
}

// Isolation: every method scopes to the context identity's user.
func TestTokenService_Isolation_ScopedToCaller(t *testing.T) {
	repo := &fakeTokenRepo{}
	svc := NewTokenService(repo)
	aUID := uuid.New()
	ctx := ctxWithUser(aUID)

	_, err := svc.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, aUID, repo.listedUserID, "List must scope to caller A")

	tokenID := uuid.New()
	_ = svc.Revoke(ctx, tokenID)
	assert.Equal(t, tokenID, repo.deletedID)
	assert.Equal(t, aUID, repo.deletedUserID, "Revoke must pass caller A as the owner scope")
}

// Isolation/security: with no identity in ctx, every method is unauthenticated
// and never touches the repo.
func TestTokenService_NoIdentity_Unauthenticated(t *testing.T) {
	repo := &fakeTokenRepo{}
	svc := NewTokenService(repo)
	ctx := context.Background()

	_, _, err := svc.Mint(ctx, "x")
	assert.ErrorIs(t, err, ErrUnauthenticated)

	_, err = svc.List(ctx)
	assert.ErrorIs(t, err, ErrUnauthenticated)

	err = svc.Revoke(ctx, uuid.New())
	assert.ErrorIs(t, err, ErrUnauthenticated)

	assert.Zero(t, repo.createCalls)
	assert.Zero(t, repo.listCalls)
	assert.Zero(t, repo.deleteCalls)
}

// Security: the plaintext never appears in any log line emitted during Mint.
func TestTokenService_Mint_PlaintextNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	repo := &fakeTokenRepo{}
	svc := NewTokenService(repo)
	ctx := ctxWithUser(uuid.New())

	plaintext, _, err := svc.Mint(ctx, "laptop")
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), plaintext, "plaintext must never be logged")
}
