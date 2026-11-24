package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

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

// TurnBindingRestorer rebuilds an accepted policy with a fresh lazy key.
type TurnBindingRestorer interface {
	RestoreTurnBinding(ctx context.Context, accepted AcceptedTurnBinding) (provider.ResolvedBinding, error)
}

// RouteConfigHash fingerprints a canonical route without retaining its credentials.
func RouteConfigHash(providerName, baseURL string, extras map[string]string) string {
	endpoint := strings.TrimSpace(baseURL)
	if u, err := url.Parse(endpoint); err == nil {
		u.RawQuery, u.Fragment, u.User = "", "", nil
		endpoint = u.String()
	}
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "provider=%s\nendpoint=%s\n", provider.Canonicalize(providerName), endpoint)
	for _, k := range keys {
		fmt.Fprintf(h, "extra:%q=%q\n", k, extras[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ErrTurnBindingChanged rejects an unusable accepted binding or a changed route.
var ErrTurnBindingChanged = errors.New("engine: accepted turn binding cannot be restored as accepted")

// CaptureAcceptedTurnBinding freezes the effective accepted policy without keys or endpoints.
func CaptureAcceptedTurnBinding(b provider.ResolvedBinding) (AcceptedTurnBinding, error) {
	switch {
	case b.Kind == "":
		return AcceptedTurnBinding{}, fmt.Errorf("%w: missing kind", ErrTurnBindingChanged)
	case b.Provider == "":
		return AcceptedTurnBinding{}, fmt.Errorf("%w: missing provider", ErrTurnBindingChanged)
	case b.Model == "":
		return AcceptedTurnBinding{}, fmt.Errorf("%w: missing model", ErrTurnBindingChanged)
	}
	return AcceptedTurnBinding{
		Kind: b.Kind, Provider: b.Provider, Model: b.Model, Backend: b.Backend, ConnectionRef: b.ConnectionRef,
		BackendExplicit: b.BackendExplicit, AllowedTools: append([]string(nil), b.AllowedTools...),
		ContextWindow: b.ContextWindow, Effort: b.Effort, Pinned: b.Pinned, OpaquePinRoute: b.OpaquePinRoute,
		RequireExecutedModel: b.RequiresModelEvidence(), RouteConfigHash: RouteConfigHash(b.Provider, b.BaseURL, b.Extras),
	}, nil
}
