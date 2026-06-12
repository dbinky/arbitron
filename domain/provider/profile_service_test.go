package provider_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
)

// admins / users / anon build request contexts carrying (or omitting) an
// identity so the service authz gate can be exercised without the auth
// middleware. They mirror the helpers in instance.UserService's tests.
func admins() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
}
func users() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser})
}
func anon() context.Context { return context.Background() }

// memProfileStore is an in-memory provider.ProfileStore for service tests. It
// records every write so a test can assert nothing was persisted on a rejected
// write, and supports seeding for read-path tests.
type memProfileStore struct {
	mu      sync.Mutex
	byAlias map[string]provider.Profile
	creates int
	updates int
	deletes int
}

func newMemProfileStore(seed ...provider.Profile) *memProfileStore {
	s := &memProfileStore{byAlias: map[string]provider.Profile{}}
	for _, p := range seed {
		s.byAlias[p.Alias] = p
	}
	return s
}

func (s *memProfileStore) Create(_ context.Context, p provider.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byAlias[p.Alias]; ok {
		return provider.ErrProfileExists
	}
	s.byAlias[p.Alias] = p
	s.creates++
	return nil
}
func (s *memProfileStore) Update(_ context.Context, p provider.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byAlias[p.Alias]; !ok {
		return provider.ErrProfileNotFound
	}
	s.byAlias[p.Alias] = p
	s.updates++
	return nil
}
func (s *memProfileStore) Delete(_ context.Context, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byAlias[alias]; !ok {
		return provider.ErrProfileNotFound
	}
	delete(s.byAlias, alias)
	s.deletes++
	return nil
}
func (s *memProfileStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byAlias[alias]
	if !ok {
		return provider.Profile{}, provider.ErrProfileNotFound
	}
	return p, nil
}
func (s *memProfileStore) List(_ context.Context) ([]provider.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Profile, 0, len(s.byAlias))
	for _, p := range s.byAlias {
		out = append(out, p)
	}
	return out, nil
}
func (s *memProfileStore) Count(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byAlias), nil
}

func (s *memProfileStore) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates + s.updates + s.deletes
}

// fakeReloader records how many times Reload was invoked and can be made to
// fail so the best-effort recache path is exercised.
type fakeReloader struct {
	calls int
	err   error
}

func (r *fakeReloader) Reload(context.Context) error { r.calls++; return r.err }

var _ provider.Reloader = (*fakeReloader)(nil)

// Authz: Create requires admin; an authenticated non-admin is forbidden, and a
// missing identity is unauthenticated. Neither persists nor reloads.
func TestProfileService_CreateRequiresAdmin(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	_, _, err := svc.Create(users(), provider.Profile{Alias: "x", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	assert.ErrorIs(t, err, provider.ErrForbidden)

	_, _, err = svc.Create(anon(), provider.Profile{Alias: "x"})
	assert.ErrorIs(t, err, provider.ErrUnauthenticated)

	assert.Equal(t, 0, store.writeCount(), "rejected writes must not persist")
	assert.Equal(t, 0, rl.calls, "rejected writes must not reload")
}

// Capability validation: an inapplicable axis is rejected (nothing persisted,
// no reload); a floored effort persists the request and echoes the floored
// effective value with exactly one reload.
func TestProfileService_CreateValidatesAndReloads(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	// Inapplicable axis: codex exposes no context knob.
	_, _, err := svc.Create(admins(), provider.Profile{Alias: "bad", Kind: provider.ProfileKindAgent, Backend: "codex", ContextWindow: provider.Context1M})
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable)
	assert.Equal(t, 0, store.writeCount(), "rejected write must not persist")
	assert.Equal(t, 0, rl.calls, "no reload on rejected write")

	// Floor case: sonnet-4.6 caps effort at high; xhigh floors to high.
	stored, eff, err := svc.Create(admins(), provider.Profile{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "sonnet-4.6", Effort: provider.EffortXHigh})
	require.NoError(t, err)
	assert.Equal(t, "ok", stored.Alias)
	assert.Equal(t, provider.EffortXHigh, stored.Effort, "stored profile keeps the requested effort")
	assert.Equal(t, provider.EffortHigh, eff.Effort, "effective effort is floored")
	assert.Equal(t, 1, rl.calls, "successful write reloads exactly once")
}

// Update gates on admin, validates, persists, and reloads like Create.
func TestProfileService_UpdateValidatesAndReloads(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	_, _, err := svc.Update(users(), provider.Profile{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	assert.ErrorIs(t, err, provider.ErrForbidden)
	assert.Equal(t, 0, rl.calls)

	_, eff, err := svc.Update(admins(), provider.Profile{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "opus-4.8", ContextWindow: provider.Context1M, Effort: provider.EffortHigh})
	require.NoError(t, err)
	assert.Equal(t, provider.Context1M, eff.ContextWindow)
	assert.Equal(t, provider.EffortHigh, eff.Effort)
	assert.Equal(t, 1, rl.calls)
}

// Delete gates on admin, removes, and reloads.
func TestProfileService_DeleteRequiresAdminAndReloads(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "gone", Kind: provider.ProfileKindChat})
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	assert.ErrorIs(t, svc.Delete(users(), "gone"), provider.ErrForbidden)
	assert.Equal(t, 0, rl.calls)

	require.NoError(t, svc.Delete(admins(), "gone"))
	assert.Equal(t, 1, rl.calls)
	assert.Equal(t, 0, store.writeCount()-1, "delete counted")
	_, err := store.Get(context.Background(), "gone")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
}

// Delete on a missing alias surfaces ErrProfileNotFound and does not reload.
func TestProfileService_DeleteMissing(t *testing.T) {
	rl := &fakeReloader{}
	svc := provider.NewProfileService(newMemProfileStore(), rules(), rl)
	err := svc.Delete(admins(), "ghost")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	assert.Equal(t, 0, rl.calls, "no reload when the delete failed")
}

// Read paths are open to any authenticated identity but reject an unauthenticated
// caller.
func TestProfileService_ListReadOpenToAuthenticated(t *testing.T) {
	svc := provider.NewProfileService(newMemProfileStore(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat}), rules(), &fakeReloader{})

	list, err := svc.List(users())
	require.NoError(t, err, "non-admin read should succeed")
	assert.Len(t, list, 1)

	_, err = svc.List(anon())
	assert.ErrorIs(t, err, provider.ErrUnauthenticated, "unauthenticated read must fail")
}

