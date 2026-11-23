package runner_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

type auditCall struct{ action, target, detail string }

type recAudit struct {
	mu    sync.Mutex
	calls []auditCall
}

func (a *recAudit) Record(_ context.Context, action, target, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, auditCall{action, target, detail})
}

func (a *recAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []string{}
	for _, c := range a.calls {
		out = append(out, c.action+" "+c.target)
	}
	return out
}

type recRevoker struct {
	mu  sync.Mutex
	ids []runner.RunnerID
}

func (r *recRevoker) RevokeRunner(_ context.Context, id runner.RunnerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

type keyFixture struct {
	db      *sql.DB
	svc     *runner.KeyService
	reg     runner.Registry
	audit   *recAudit
	revoker *recRevoker
}

func newKeyFixture(t *testing.T) keyFixture {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "keys.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	reg := adaptsqlite.NewRunnerRegistryRepo(db)
	a, rv := &recAudit{}, &recRevoker{}
	svc := runner.NewKeyService(runner.KeyServiceDeps{
		Store: adaptsqlite.NewRunnerKeyRepo(db), Registry: reg, Audit: a, Revoker: rv,
		Clock: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }, DefaultStepLimit: 8,
	})
	return keyFixture{db: db, svc: svc, reg: reg, audit: a, revoker: rv}
}

func adminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin})
}

func userCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser})
}

func sign(t *testing.T, priv ed25519.PrivateKey, keyID uuid.UUID) (nonce, sig []byte) {
	t.Helper()
	nonce, err := runner.NewChallengeNonce()
	require.NoError(t, err)
	return nonce, ed25519.Sign(priv, runner.ChallengeMessage(keyID, nonce))
}

func TestKeyService_CreateShowsSecretOnceStoresHashOnly(t *testing.T) {
	f := newKeyFixture(t)
	plaintext, k, err := f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plaintext, runner.RunnerKeyPrefix))
	assert.Len(t, plaintext, len(runner.RunnerKeyPrefix)+43)
	assert.Empty(t, k.Hash, "the hash never leaves the service")
	assert.Equal(t, runner.RunnerID("mac-a"), k.RunnerID)
	assert.True(t, strings.HasPrefix(plaintext, k.Prefix))

	// The database holds the hash, never the plaintext.
	var hash, prefix string
	var pub []byte
	require.NoError(t, f.db.QueryRow(`SELECT key_hash, prefix, machine_public_key FROM runner_keys WHERE name = 'mac-a'`).Scan(&hash, &prefix, &pub))
	sum := sha256.Sum256([]byte(plaintext))
	assert.Equal(t, hex.EncodeToString(sum[:]), hash)
	assert.NotContains(t, hash+prefix, plaintext[len(k.Prefix):])
	assert.Nil(t, pub, "unclaimed")

	keys, err := f.svc.List(adminCtx())
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Empty(t, keys[0].Hash, "list never exposes the hash")

	r, _, err := f.reg.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.Liveness("offline"), r.Liveness, "a key registers an offline runner row")
	assert.Equal(t, 8, r.StepLimit)
	assert.False(t, r.BuiltIn)
	assert.Equal(t, []string{audit.ActionRunnerKeyCreate + " mac-a"}, f.audit.actions())
}

func TestKeyService_CreateValidationAndAuthz(t *testing.T) {
	f := newKeyFixture(t)
	for _, bad := range []string{"", "a", "Mac-A", "-mac", "mac_a", "builtin", strings.Repeat("a", 64)} {
		_, _, err := f.svc.Create(adminCtx(), bad)
		assert.ErrorIs(t, err, runner.ErrInvalidKeyName, bad)
	}
	_, _, err := f.svc.Create(userCtx(), "mac-a")
	assert.ErrorIs(t, err, identity.ErrForbidden)
	_, _, err = f.svc.Create(context.Background(), "mac-a")
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.svc.List(userCtx())
	assert.ErrorIs(t, err, identity.ErrForbidden)

	_, _, err = f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)
	_, _, err = f.svc.Create(adminCtx(), "mac-a")
	assert.ErrorIs(t, err, runner.ErrKeyNameTaken)
	assert.Equal(t, []string{audit.ActionRunnerKeyCreate + " mac-a"}, f.audit.actions(), "failed creates are not audited")
}

func TestKeyService_Authenticate(t *testing.T) {
	f := newKeyFixture(t)
	plaintext, created, err := f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)

	k, err := f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)
	assert.Equal(t, created.ID, k.ID)
	var lastUsed sql.NullString
	require.NoError(t, f.db.QueryRow(`SELECT last_used_at FROM runner_keys WHERE id = ?`, k.ID.String()).Scan(&lastUsed))
	assert.True(t, lastUsed.Valid, "authentication records last use")

	for _, bad := range []string{"", "ark_short", "arb_" + plaintext[4:], plaintext + "x", "ark_" + strings.Repeat("A", 43)} {
		_, err := f.svc.Authenticate(context.Background(), bad)
		assert.ErrorIs(t, err, runner.ErrKeyNotFound, bad)
	}
}

