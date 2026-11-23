package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
)

// RunnerKeyPrefix marks every runner key. Runner keys are a separate credential
// kind from user API tokens (arb_…) and authorize only the runner channel.
const RunnerKeyPrefix = "ark_"

// ChallengeNonceBytes is the size of the server's machine-binding challenge.
const ChallengeNonceBytes = 32

const (
	runnerKeySecretBytes = 32
	runnerKeyEncodedLen  = 43 // base64.RawURLEncoding of 32 bytes
	runnerKeyPrefixShown = 8  // clear characters after "ark_" kept for display
	challengeDomain      = "arbitron-runner-v1:"
)

// KeySetupGuidance accompanies the once-shown plaintext on every surface.
const KeySetupGuidance = "Copy this key now; it is shown only once. On the Mac, put ARBITRON_SERVER_URL=<this server's URL> " +
	"and ARBITRON_RUNNER_KEY=<this key> in ~/.arbitron-runner/runner.env (mode 0600), then run install.sh --runner. " +
	"The key binds to the first machine that connects with it."

var runnerKeyNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

var (
	ErrKeyNotFound           = errors.New("runner: key not found")
	ErrKeyRevoked            = errors.New("runner: key revoked")
	ErrKeyAlreadyClaimed     = errors.New("runner: key already claimed by a machine")
	ErrKeyNameTaken          = errors.New("runner: key name already in use")
	ErrInvalidKeyName        = errors.New(`runner: key name must match ^[a-z0-9][a-z0-9-]{1,62}$ and must not be "builtin"`)
	ErrRevokeNotAcknowledged = errors.New("runner: revocation must acknowledge that the runner disconnects immediately and interrupted steps may repeat (drain first if possible)")
	ErrWrongMachine          = errors.New("runner: key is bound to a different machine")
)

// RunnerKey is one runner credential. Hash is the SHA-256 hex of the plaintext
// and is cleared on every value returned to callers outside the service.
type RunnerKey struct {
	ID               uuid.UUID
	Name             string
	RunnerID         RunnerID
	Hash             string
	Prefix           string
	MachinePublicKey []byte
	CreatedAt        time.Time
	ClaimedAt        *time.Time
	LastUsedAt       *time.Time
	RevokedAt        *time.Time
}

// KeyStore persists runner keys (adapter/sqlite/runner_key_repo.go).
type KeyStore interface {
	Create(ctx context.Context, k *RunnerKey) error
	GetByHash(ctx context.Context, hash string) (RunnerKey, error)
	List(ctx context.Context) ([]RunnerKey, error)
	Claim(ctx context.Context, id uuid.UUID, pub []byte, at time.Time) error
	TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error
	Revoke(ctx context.Context, id uuid.UUID, at time.Time) error
}

// SessionRevoker closes a runner's live session and treats it as lost (the Hub).
type SessionRevoker interface {
	RevokeRunner(ctx context.Context, id RunnerID) error
}

// RevokeAck is the explicit operator confirmation carried by REST/MCP/dashboard.
type RevokeAck struct{ Acknowledged bool }

// KeyServiceDeps wires the key service.
type KeyServiceDeps struct {
	Store            KeyStore
	Registry         Registry
	Audit            AuditRecorder
	Revoker          SessionRevoker
	Clock            func() time.Time
	DefaultStepLimit int
}

// KeyService owns runner key lifecycle and machine binding.
type KeyService struct {
	d    KeyServiceDeps
	now  func() time.Time
	rand io.Reader
}

// NewKeyService validates and builds the service. Missing ports are a wiring bug.
func NewKeyService(d KeyServiceDeps) *KeyService {
	if d.Store == nil || d.Registry == nil || d.Audit == nil || d.Revoker == nil {
		panic("runner: NewKeyService requires Store, Registry, Audit and Revoker")
	}
	if d.DefaultStepLimit < 1 {
		panic("runner: NewKeyService requires DefaultStepLimit >= 1")
	}
	now := d.Clock
	if now == nil {
		now = time.Now
	}
	return &KeyService{d: d, now: now, rand: rand.Reader}
}

func requireAdmin(ctx context.Context) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.ErrUnauthenticated
	}
	if !id.IsAdmin() {
		return identity.ErrForbidden
	}
	return nil
}

// HashRunnerKey is the SHA-256 hex of the full plaintext.
func HashRunnerKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func auditDetail(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Create mints a key (admin only). The plaintext is returned exactly once; only
// its hash is stored. The name becomes the runner name and an offline runner
// row is registered so the runner is visible before it ever connects.
func (s *KeyService) Create(ctx context.Context, name string) (string, RunnerKey, error) {
	if err := requireAdmin(ctx); err != nil {
		return "", RunnerKey{}, err
	}
	name = strings.TrimSpace(name)
	if !runnerKeyNameRE.MatchString(name) || RunnerID(name) == BuiltInRunnerID {
		return "", RunnerKey{}, ErrInvalidKeyName
	}
	if _, _, err := s.d.Registry.GetRunner(ctx, RunnerID(name)); err == nil {
		return "", RunnerKey{}, ErrKeyNameTaken
	} else if !errors.Is(err, ErrRunnerNotFound) {
		return "", RunnerKey{}, fmt.Errorf("runner: checking runner %q: %w", name, err)
	}
	secret := make([]byte, runnerKeySecretBytes)
	if _, err := io.ReadFull(s.rand, secret); err != nil {
		return "", RunnerKey{}, fmt.Errorf("runner: generating key: %w", err)
	}
	plaintext := RunnerKeyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	now := s.now().UTC()
	k := RunnerKey{ID: uuid.New(), Name: name, RunnerID: RunnerID(name), Hash: HashRunnerKey(plaintext),
		Prefix: plaintext[:len(RunnerKeyPrefix)+runnerKeyPrefixShown], CreatedAt: now}
	if err := s.d.Store.Create(ctx, &k); err != nil {
		return "", RunnerKey{}, err
	}
	r := Runner{ID: k.RunnerID, Name: name, StepLimit: s.d.DefaultStepLimit, Labels: map[string]string{},
		Compat: Compatibility{State: CompatCompatible}, Liveness: LivenessOffline, CreatedAt: now}
	if err := s.d.Registry.UpsertRunner(ctx, r); err != nil {
		_ = s.d.Store.Revoke(ctx, k.ID, now) // never leave a usable key without its runner row
		return "", RunnerKey{}, fmt.Errorf("runner: registering runner %q: %w", name, err)
	}
	s.d.Audit.Record(ctx, audit.ActionRunnerKeyCreate, name, auditDetail(map[string]any{
		"keyId": k.ID.String(), "runnerId": name, "prefix": k.Prefix}))
	k.Hash = ""
	return plaintext, k, nil
}

// List returns every key (admin only) without hashes.
func (s *KeyService) List(ctx context.Context) ([]RunnerKey, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	keys, err := s.d.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		keys[i].Hash = ""
	}
	return keys, nil
}

