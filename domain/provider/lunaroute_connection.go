package provider

import (
	"strings"
	"unicode"
)

const lunaRouteKeyPrefix = "lr_"

// ValidLunaRouteKey reports whether key is shaped like a LunaRoute credential:
// the lr_ prefix, a non-empty suffix, and no whitespace or control characters.
// LunaRoute forwards any non-lr_ value upstream as a bring-your-own key, so a
// mis-pasted foreign key must never reach it.
func ValidLunaRouteKey(key string) bool {
	if !strings.HasPrefix(key, lunaRouteKeyPrefix) || len(key) == len(lunaRouteKeyPrefix) {
		return false
	}
	return strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func checkLunaRouteKey(key string) error {
	if key == "" {
		return ErrLunaRouteKeyRequired
	}
	if !ValidLunaRouteKey(key) {
		return ErrLunaRouteKeyPrefix
	}
	return nil
}

func normalizeLunaRouteConnection(c Connection) (Connection, error) {
	root, err := NormalizeLunaRouteBaseURL(c.BaseURL)
	if err != nil {
		return Connection{}, err
	}
	c.Provider = CanonicalLunaRoute
	c.BaseURL = root
	return c, nil
}

// PrepareLunaRouteCreate validates a LunaRoute create and returns the
// connection to persist (canonical provider, hosted root). Non-LunaRoute
// connections are returned unchanged.
func PrepareLunaRouteCreate(c Connection, apiKey string) (Connection, error) {
	if !IsLunaRoute(c.Provider) {
		return c, nil
	}
	if err := checkLunaRouteKey(apiKey); err != nil {
		return Connection{}, err
	}
	return normalizeLunaRouteConnection(c)
}

// PrepareLunaRouteUpdate validates a LunaRoute update. Keeping the stored key
// is allowed only when staying with the same side of the LunaRoute boundary;
// switching to or away from LunaRoute requires a new, non-empty key.
// storedProvider is the provider read from storage before the write.
func PrepareLunaRouteUpdate(c Connection, apiKey *string, storedProvider string) (Connection, error) {
	if !IsLunaRoute(c.Provider) {
		if IsLunaRoute(storedProvider) && (apiKey == nil || *apiKey == "") {
			return Connection{}, ErrLunaRouteDestinationKeyRequired
		}
		return c, nil
	}
	if apiKey == nil {
		if !IsLunaRoute(storedProvider) {
			return Connection{}, ErrLunaRouteKeyRequired
		}
	} else if err := checkLunaRouteKey(*apiKey); err != nil {
		return Connection{}, err
	}
	return normalizeLunaRouteConnection(c)
}
