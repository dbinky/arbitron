package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
)

// Default: a zero-value Profile must report empty axes so existing config
// profiles (which never set ContextWindow/Effort) stay valid — an empty axis is
// the "unset/default" sentinel that emits nothing.
func TestProfile_NewAxesDefault(t *testing.T) {
	var p provider.Profile
	assert.Equal(t, provider.ContextWindow(""), p.ContextWindow, "zero Profile must have empty ContextWindow (treated as default)")
	assert.Equal(t, provider.Effort(""), p.Effort, "zero Profile must have empty Effort (treated as default)")
}

// Default: a zero-value ResolvedBinding likewise carries empty axes; the
// resolver copies the profile's axes verbatim, and an unset profile yields an
// unset binding (no-op for the Phase-3 emitter).
func TestResolvedBinding_NewAxesDefault(t *testing.T) {
	var b provider.ResolvedBinding
	assert.Equal(t, provider.ContextWindow(""), b.ContextWindow)
	assert.Equal(t, provider.Effort(""), b.Effort)
}

// Port: the new ProfileStore sentinels are distinct, non-nil errors callers can
// match with errors.Is.
func TestProfileStore_Sentinels(t *testing.T) {
	assert.Error(t, provider.ErrProfileExists)
	assert.Error(t, provider.ErrProfileNotFound)
	assert.NotEqual(t, provider.ErrProfileExists, provider.ErrProfileNotFound)
}

// fakeProfileStore is a compile-time witness that the ProfileStore port shape is
// implementable; the real SQLite implementation is asserted in adapter/sqlite.
type fakeProfileStore struct{}

func (fakeProfileStore) Create(context.Context, provider.Profile) error { return nil }
func (fakeProfileStore) Update(context.Context, provider.Profile) error { return nil }
func (fakeProfileStore) Delete(context.Context, string) error           { return nil }
func (fakeProfileStore) Get(context.Context, string) (provider.Profile, error) {
	return provider.Profile{}, nil
}
func (fakeProfileStore) List(context.Context) ([]provider.Profile, error) { return nil, nil }
func (fakeProfileStore) Count(context.Context) (int, error)               { return 0, nil }

var _ provider.ProfileStore = fakeProfileStore{}
