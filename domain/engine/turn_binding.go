package engine

import "github.com/arbitron/arbitron/domain/provider"

// AcceptedTurnBinding is the credential-free execution policy accepted for a
// session. Credentials and arbitrary provider metadata must never enter it.
type AcceptedTurnBinding struct {
	Kind                                    provider.ProfileKind
	Provider, Model, Backend, ConnectionRef string
	BackendExplicit                         bool
	AllowedTools                            []string
	ContextWindow                           provider.ContextWindow
	Effort                                  provider.Effort
	Pinned                                  bool
	OpaquePinRoute                          string
	RequireExecutedModel                    bool
	RouteConfigHash                         string
}
