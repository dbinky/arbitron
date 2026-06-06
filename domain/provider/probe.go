package provider

import (
	"context"
	"errors"
)

// ConnectionProbe validates a resolved connection and, where the provider
// exposes a model listing, returns its models. It is a driven port: the CLI
// (and any future caller) depends on this interface; adapters implement it.
//
// Probe authenticates + checks reachability for a resolved binding, returning
// the available models when the provider exposes a listing, or
// ModelsUnsupported=true when it does not (reachability only). A non-nil error
// means validation failed (credentials rejected, endpoint unreachable, or a
// region/config fault); the error never carries the connection's secret.
type ConnectionProbe interface {
	Probe(ctx context.Context, b ResolvedBinding) (ProbeResult, error)
}

// ProbeResult is the outcome of a successful Probe (err == nil).
type ProbeResult struct {
	// Reachable is true when credentials authenticated and the endpoint
	// responded. A successful Probe always sets this true.
	Reachable bool
	// ModelsUnsupported is true when the provider exposes no model listing
	// (e.g. the Claude CLI subscription). Models is then empty by contract.
	ModelsUnsupported bool
	// Models holds the provider's model identifiers when listing is supported.
	// It may be empty even when supported (a reachable provider with no models).
	Models []string
}

// ProbeSelector chooses the concrete ConnectionProbe for a resolved binding,
// dispatching by binding.Provider. It mirrors engine.ChatProviderSelector so
// the CLI depends on this domain port and never imports an adapter package.
type ProbeSelector interface {
	ProbeFor(b ResolvedBinding) (ConnectionProbe, error)
}

// ErrNoProbeForProvider is returned by a ProbeSelector when a binding names a
// provider that has no probe mapping.
var ErrNoProbeForProvider = errors.New("provider: no probe for provider")
