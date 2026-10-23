package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// holdRead takes the shared read lease (a run-snapshot capture or an in-flight
// advancement) and returns a release func.
func holdRead(t *testing.T, g *provider.ExecutionGate) func() {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = g.WithRead(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	return func() { close(release); <-done }
}

// blocksUntilReleased runs op in a goroutine, asserts it does not finish while
// the read lease is held, then asserts it finishes with no error once released.
func blocksUntilReleased(t *testing.T, name string, g *provider.ExecutionGate, op func() error) {
	t.Helper()
	release := holdRead(t, g)
	res := make(chan error, 1)
	go func() { res <- op() }()
	select {
	case err := <-res:
		release()
		t.Fatalf("%s ran while a gate read lease was held (err=%v): it is not on the shared gate", name, err)
	case <-time.After(75 * time.Millisecond):
	}
	release()
	select {
	case err := <-res:
		require.NoError(t, err, name)
	case <-time.After(2 * time.Second):
		t.Fatalf("%s never completed after the lease was released", name)
	}
}

func TestConnectionServiceMutationsWaitOnGate(t *testing.T) {
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}
	gate := provider.NewExecutionGate()
	store := newMemConnStore(conn)
	svc := provider.NewConnectionService(store, fakeProfileLister{})
	svc.SetMutationGate(gate)

	blocksUntilReleased(t, "Create", gate, func() error {
		return svc.Create(admins(), provider.Connection{Name: "d", Provider: "openrouter", BaseURL: "https://y"}, "k")
	})
	blocksUntilReleased(t, "Update", gate, func() error { return svc.Update(admins(), conn, nil) })
	blocksUntilReleased(t, "UpdatePatch", gate, func() error { return svc.UpdatePatch(admins(), conn, nil, nil) })
	blocksUntilReleased(t, "Delete", gate, func() error { return svc.Delete(admins(), "d") })
}

func TestConnectionServiceFailureReleasesGate(t *testing.T) {
	conn := provider.Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}
	gate := provider.NewExecutionGate()
	svc := provider.NewConnectionService(newMemConnStore(conn), fakeProfileLister{profiles: []provider.Profile{{Alias: "p", ConnectionRef: "c"}}})
	svc.SetMutationGate(gate)

	assert.ErrorIs(t, svc.Delete(admins(), "c"), provider.ErrConnectionInUse)
	assert.ErrorIs(t, svc.Create(admins(), conn, "k"), provider.ErrConnectionExists)
	// A rejected write must not strand the exclusive lease.
	require.NoError(t, gate.WithMutation(context.Background(), func(context.Context) error { return nil }))
}

func TestConnectionServiceRejectsBeforeTakingGate(t *testing.T) {
	gate := provider.NewExecutionGate()
	svc := provider.NewConnectionService(newMemConnStore(), fakeProfileLister{})
	svc.SetMutationGate(gate)
	release := holdRead(t, gate)
	defer release()
	// Authz fails fast and does not queue behind a held lease.
	done := make(chan error, 1)
	go func() { done <- svc.Create(users(), provider.Connection{Name: "x"}, "k") }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, provider.ErrForbidden)
	case <-time.After(time.Second):
		t.Fatal("a forbidden caller was queued behind the execution gate")
	}
}

func TestProfileServiceMutationsWaitOnGate(t *testing.T) {
	gate := provider.NewExecutionGate()
	store := newMemProfileStore(provider.Profile{Alias: "gone", Kind: provider.ProfileKindChat})
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})
	svc.SetAdvancementDependencies(&fakeAdvanceStore{}, &fakePublisher{}, gate)

	blocksUntilReleased(t, "Create", gate, func() error {
		_, _, err := svc.Create(admins(), provider.Profile{Alias: "new", Kind: provider.ProfileKindChat, Model: "sonnet-4.6"})
		return err
	})
	blocksUntilReleased(t, "Delete", gate, func() error { return svc.Delete(admins(), "gone") })
	assert.Equal(t, 2, rl.calls, "each admin write still recaches once, under the lease")
}

func TestProfileServiceFailedWriteReleasesGate(t *testing.T) {
	gate := provider.NewExecutionGate()
	svc := provider.NewProfileService(newMemProfileStore(), rules(), &fakeReloader{})
	svc.SetAdvancementDependencies(&fakeAdvanceStore{}, &fakePublisher{}, gate)
	assert.ErrorIs(t, svc.Delete(admins(), "ghost"), provider.ErrProfileNotFound)
	require.NoError(t, gate.WithMutation(context.Background(), func(context.Context) error { return nil }))
}