func TestProfileService_GetReadOpenToAuthenticated(t *testing.T) {
	svc := provider.NewProfileService(newMemProfileStore(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat}), rules(), &fakeReloader{})

	got, err := svc.Get(users(), "a")
	require.NoError(t, err)
	assert.Equal(t, "a", got.Alias)

	_, err = svc.Get(anon(), "a")
	assert.ErrorIs(t, err, provider.ErrUnauthenticated)
}

// A reload failure after a durable write does not fail the write: the DB already
// holds the truth, so the stored profile + effective values are still returned.
func TestProfileService_ReloadFailureIsNonFatal(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{err: errors.New("boom")}
	svc := provider.NewProfileService(store, rules(), rl)

	stored, _, err := svc.Create(admins(), provider.Profile{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	require.NoError(t, err, "a reload failure must not fail a durable write")
	assert.Equal(t, "ok", stored.Alias)
	assert.Equal(t, 1, rl.calls)
	assert.Equal(t, 1, store.writeCount(), "write persisted despite reload failure")
}

// Failure: a malformed axis string (not a valid enum value) is rejected before
// persist and before rules.Validate — the ParseContextWindow/ParseEffort gate
// is upstream of Validate and must fail closed with nothing stored.
func TestProfileService_CreateMalformedAxisRejected(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	// Malformed context_window.
	_, _, err := svc.Create(admins(), provider.Profile{
		Alias: "bad-cw", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		ContextWindow: "not-a-tier",
	})
	assert.Error(t, err, "malformed ContextWindow must be rejected")
	assert.Equal(t, 0, store.writeCount(), "nothing persisted on axis parse error")
	assert.Equal(t, 0, rl.calls)

	// Malformed effort.
	_, _, err = svc.Create(admins(), provider.Profile{
		Alias: "bad-eff", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		Effort: "not-an-effort",
	})
	assert.Error(t, err, "malformed Effort must be rejected")
	assert.Equal(t, 0, store.writeCount())
}

// Success: a profile with empty Backend and empty ConnectionRef defaults the
// validation backend to "claude-cli" (the no-key subscription path). Default
// axes (default/default) always validate on claude-li, so the write succeeds.
func TestProfileService_CreateEmptyBackendDefaultsToCli(t *testing.T) {
	// Use a ruleset that includes claude-li (the substituted backend).
	// The shared rules() fixture only covers claude-code + codex.
	cliRules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-cli": {Backend: "claude-cli"},
	})
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, cliRules, rl)

	stored, eff, err := svc.Create(admins(), provider.Profile{
		Alias: "plain-chat", Kind: provider.ProfileKindChat,
		// Backend and ConnectionRef intentionally empty.
	})
	require.NoError(t, err, "empty backend + empty connection must validate as claude-cli (default/default)")
	assert.Equal(t, "plain-chat", stored.Alias)
	assert.Equal(t, provider.ContextDefault, eff.ContextWindow)
	assert.Equal(t, provider.EffortDefault, eff.Effort)
	assert.Equal(t, 1, rl.calls)
}

// Error: Delete on an unauthenticated context returns ErrUnauthenticated (anon
// is not the same as non-admin).
func TestProfileService_DeleteUnauthenticated(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "x", Kind: provider.ProfileKindChat})
	svc := provider.NewProfileService(store, rules(), &fakeReloader{})
	err := svc.Delete(anon(), "x")
	assert.ErrorIs(t, err, provider.ErrUnauthenticated)
}

// Capabilities is read-open: any authenticated caller can fetch the ruleset to
// discover valid axis values; an unauthenticated caller is rejected.
func TestProfileService_Capabilities(t *testing.T) {
	svc := provider.NewProfileService(newMemProfileStore(), rules(), &fakeReloader{})

	caps, err := svc.Capabilities(users())
	require.NoError(t, err)
	assert.Contains(t, caps.Describe(), "claude-code")
	assert.Contains(t, caps.Describe(), "codex")

	_, err = svc.Capabilities(anon())
	assert.ErrorIs(t, err, provider.ErrUnauthenticated)
}
