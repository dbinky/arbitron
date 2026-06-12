package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

// fixedProfileStore is a read-only fixture: its mutators must report a dedicated
// "this store does not mutate" sentinel (ErrReadOnlyStore) rather than borrowing
// ErrProfileExists / ErrProfileNotFound, which describe a DIFFERENT situation. A
// caller branching on errors.Is(err, ErrProfileExists) (e.g. a retry-on-conflict
// helper) must not loop forever against a store where nothing ever exists.
func TestFixedProfileStore_WritesReturnReadOnlySentinel(t *testing.T) {
	s := fixedProfileStore{}
	ctx := context.Background()

	t.Run("Create", func(t *testing.T) {
		err := s.Create(ctx, provider.Profile{Alias: "x"})
		if !errors.Is(err, provider.ErrReadOnlyStore) {
			t.Fatalf("Create error = %v, want ErrReadOnlyStore", err)
		}
		if errors.Is(err, provider.ErrProfileExists) {
			t.Fatalf("Create must not borrow ErrProfileExists: %v", err)
		}
	})
	t.Run("Update", func(t *testing.T) {
		err := s.Update(ctx, provider.Profile{Alias: "x"})
		if !errors.Is(err, provider.ErrReadOnlyStore) {
			t.Fatalf("Update error = %v, want ErrReadOnlyStore", err)
		}
		if errors.Is(err, provider.ErrProfileNotFound) {
			t.Fatalf("Update must not borrow ErrProfileNotFound: %v", err)
		}
	})
	t.Run("Delete", func(t *testing.T) {
		err := s.Delete(ctx, "x")
		if !errors.Is(err, provider.ErrReadOnlyStore) {
			t.Fatalf("Delete error = %v, want ErrReadOnlyStore", err)
		}
		if errors.Is(err, provider.ErrProfileNotFound) {
			t.Fatalf("Delete must not borrow ErrProfileNotFound: %v", err)
		}
	})
}