// Design §8 Success: "The first claim binds the key to the machine."
func TestKeyService_FirstClaimBindsMachineAndReconnectProvesPossession(t *testing.T) {
	f := newKeyFixture(t)
	plaintext, _, err := f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)
	k, err := f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nonce, sig := sign(t, priv, k.ID)
	bound, err := f.svc.ProveMachine(context.Background(), k, nonce, pub, sig)
	require.NoError(t, err)
	assert.Equal(t, []byte(pub), bound.MachinePublicKey)
	require.NotNil(t, bound.ClaimedAt)

	// Reconnect: a fresh challenge signed by the same machine is accepted.
	again, err := f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)
	assert.Equal(t, []byte(pub), again.MachinePublicKey)
	nonce2, sig2 := sign(t, priv, k.ID)
	_, err = f.svc.ProveMachine(context.Background(), again, nonce2, pub, sig2)
	require.NoError(t, err)

	// Replaying the old signature against a new nonce fails.
	_, err = f.svc.ProveMachine(context.Background(), again, nonce2, pub, sig)
	assert.ErrorIs(t, err, runner.ErrWrongMachine)
}

// Design §8 Failure: "A copied key used from a second machine (different keypair) is rejected."
func TestKeyService_CopiedKeyFromSecondKeypairRejected(t *testing.T) {
	f := newKeyFixture(t)
	plaintext, _, err := f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)
	k, err := f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	n, s := sign(t, privA, k.ID)
	_, err = f.svc.ProveMachine(context.Background(), k, n, pubA, s)
	require.NoError(t, err)

	copied, err := f.svc.Authenticate(context.Background(), plaintext) // same key, other Mac
	require.NoError(t, err)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	nB, sB := sign(t, privB, k.ID)
	_, err = f.svc.ProveMachine(context.Background(), copied, nB, pubB, sB)
	assert.ErrorIs(t, err, runner.ErrWrongMachine, "a valid signature from another keypair is still the wrong machine")

	_, err = f.svc.ProveMachine(context.Background(), copied, nB, pubA, sB)
	assert.ErrorIs(t, err, runner.ErrWrongMachine, "claiming machine A's public key without A's private key fails")
}

// Design §8 regression: "simultaneous first claims bind one machine".
func TestKeyService_SimultaneousFirstClaimsBindExactlyOne(t *testing.T) {
	f := newKeyFixture(t)
	plaintext, _, err := f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)
	k, err := f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)

	const n = 10
	pubs := make([]ed25519.PublicKey, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		pubs[i] = pub
		nonce, sig := sign(t, priv, k.ID)
		wg.Add(1)
		go func(i int, nonce, sig []byte) {
			defer wg.Done()
			_, errs[i] = f.svc.ProveMachine(context.Background(), k, nonce, pubs[i], sig)
		}(i, nonce, sig)
	}
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner, "more than one machine bound the key")
			winner = i
			continue
		}
		assert.ErrorIs(t, err, runner.ErrWrongMachine)
	}
	require.NotEqual(t, -1, winner)
	stored, err := f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)
	assert.Equal(t, []byte(pubs[winner]), stored.MachinePublicKey)
}

func TestKeyService_RevokeRequiresAckDisconnectsAndAudits(t *testing.T) {
	f := newKeyFixture(t)
	plaintext, k, err := f.svc.Create(adminCtx(), "mac-a")
	require.NoError(t, err)

	err = f.svc.Revoke(adminCtx(), k.ID, runner.RevokeAck{Acknowledged: false})
	assert.ErrorIs(t, err, runner.ErrRevokeNotAcknowledged)
	_, err = f.svc.Authenticate(context.Background(), plaintext)
	require.NoError(t, err, "an unacknowledged revoke changes nothing")
	assert.Empty(t, f.revoker.ids)

	assert.ErrorIs(t, f.svc.Revoke(userCtx(), k.ID, runner.RevokeAck{Acknowledged: true}), identity.ErrForbidden)
	assert.ErrorIs(t, f.svc.Revoke(adminCtx(), uuid.New(), runner.RevokeAck{Acknowledged: true}), runner.ErrKeyNotFound)

	require.NoError(t, f.svc.Revoke(adminCtx(), k.ID, runner.RevokeAck{Acknowledged: true}))
	assert.Equal(t, []runner.RunnerID{"mac-a"}, f.revoker.ids, "the live session is closed immediately")
	_, err = f.svc.Authenticate(context.Background(), plaintext)
	assert.ErrorIs(t, err, runner.ErrKeyRevoked)
	assert.Contains(t, f.audit.actions(), audit.ActionRunnerKeyRevoke+" mac-a")

	require.NoError(t, f.svc.Revoke(adminCtx(), k.ID, runner.RevokeAck{Acknowledged: true}), "idempotent")
	assert.Len(t, f.revoker.ids, 1, "a second revoke does not re-disconnect")
}

func TestChallengeMessage_FormatAndVerify(t *testing.T) {
	keyID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	nonce := make([]byte, runner.ChallengeNonceBytes)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	msg := runner.ChallengeMessage(keyID, nonce)
	assert.Equal(t, "arbitron-runner-v1:"+keyID.String()+":"+base64.StdEncoding.EncodeToString(nonce), string(msg))

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig := ed25519.Sign(priv, msg)
	assert.True(t, runner.VerifyChallenge(pub, keyID, nonce, sig))
	assert.False(t, runner.VerifyChallenge(pub, uuid.New(), nonce, sig), "bound to the key id")
	tampered := append([]byte(nil), nonce...)
	tampered[0] ^= 0xff
	assert.False(t, runner.VerifyChallenge(pub, keyID, tampered, sig), "bound to the nonce")
	assert.False(t, runner.VerifyChallenge(pub[:31], keyID, nonce, sig), "malformed key")

	n1, err := runner.NewChallengeNonce()
	require.NoError(t, err)
	n2, _ := runner.NewChallengeNonce()
	assert.Len(t, n1, runner.ChallengeNonceBytes)
	assert.NotEqual(t, n1, n2, "nonces are fresh")
}
