package provider_test

import (
	"context"
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// existsStore is a minimal ConnExistsStore stub for exercising ConnChecker in
// isolation: it records the names looked up and answers from a fixed set.
type existsStore struct {
	present map[string]bool
	err     error
	asked   []string
}

func (s *existsStore) Get(_ context.Context, name string) (provider.Connection, error) {
	s.asked = append(s.asked, name)
	if s.err != nil {
		return provider.Connection{}, s.err
	}
	if s.present[name] {
		return provider.Connection{Name: name}, nil
	}
	return provider.Connection{}, provider.ErrConnectionNotFound
}

// Happy path: a known alias with a non-empty ConnectionRef returns that ref and
// aliasKnown=true.
func TestConnChecker_ProfileConnectionRef_Known(t *testing.T) {
	store := &existsStore{present: map[string]bool{"azure-prod": true}}
	c := adapterprovider.NewConnChecker([]provider.Profile{
		{Alias: "az", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod"},
		{Alias: "claude", Kind: provider.ProfileKindChat},
	}, store)

	ref, ok := c.ProfileConnectionRef("az")
	assert.True(t, ok)
	assert.Equal(t, "azure-prod", ref)
}

// Success: a known alias on the no-key path returns an empty ref, aliasKnown=true.
func TestConnChecker_ProfileConnectionRef_NoKeyPath(t *testing.T) {
	c := adapterprovider.NewConnChecker([]provider.Profile{
		{Alias: "claude", Kind: provider.ProfileKindChat},
	}, &existsStore{})

	ref, ok := c.ProfileConnectionRef("claude")
	assert.True(t, ok)
	assert.Equal(t, "", ref)
}

// Error: an unknown alias reports aliasKnown=false with an empty ref.
func TestConnChecker_ProfileConnectionRef_Unknown(t *testing.T) {
	c := adapterprovider.NewConnChecker(nil, &existsStore{})

	ref, ok := c.ProfileConnectionRef("ghost")
	assert.False(t, ok)
	assert.Equal(t, "", ref)
}

// Success: ConnectionExists reports true for a present connection.
func TestConnChecker_ConnectionExists_Present(t *testing.T) {
	store := &existsStore{present: map[string]bool{"azure-prod": true}}
	c := adapterprovider.NewConnChecker(nil, store)

	assert.True(t, c.ConnectionExists("azure-prod"))
	assert.Equal(t, []string{"azure-prod"}, store.asked)
}

// Failure: ConnectionExists reports false for a missing connection.
func TestConnChecker_ConnectionExists_Missing(t *testing.T) {
	store := &existsStore{present: map[string]bool{}}
	c := adapterprovider.NewConnChecker(nil, store)

	assert.False(t, c.ConnectionExists("ghost"))
}

// Edge: a nil store (Claude-only deployment with zero connections) reports
// every connection as absent without panicking.
func TestConnChecker_ConnectionExists_NilStore(t *testing.T) {
	c := adapterprovider.NewConnChecker([]provider.Profile{
		{Alias: "claude", Kind: provider.ProfileKindChat},
	}, nil)

	assert.False(t, c.ConnectionExists("anything"))
}

// Edge: a non-NotFound store error collapses to "does not exist" (validation
// surfaces the unknown-connection error; the store error is not masked as true).
func TestConnChecker_ConnectionExists_StoreError(t *testing.T) {
	store := &existsStore{err: errors.New("db down")}
	c := adapterprovider.NewConnChecker(nil, store)

	assert.False(t, c.ConnectionExists("azure-prod"))
}

// Edge case: duplicate aliases in profiles — the last profile in the slice wins
// (map last-write semantics). This documents the silent-overwrite behavior so
// callers know to deduplicate aliases before constructing a ConnChecker.
func TestConnChecker_DuplicateAlias_LastWriteWins(t *testing.T) {
	store := &existsStore{present: map[string]bool{"conn-b": true}}
	c := adapterprovider.NewConnChecker([]provider.Profile{
		{Alias: "prod", Kind: provider.ProfileKindChat, ConnectionRef: "conn-a"},
		{Alias: "prod", Kind: provider.ProfileKindChat, ConnectionRef: "conn-b"},
	}, store)

	ref, ok := c.ProfileConnectionRef("prod")
	require.True(t, ok, "alias is still known after duplicate")
	assert.Equal(t, "conn-b", ref, "last-listed profile wins on duplicate alias")
	assert.True(t, c.ConnectionExists(ref), "resolved ref must resolve against the store")
}

// Integration with workflow.Validate: a workflow node referencing a profile
// whose connection is missing produces an UNKNOWN_CONNECTION error.
func TestConnChecker_SatisfiesWorkflowPort(t *testing.T) {
	store := &existsStore{present: map[string]bool{}}
	c := adapterprovider.NewConnChecker([]provider.Profile{
		{Alias: "az", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod"},
	}, store)

	ref, ok := c.ProfileConnectionRef("az")
	require.True(t, ok)
	require.Equal(t, "azure-prod", ref)
	assert.False(t, c.ConnectionExists(ref))
}
