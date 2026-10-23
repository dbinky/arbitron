package provider_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
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
func (s *memProfileStore) UpdateExpected(_ context.Context, expected, next provider.Profile) (provider.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.byAlias[expected.Alias]
	if !ok || !reflect.DeepEqual(current, expected) || next.Alias != expected.Alias {
		return provider.Profile{}, provider.ErrAdvanceConflict
	}
	s.byAlias[next.Alias] = next.Clone()
	s.updates++
	return next.Clone(), nil
}

func checkPreparedSelection(s provider.PreparedSelection) error {
	if s.Candidate == nil || s.Candidate.Model != s.Profile.Model || s.Candidate.CanonicalID == "" || s.Candidate.PathRevision == "" || s.Candidate.ExecutionRevision.ID != s.Candidate.PathRevision {
		return provider.ErrExecutionRevisionUnavailable
	}
	return nil
}

func (s *memProfileStore) CreateValidated(ctx context.Context, selection provider.PreparedSelection) (provider.Profile, error) {
	if err := checkPreparedSelection(selection); err != nil {
		return provider.Profile{}, err
	}
	if err := s.Create(ctx, selection.Profile); err != nil {
		return provider.Profile{}, err
	}
	return selection.Profile.Clone(), nil
}

func (s *memProfileStore) UpdateValidated(ctx context.Context, expected provider.Profile, selection provider.PreparedSelection) (provider.Profile, error) {
	if err := checkPreparedSelection(selection); err != nil {
		return provider.Profile{}, err
	}
	return s.UpdateExpected(ctx, expected, selection.Profile)
}

func (s *memProfileStore) DeleteExpected(_ context.Context, expected provider.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.byAlias[expected.Alias]
	if !ok || !reflect.DeepEqual(current, expected) {
		return provider.ErrAdvanceConflict
	}
	delete(s.byAlias, expected.Alias)
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
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})

	// Inapplicable axis: codex exposes no context knob.
	_, _, err := svc.Create(admins(), provider.Profile{Alias: "bad", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "gpt-6.1-sol", ContextWindow: provider.Context1M})
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
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})

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
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})

	stored, _, err := svc.Create(admins(), provider.Profile{Alias: "ok", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "sonnet-4.6"})
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
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})

	stored, eff, err := svc.Create(admins(), provider.Profile{
		Alias: "plain-chat", Kind: provider.ProfileKindChat, Model: "sonnet-4.6",
		// Backend and ConnectionRef intentionally empty.
	})
	require.NoError(t, err, "empty backend + empty connection must validate as claude-cli (default/default)")
	assert.Equal(t, "plain-chat", stored.Alias)
	assert.Equal(t, provider.ContextDefault, eff.ContextWindow)
	assert.Equal(t, provider.EffortDefault, eff.Effort)
	assert.Equal(t, 1, rl.calls)
}

// Gap 2 — Success: a connection-backed CHAT profile with an empty agent backend
// must be creatable. Its EffectiveBackend is "" (the connection supplies routing),
// which is absent from the capability ruleset; rather than ErrUnknownBackend, the
// connection-supplied chat path validates against the chat-tier axis policy.
// Default axes always pass, so the write succeeds.
func TestProfileService_CreateChatConnectionDefaultAxes(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &ollamaRouteSource{})

	stored, eff, err := svc.Create(admins(), provider.Profile{
		Alias: "workhorse", Kind: provider.ProfileKindChat,
		ConnectionRef: "ollama-cloud", Model: "deepseek-v4-flash",
		// Backend intentionally empty: the connection supplies the routing.
	})
	require.NoError(t, err, "a connection-backed chat profile must be creatable without an agent backend")
	assert.Equal(t, "workhorse", stored.Alias)
	assert.Equal(t, provider.ContextDefault, eff.ContextWindow)
	assert.Equal(t, provider.EffortDefault, eff.Effort)
	assert.Equal(t, 1, store.writeCount())
	assert.Equal(t, 1, rl.calls)
}

