package provider

import (
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ResolverChecker adapts a provider.ProfileResolver to workflow.ProfileChecker
// so workflow validation can look up a profile's capability kind by alias
// without importing the provider package.
type ResolverChecker struct {
	r provider.ProfileResolver
}

var _ workflow.ProfileChecker = ResolverChecker{}

// NewResolverChecker wraps a resolver as a workflow.ProfileChecker.
func NewResolverChecker(r provider.ProfileResolver) ResolverChecker {
	return ResolverChecker{r: r}
}

// ProfileKind resolves the alias and returns its kind as a plain string. An
// unresolvable alias (including ErrUnknownProfile) returns ok=false.
func (c ResolverChecker) ProfileKind(alias string) (string, bool) {
	binding, err := c.r.Resolve(alias)
	if err != nil {
		return "", false
	}
	return string(binding.Kind), true
}
