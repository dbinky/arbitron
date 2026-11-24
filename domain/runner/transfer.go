package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// Transfer directions match migration 044's transfer_sessions vocabulary.
const (
	TransferPull   = "pull"
	TransferPush   = "push"
	TransferBundle = "bundle"
)

// DefaultTransferTokenTTL is used when no positive transfer lifetime is supplied.
const DefaultTransferTokenTTL = 15 * time.Minute

// TransferScope names exactly one job fence or durable maintenance operation,
// together with the pinned checkpoint/bundle and exact writer claim.
type TransferScope struct {
	Fence                  Fence
	MaintenanceOperationID string
	RunID                  uuid.UUID
	Direction              string
	BaseRef                string
	BundleHash             string
	Resource               engine.ResourceFenceRef
}

// TransferToken carries the exact authority issued for one transfer.
type TransferToken struct {
	Token                  string
	MaintenanceOperationID string
	JobID                  uuid.UUID
	Generation             int64
	RunnerID               RunnerID
	RunID                  uuid.UUID
	Direction              string
	BaseRef                string
	Resource               engine.ResourceFenceRef
	BundleHash             string
	ExpiresAt              time.Time
}

// Scope projects all authorization fields, excluding token text and expiry.
func (t TransferToken) Scope() TransferScope {
	return TransferScope{
		Fence:                  Fence{JobID: t.JobID, RunnerID: t.RunnerID, Generation: t.Generation},
		MaintenanceOperationID: t.MaintenanceOperationID,
		RunID:                  t.RunID, Direction: t.Direction, BaseRef: t.BaseRef,
		BundleHash: t.BundleHash, Resource: t.Resource,
	}
}

// TransferTokenPrefix distinguishes transfer credentials from runner keys.
const TransferTokenPrefix = "art_"
const transferTokenBytes = 32

var (
	ErrTransferTokenInvalid = errors.New("invalid transfer token")
	ErrTransferTokenExpired = errors.New("expired transfer token")
	ErrTransferScope        = errors.New("invalid transfer scope")
)

// TransferSession persists pinned authority and upload progress, never plaintext credentials.
type TransferSession struct {
	TokenHash, MaintenanceOperationID string
	JobID                             uuid.UUID
	Generation                        int64
	RunnerID                          RunnerID
	RunID                             uuid.UUID
	Direction, BaseRef, BundleHash    string
	Resource                          engine.ResourceFenceRef
	CheckpointRef                     string
	ExpiresAt                         time.Time
	Parts                             map[int]string
	CreatedAt                         time.Time
}

// TransferSessionStore stores hashed transfer credentials and resumable progress.
type TransferSessionStore interface {
	Create(context.Context, TransferSession) error
	GetByHash(context.Context, string) (TransferSession, error)
	RecordPart(context.Context, string, int, string) error
	SetCheckpointRef(context.Context, string, string) error
	DeleteExpired(context.Context, time.Time) (int64, error)
}

// FenceChecker verifies the exact live job assignment.
type FenceChecker interface {
	FenceCheck(context.Context, Fence) error
}

// ResourceChecker verifies the exact live resource claim.
type ResourceChecker interface {
	Check(context.Context, engine.ResourceFenceRef) error
}

// TransferTokenService issues short-lived credentials and rechecks both fences per request.
type TransferTokenService struct {
	store     TransferSessionStore
	fences    FenceChecker
	resources ResourceChecker
	now       func() time.Time
}

// NewTransferTokenService requires both authority checkers; a nil clock uses time.Now.
func NewTransferTokenService(store TransferSessionStore, fences FenceChecker, resources ResourceChecker, now func() time.Time) *TransferTokenService {
	if store == nil || fences == nil || resources == nil {
		panic("runner: transfer tokens require store, fences and resources")
	}
	if now == nil {
		now = time.Now
	}
	return &TransferTokenService{store: store, fences: fences, resources: resources, now: now}
}

