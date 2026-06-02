package provider

import (
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ProfileKinder looks up a profile's capability kind by alias with no I/O. The
// StaticProfileResolver satisfies it via its in-memory registry; the checker
// depends on this narrow capability rather than the full ProfileResolver so a
// capability check never triggers a connection-store read.
type ProfileKinder interface {
	Kind(alias string) (provider.ProfileKind, bool)
}

// ResolverChecker adapts a ProfileKinder to workflow.ProfileChecker so workflow
// validation can look up a profile's capability kind by alias without importing
// the provider package.
type ResolverChecker struct {
	k ProfileKinder
}

var _ workflow.ProfileChecker = ResolverChecker{}

// NewResolverChecker wraps a ProfileKinder as a workflow.ProfileChecker.
func NewResolverChecker(k ProfileKinder) ResolverChecker {
	return ResolverChecker{k: k}
}

// ProfileKind returns a profile's kind as a plain string using the zero-I/O
// Kind lookup. An unknown alias returns ok=false. CORRECTNESS: because this no
// longer runs the full connection-backed Resolve chain, a valid profile whose
// connection is absent is reported with its kind (ok=true) and is NOT mislabeled
// UNKNOWN_PROFILE — the missing connection is surfaced by the connection-existence
// check instead.
func (c ResolverChecker) ProfileKind(alias string) (string, bool) {
	kind, ok := c.k.Kind(alias)
	if !ok {
		return "", false
	}
	return string(kind), true
}
