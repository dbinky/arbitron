package provider

import "regexp"

const lunaRouteKeyPrefix = "lr_"

// lunaRouteKeySuffixClass is the regexp character-class body of every byte that
// may follow the lr_ prefix. It is the single source for both the write/reveal
// validator and the fallback redaction pattern, so the set of accepted keys and
// the set of fully redacted keys are equal by construction.
const lunaRouteKeySuffixClass = `A-Za-z0-9._~+/=-`

var (
	lrKeyRE       = regexp.MustCompile(regexp.QuoteMeta(lunaRouteKeyPrefix) + `[` + lunaRouteKeySuffixClass + `]+`)
	lrKeyAcceptRE = regexp.MustCompile(`^` + regexp.QuoteMeta(lunaRouteKeyPrefix) + `[` + lunaRouteKeySuffixClass + `]+$`)
)

// ValidLunaRouteKey reports whether key is shaped like a LunaRoute credential:
// the lr_ prefix followed by a non-empty run of the characters redaction can
// match completely (letters, digits and . _ ~ + / = -). LunaRoute forwards any
// non-lr_ value upstream as a bring-your-own key, so a mis-pasted foreign key
// must never reach it; anything outside the set is refused rather than stored
// where a diagnostic could only partly redact it.
func ValidLunaRouteKey(key string) bool {
	return lrKeyAcceptRE.MatchString(key)
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