// Success: a connection-backed chat profile may set a graded reasoning effort. The
// chat tier honors it (it reaches the OpenAI-compatible adapter as reasoning_effort),
// so high validates and echoes high as the effective effort.
func TestProfileService_CreateChatConnectionEffortHonored(t *testing.T) {
	store := newMemProfileStore()
	svc := provider.NewProfileService(store, rules(), &fakeReloader{})
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &ollamaRouteSource{})

	_, eff, err := svc.Create(admins(), provider.Profile{
		Alias: "ds4", Kind: provider.ProfileKindChat,
		ConnectionRef: "ollama-cloud", Model: "deepseek-v4-flash",
		Effort: provider.EffortHigh,
	})
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff.Effort, "graded effort is honored on the chat tier")
}

// Floor: the OpenAI-compatible chat vocabulary has no xhigh, so an xhigh request on
// a connection-backed chat profile floors to high — and the echoed Effective shows
// the floored value, matching what the adapter will emit.
func TestProfileService_CreateChatConnectionFloorsXHigh(t *testing.T) {
	store := newMemProfileStore()
	svc := provider.NewProfileService(store, rules(), &fakeReloader{})
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &ollamaRouteSource{})

	_, eff, err := svc.Create(admins(), provider.Profile{
		Alias: "ds4x", Kind: provider.ProfileKindChat,
		ConnectionRef: "ollama-cloud", Model: "deepseek-v4-flash",
		Effort: provider.EffortXHigh,
	})
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff.Effort, "xhigh floors to high on the chat tier")
}

// Error: the OpenAI-compatible chat tier has no context-window knob, so a
// non-default context on a connection-backed chat profile is rejected with
// ErrAxisNotApplicable and nothing is persisted.
func TestProfileService_CreateChatConnectionNonDefaultContextRejected(t *testing.T) {
	store := newMemProfileStore()
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &ollamaRouteSource{})

	_, _, err := svc.Create(admins(), provider.Profile{
		Alias: "ds4ctx", Kind: provider.ProfileKindChat,
		ConnectionRef: "ollama-cloud", Model: "deepseek-v4-flash",
		ContextWindow: provider.Context1M,
	})
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable, "a context tier is not applicable to the chat connection path")
	assert.Equal(t, 0, store.writeCount(), "nothing persisted on a rejected axis")
	assert.Equal(t, 0, rl.calls)
}

// Regression: an AGENT profile with a connection but an empty backend must still
// fail with ErrUnknownBackend — an agent needs a concrete backend; only the chat
// path is exempted. This guards the seed contract (profile_seed_test.go).
func TestProfileService_CreateAgentConnectionEmptyBackendStillRejected(t *testing.T) {
	store := newMemProfileStore()
	svc := provider.NewProfileService(store, rules(), &fakeReloader{})
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &ollamaRouteSource{})

	_, _, err := svc.Create(admins(), provider.Profile{
		Alias: "cloud-agent", Kind: provider.ProfileKindAgent,
		ConnectionRef: "my-conn", Model: "deepseek-v4-flash", // backend="" conn!="" on the AGENT path
	})
	assert.ErrorIs(t, err, provider.ErrUnknownBackend, "an agent profile must name a backend in the ruleset")
	assert.Equal(t, 0, store.writeCount())
}

// Error (Pass 2): Update on an unauthenticated context (no identity) returns
// ErrUnauthenticated — anon is distinct from non-admin (ErrForbidden).
func TestProfileService_UpdateUnauthenticated(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "x", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	svc := provider.NewProfileService(store, rules(), &fakeReloader{})
	_, _, err := svc.Update(anon(), provider.Profile{Alias: "x", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	assert.ErrorIs(t, err, provider.ErrUnauthenticated)
}

// Error (Pass 2): a persist error (e.g. ErrProfileExists on Create) propagates
// and the reloader is NOT called — reload is only triggered on a durable write,
// not on a failed one.
func TestProfileService_PersistErrorSuppressesReload(t *testing.T) {
	// Pre-seed the store so Create returns ErrProfileExists.
	store := newMemProfileStore(provider.Profile{Alias: "dup", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)

	svc.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})
	_, _, err := svc.Create(admins(), provider.Profile{Alias: "dup", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "sonnet-4.6"})
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrProfileExists)
	assert.Equal(t, 0, rl.calls, "reloader must NOT be called when persist fails")
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

// ollamaRouteSource reports the connection's route provider, as production
// does for a connection-backed profile, so the fixed-version grammar applied is
// the connection's and not the claude subscription's.
type ollamaRouteSource struct{ fakeSource }

func (ollamaRouteSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	return []provider.VersionRequest{provider.VersionRequestFor(p, "ollama-cloud")}, nil
}
