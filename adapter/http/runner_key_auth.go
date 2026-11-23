// adapter/http/runner_key_auth.go
package http

import (
	"net/http"
	"strings"

	"github.com/arbitron/arbitron/domain/runner"
)

// RunnerAuthScheme is the only way a runner presents its key:
// "Authorization: Runner ark_…". Keys never travel in query parameters.
const RunnerAuthScheme = "Runner"

// runnerKeyFromHeader extracts a key presented with the Runner scheme.
func runnerKeyFromHeader(h http.Header) (string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(h.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, RunnerAuthScheme) {
		return "", false
	}
	key := strings.TrimSpace(rest)
	return key, key != ""
}

// presentsRunnerKey reports whether the request carries a runner key in any
// Authorization form. User-auth surfaces refuse such requests outright: a
// runner key authorizes only the runner channel and is never a user identity.
func presentsRunnerKey(r *http.Request) bool {
	if _, ok := runnerKeyFromHeader(r.Header); ok {
		return true
	}
	scheme, rest, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	return ok && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(strings.TrimSpace(rest), runner.RunnerKeyPrefix)
}
