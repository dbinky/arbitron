package provider

import (
	"context"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ConnExistsStore is the minimal slice of provider.ConnectionStore the
// ConnChecker needs: a metadata Get used only to test existence. Accepting this
// narrow interface (rather than the full store) keeps the checker decoupled and
// trivially stubbable.
type ConnExistsStore interface {
	Get(ctx context.Context, name string) (provider.Connection, error)
}

// ConnChecker adapts the profile registry plus a ConnectionStore to
// workflow.ConnectionChecker so workflow validation can confirm that a profile's
// non-empty ConnectionRef names an existing connection — without the domain
// importing the provider-persistence layer.
type ConnChecker struct {
	refByAlias map[string]string
	store      ConnExistsStore
}

var _ workflow.ConnectionChecker = (*ConnChecker)(nil)

// NewConnChecker builds the alias -> ConnectionRef map from the loaded profiles
// and captures the store used for existence checks. The store may be nil when
// no connections are configured (Claude-only deployment); ConnectionExists then
// reports every name as absent.
func NewConnChecker(profiles []provider.Profile, store ConnExistsStore) *ConnChecker {
	refByAlias := make(map[string]string, len(profiles))
	for _, p := range profiles {
		refByAlias[p.Alias] = p.ConnectionRef
	}
	return &ConnChecker{refByAlias: refByAlias, store: store}
}

// ProfileConnectionRef returns the ConnectionRef for an alias and whether the
// alias is known. An empty ref on a known alias is the claude-cli no-key path.
func (c *ConnChecker) ProfileConnectionRef(alias string) (string, bool) {
	ref, ok := c.refByAlias[alias]
	return ref, ok
}

// ConnectionExists reports whether a connection with the given name exists. A
// nil store (no connections configured) reports false; any store error
// (including ErrConnectionNotFound) reports false so a genuine miss is surfaced
// by validation rather than masked as present.
func (c *ConnChecker) ConnectionExists(name string) bool {
	if c.store == nil {
		return false
	}
	_, err := c.store.Get(context.Background(), name)
	return err == nil
}