// HashTransferToken is the persistent lookup key for a plaintext credential.
func HashTransferToken(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}
func (s *TransferTokenService) Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error) {
	if ttl <= 0 || !validTransferScope(scope) {
		return TransferToken{}, ErrTransferScope
	}
	if err := s.checkLive(ctx, scope); err != nil {
		return TransferToken{}, err
	}
	bytes := make([]byte, transferTokenBytes)
	if _, err := rand.Read(bytes); err != nil {
		return TransferToken{}, err
	}
	plaintext := TransferTokenPrefix + base64.RawURLEncoding.EncodeToString(bytes)
	now := s.now()
	row := TransferSession{
		TokenHash: HashTransferToken(plaintext),
		JobID:     scope.Fence.JobID, Generation: scope.Fence.Generation, RunnerID: scope.Fence.RunnerID,
		RunID: scope.RunID, Direction: scope.Direction,
		BaseRef: scope.BaseRef, BundleHash: scope.BundleHash, Resource: scope.Resource,
		MaintenanceOperationID: scope.MaintenanceOperationID,
		ExpiresAt:              now.Add(ttl), CreatedAt: now, Parts: map[int]string{},
	}
	if err := s.store.Create(ctx, row); err != nil {
		return TransferToken{}, err
	}
	return row.token(plaintext), nil
}
func (row TransferSession) token(plaintext string) TransferToken {
	return TransferToken{
		Token: plaintext, JobID: row.JobID, Generation: row.Generation, RunnerID: row.RunnerID,
		RunID: row.RunID, Direction: row.Direction,
		BaseRef: row.BaseRef, BundleHash: row.BundleHash, Resource: row.Resource,
		MaintenanceOperationID: row.MaintenanceOperationID, ExpiresAt: row.ExpiresAt,
	}
}
func (s *TransferTokenService) Session(ctx context.Context, plaintext string, r RunnerID) (TransferSession, error) {
	row, err := s.store.GetByHash(ctx, HashTransferToken(plaintext))
	if err != nil {
		return TransferSession{}, err
	}
	if err := s.validateSession(ctx, row, r); err != nil {
		return TransferSession{}, err
	}
	return row, nil
}
func (s *TransferTokenService) RecordPart(ctx context.Context, plaintext string, index int, hash string) error {
	row, err := s.ownSession(ctx, plaintext)
	if err != nil {
		return err
	}
	return s.store.RecordPart(ctx, row.TokenHash, index, hash)
}
func (s *TransferTokenService) RecordCommit(ctx context.Context, plaintext, ref string) error {
	row, err := s.ownSession(ctx, plaintext)
	if err != nil {
		return err
	}
	return s.store.SetCheckpointRef(ctx, row.TokenHash, ref)
}
func (s *TransferTokenService) ownSession(ctx context.Context, plaintext string) (TransferSession, error) {
	row, err := s.store.GetByHash(ctx, HashTransferToken(plaintext))
	if err != nil {
		return TransferSession{}, err
	}
	if err := s.validateSession(ctx, row, row.RunnerID); err != nil {
		return TransferSession{}, err
	}
	return row, nil
}
func (s *TransferTokenService) Sweep(ctx context.Context) (int64, error) {
	return s.store.DeleteExpired(ctx, s.now())
}
func (s *TransferTokenService) Validate(ctx context.Context, plaintext string, r RunnerID) (TransferToken, error) {
	row, err := s.store.GetByHash(ctx, HashTransferToken(plaintext))
	if err != nil {
		return TransferToken{}, err
	}
	if err := s.validateSession(ctx, row, r); err != nil {
		return TransferToken{}, err
	}
	return row.token(plaintext), nil
}
func (s *TransferTokenService) validateSession(ctx context.Context, row TransferSession, r RunnerID) error {
	if row.RunnerID != r {
		return ErrTransferScope
	}
	if !s.now().Before(row.ExpiresAt) {
		return ErrTransferTokenExpired
	}
	return s.checkLive(ctx, row.token("").Scope())
}
func validTransferScope(scope TransferScope) bool {
	f := scope.Fence
	if scope.RunID == uuid.Nil || f.RunnerID == "" || scope.Resource.Validate() != nil {
		return false
	}
	if f.JobID != uuid.Nil {
		if f.Generation < 1 || scope.MaintenanceOperationID != "" || scope.Resource.Owner != string(f.RunnerID) {
			return false
		}
	} else if f.Generation != 0 || scope.MaintenanceOperationID == "" {
		return false
	}
	switch scope.Direction {
	case TransferPull, TransferPush:
		ref, err := ParseCheckpointRef(scope.BaseRef)
		return err == nil && ref.RunID == scope.RunID && ref.Version > 0 && scope.BundleHash == "" && scope.Resource.Key == "run:"+scope.RunID.String()
	case TransferBundle:
		hash, err := hex.DecodeString(scope.BundleHash)
		return scope.BaseRef == "" && err == nil && len(hash) == sha256.Size && scope.BundleHash == strings.ToLower(scope.BundleHash)
	default:
		return false
	}
}
func (s *TransferTokenService) checkLive(ctx context.Context, scope TransferScope) error {
	if scope.Fence.JobID != uuid.Nil {
		if err := s.fences.FenceCheck(ctx, scope.Fence); err != nil {
			return err
		}
	}
	return s.resources.Check(ctx, scope.Resource)
}

// TransferTokens issues and validates tokens against live job/resource fences.
type TransferTokens interface {
	Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error)
	Validate(ctx context.Context, token string, runner RunnerID) (TransferToken, error)
}