// Revoke (admin only) requires the acknowledgment, revokes the key, closes the
// runner's live session (treated as lost; its steps are re-placed) and audits.
func (s *KeyService) Revoke(ctx context.Context, id uuid.UUID, ack RevokeAck) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	if !ack.Acknowledged {
		return ErrRevokeNotAcknowledged
	}
	keys, err := s.d.Store.List(ctx)
	if err != nil {
		return err
	}
	var k *RunnerKey
	for i := range keys {
		if keys[i].ID == id {
			k = &keys[i]
			break
		}
	}
	if k == nil {
		return ErrKeyNotFound
	}
	if k.RevokedAt != nil {
		return nil
	}
	if err := s.d.Store.Revoke(ctx, id, s.now().UTC()); err != nil {
		return err
	}
	revErr := s.d.Revoker.RevokeRunner(ctx, k.RunnerID)
	s.d.Audit.Record(ctx, audit.ActionRunnerKeyRevoke, k.Name, auditDetail(map[string]any{
		"keyId": id.String(), "runnerId": string(k.RunnerID), "acknowledged": true}))
	if revErr != nil {
		return fmt.Errorf("runner: key revoked but closing runner %s failed: %w", k.RunnerID, revErr)
	}
	return nil
}

// Authenticate resolves a presented plaintext key. Malformed keys never reach the store.
func (s *KeyService) Authenticate(ctx context.Context, plaintext string) (RunnerKey, error) {
	if !strings.HasPrefix(plaintext, RunnerKeyPrefix) || len(plaintext) != len(RunnerKeyPrefix)+runnerKeyEncodedLen {
		return RunnerKey{}, ErrKeyNotFound
	}
	k, err := s.d.Store.GetByHash(ctx, HashRunnerKey(plaintext))
	if err != nil {
		return RunnerKey{}, err
	}
	if k.RevokedAt != nil {
		return RunnerKey{}, ErrKeyRevoked
	}
	_ = s.d.Store.TouchLastUsed(ctx, k.ID, s.now().UTC()) // bookkeeping never blocks admission
	return k, nil
}

// ProveMachine verifies the challenge signature and binds an unclaimed key to
// pub atomically. A bound key accepts only its own machine.
func (s *KeyService) ProveMachine(ctx context.Context, k RunnerKey, nonce []byte, pub ed25519.PublicKey, sig []byte) (RunnerKey, error) {
	if !VerifyChallenge(pub, k.ID, nonce, sig) {
		return RunnerKey{}, ErrWrongMachine
	}
	if k.RevokedAt != nil {
		return RunnerKey{}, ErrKeyRevoked
	}
	if k.MachinePublicKey != nil {
		if subtle.ConstantTimeCompare(k.MachinePublicKey, pub) != 1 {
			return RunnerKey{}, ErrWrongMachine
		}
		return k, nil
	}
	now := s.now().UTC()
	switch err := s.d.Store.Claim(ctx, k.ID, pub, now); {
	case err == nil:
		k.MachinePublicKey = append([]byte(nil), pub...)
		k.ClaimedAt = &now
		return k, nil
	case errors.Is(err, ErrKeyAlreadyClaimed):
		cur, gerr := s.d.Store.GetByHash(ctx, k.Hash)
		if gerr != nil {
			return RunnerKey{}, gerr
		}
		if subtle.ConstantTimeCompare(cur.MachinePublicKey, pub) == 1 {
			return cur, nil
		}
		return RunnerKey{}, ErrWrongMachine
	default:
		return RunnerKey{}, err
	}
}

// NewChallengeNonce returns 32 fresh random bytes.
func NewChallengeNonce() ([]byte, error) {
	n := make([]byte, ChallengeNonceBytes)
	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("runner: challenge nonce: %w", err)
	}
	return n, nil
}

// ChallengeMessage is the exact byte string the runner signs.
func ChallengeMessage(keyID uuid.UUID, nonce []byte) []byte {
	return []byte(challengeDomain + keyID.String() + ":" + base64.StdEncoding.EncodeToString(nonce))
}

// VerifyChallenge checks an ed25519 signature over ChallengeMessage.
func VerifyChallenge(pub ed25519.PublicKey, keyID uuid.UUID, nonce, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || len(nonce) != ChallengeNonceBytes {
		return false
	}
	return ed25519.Verify(pub, ChallengeMessage(keyID, nonce), sig)
}
