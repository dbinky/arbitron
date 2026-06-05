// Package awssig is a stdlib-only leaf package providing a hand-rolled AWS
// Signature Version 4 request signer (with an injectable clock for deterministic
// signatures) and sealed-credential parse/build helpers. It imports no other
// arbitron package so it can be reused by adapter/bedrock, adapter/opencode, and
// adapter/probe without an import cycle.
package awssig

import (
	"encoding/json"
	"errors"
	"strings"
)

// Credential is the parsed AWS secret material the signer uses. The
// access-key-id is NOT here (it travels as a non-secret identifier); only true
// secrets are carried.
type Credential struct {
	SecretAccessKey string // the sealed secret-access-key (required)
	SessionToken    string // optional STS session token; "" when absent
}

// ErrMissingSecret is returned when the sealed payload yields no secret-access-key.
var ErrMissingSecret = errors.New("awssig: sealed credential payload has no secret-access-key")

// jsonComposite is the sealed JSON shape when a session token is present.
type jsonComposite struct {
	Secret       string `json:"secret"`
	SessionToken string `json:"session_token"`
}

// ParseCredential interprets the revealed sealed string. It tolerates BOTH forms:
//   - a JSON composite object {"secret":"...","session_token":"..."} (session_token optional)
//   - a bare secret-access-key string (no session token)
//
// A leading '{' (after trimming whitespace) selects the JSON form; anything else
// is treated as the bare secret. An empty/whitespace-only payload, or a JSON
// object whose "secret" is empty, returns ErrMissingSecret.
func ParseCredential(sealed string) (Credential, error) {
	trimmed := strings.TrimSpace(sealed)
	if trimmed == "" {
		return Credential{}, ErrMissingSecret
	}
	if strings.HasPrefix(trimmed, "{") {
		var composite jsonComposite
		if err := json.Unmarshal([]byte(trimmed), &composite); err != nil {
			// SECURITY: do not echo the payload; only a static reason.
			return Credential{}, errors.New("awssig: malformed JSON credential payload")
		}
		if composite.Secret == "" {
			return Credential{}, ErrMissingSecret
		}
		return Credential{SecretAccessKey: composite.Secret, SessionToken: composite.SessionToken}, nil
	}
	// Bare secret-access-key form (preserved verbatim, no session token).
	return Credential{SecretAccessKey: sealed}, nil
}

// BuildCredential is the inverse seal helper used by the connection write-path:
// when sessionToken == "" it returns the bare secret string; otherwise it returns
// the JSON composite {"secret":"...","session_token":"..."}. ParseCredential
// round-trips its output.
func BuildCredential(secret, sessionToken string) string {
	if sessionToken == "" {
		return secret
	}
	b, _ := json.Marshal(jsonComposite{Secret: secret, SessionToken: sessionToken})
	return string(b)
}
