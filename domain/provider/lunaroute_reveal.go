package provider

import "context"

// GuardLunaRouteReveal wraps a LunaRoute binding's lazy RevealKey so that every
// reveal first re-checks the hosted endpoint, then fails closed on a revealed
// value that is not a valid lr_ key. It reveals nothing itself; construction
// stays lazy. Non-LunaRoute bindings are returned unchanged.
func GuardLunaRouteReveal(b ResolvedBinding) ResolvedBinding {
	if !IsLunaRoute(b.Provider) {
		return b
	}
	inner := b.RevealKey
	baseURL := b.BaseURL
	b.RevealKey = func(ctx context.Context) (string, error) {
		if _, err := NormalizeLunaRouteBaseURL(baseURL); err != nil {
			return "", err
		}
		if inner == nil {
			return "", ErrLunaRouteStoredCredential
		}
		key, err := inner(ctx)
		if err != nil {
			return "", LocalLunaRouteError("reveal", err, key)
		}
		if !ValidLunaRouteKey(key) {
			return "", ErrLunaRouteStoredCredential
		}
		return key, nil
	}
	return b
}
