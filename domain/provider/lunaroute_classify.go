package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// LunaRouteOperation identifies the gateway call. Failed listings, including
// bare 404s, cannot establish that a selected model is unavailable.
type LunaRouteOperation string

const (
	LunaRouteListModels LunaRouteOperation = "list models"
	LunaRouteChat       LunaRouteOperation = "chat"
	LunaRouteAgent      LunaRouteOperation = "agent run"
)

const lunaRouteSnippetMax = 256

// LR-04 (docs/designs/2026-11-10-lunaroute-provider-decisions.md). Availability
// is the re-list's job: MODEL_NOT_FOUND and *_busy are deliberately absent.
var lunaRouteDocumentedCodes = map[string]error{
	"INVALID_API_KEY":                   ErrLunaRouteCredentialsRejected,
	"CONCURRENT_REQUEST_LIMIT_EXCEEDED": ErrLunaRouteBusy,
	"INFERENCE_UNAVAILABLE":             ErrLunaRouteServiceFailure,
	"NO_HEALTHY_PROVIDER":               ErrLunaRouteServiceFailure,
	"PROVIDER_KEY_UNAVAILABLE":          ErrLunaRouteServiceFailure,
	"BYOK_SPEND_GATE_UNAVAILABLE":       ErrLunaRouteServiceFailure,
	"BYOK_SPEND_CAP_REACHED":            ErrLunaRouteRequestRefused,
	"MODEL_UNPRICED":                    ErrLunaRouteRequestRefused,
}

// Key by operation even though all operations currently document the same set.
var lunaRouteVerifiedCodes = map[LunaRouteOperation]map[string]error{
	LunaRouteListModels: lunaRouteDocumentedCodes,
	LunaRouteChat:       lunaRouteDocumentedCodes,
	LunaRouteAgent:      lunaRouteDocumentedCodes,
}

// LunaRouteOutcome contains transient classification inputs. Body must never
// become diagnostic text, including when a response read was cut short.
type LunaRouteOutcome struct {
	Operation  LunaRouteOperation
	Status     int
	Code       string
	RetryAfter string
	Transport  error
	Malformed  error
	Body       string
	Secrets    []string
}

// LunaRouteFailure displays safe diagnostics while retaining original error
// identity through Unwrap. Snippet contains only an allowlisted code.
type LunaRouteFailure struct {
	Kind       error
	Operation  LunaRouteOperation
	Status     int
	Snippet    string
	RetryAfter string
	cause      error
	causeText  string
}

func (f *LunaRouteFailure) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "lunaroute %s: %s", f.Operation, f.Kind.Error())
	if f.Status != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", f.Status)
	}
	if f.Snippet != "" {
		b.WriteString(": " + f.Snippet)
	}
	if f.RetryAfter != "" {
		b.WriteString("; retry after " + f.RetryAfter)
	}
	if f.causeText != "" {
		b.WriteString(": " + f.causeText)
	}
	return b.String()
}

func (f *LunaRouteFailure) Unwrap() []error {
	if f.cause == nil {
		return []error{f.Kind}
	}
	return []error{f.Kind, f.cause}
}

var (
	lrKeyRE     = regexp.MustCompile(`lr_[A-Za-z0-9._~+/=-]+`)
	bearerRE    = regexp.MustCompile(`(?i)(bearer\s+)[^\s"',]+`)
	apiHeaderRE = regexp.MustCompile(`(?i)((?:x-api-key|lunaroute-api-key)\s*[:=]\s*)[^\s"',]+`)
)

// RedactLunaRoute removes revealed secrets and auth material before control
// scrubbing and truncating to 256 bytes on a UTF-8 boundary. It accepts complete
// local diagnostics; already-cut gateway bodies must be discarded instead.
func RedactLunaRoute(s string, secrets ...string) string {
	// A single longest-first replacement avoids leaking overlapping secret
	// suffixes or reprocessing the replacement marker as another secret.
	ordered := slices.Clone(secrets)
	slices.SortFunc(ordered, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, len(ordered)*2)
	for _, secret := range ordered {
		if secret != "" {
			pairs = append(pairs, secret, "[REDACTED]")
		}
	}
	if len(pairs) != 0 {
		s = strings.NewReplacer(pairs...).Replace(s)
	}
	s = lrKeyRE.ReplaceAllString(s, "lr_[REDACTED]")
	s = bearerRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = apiHeaderRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) <= lunaRouteSnippetMax {
		return s
	}
	cut := lunaRouteSnippetMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// LunaRouteErrorCode decodes only the LR-04 code field, never prose or type.
// Nested error objects take precedence; contradictory string codes are unknown.
// Unknown codes are returned for status fallback, not treated as evidence.
func LunaRouteErrorCode(op LunaRouteOperation, body string) string {
	if lunaRouteVerifiedCodes[op] == nil {
		return ""
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &envelope) != nil {
		return ""
	}
	var top string
	_ = json.Unmarshal(envelope["code"], &top)
	if raw, exists := envelope["error"]; exists {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			return ""
		}
		var code string
		if json.Unmarshal(nested["code"], &code) != nil {
			return ""
		}
		if top != "" && code != "" && top != code {
			return ""
		}
		return code
	}
	return top
}

// NormalizeLunaRouteRetryAfter accepts unsigned delta-seconds or an RFC1123
// HTTP date. No arbitrary header text is allowed into retry guidance.
func NormalizeLunaRouteRetryAfter(raw string) string {
	if strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	digits := true
	for _, r := range raw {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return ""
		}
		return strconv.FormatUint(seconds, 10)
	}
	// HTTP dates require GMT, not arbitrary RFC1123 zone abbreviations.
	if !strings.HasSuffix(raw, " GMT") {
		return ""
	}
	date, err := time.Parse(http.TimeFormat, raw)
	if err != nil {
		return ""
	}
	return date.UTC().Format(http.TimeFormat)
}

// ClassifyLunaRoute maps a failed gateway outcome to exactly one sentinel.
// Transport/decode errors precede codes, and documented codes precede status.
func ClassifyLunaRoute(o LunaRouteOutcome) *LunaRouteFailure {
	f := &LunaRouteFailure{Operation: o.Operation, Status: o.Status}
	switch {
	case o.Transport != nil:
		f.cause = o.Transport
		f.causeText = RedactLunaRoute(o.Transport.Error(), o.Secrets...)
		if errors.Is(o.Transport, context.Canceled) {
			f.Kind = ErrLunaRouteCauseUnknown
		} else {
			f.Kind = ErrLunaRouteServiceFailure
		}
	case o.Malformed != nil:
		f.cause = o.Malformed
		f.causeText = "malformed response: " + RedactLunaRoute(o.Malformed.Error(), o.Secrets...)
		f.Kind = ErrLunaRouteServiceFailure
	case o.Code != "" && lunaRouteVerifiedCodes[o.Operation][o.Code] != nil:
		f.Kind = lunaRouteVerifiedCodes[o.Operation][o.Code]
		f.Snippet = "code=" + o.Code
	case o.Status == http.StatusUnauthorized:
		f.Kind = ErrLunaRouteCredentialsRejected
	case o.Status == http.StatusTooManyRequests:
		f.Kind = ErrLunaRouteBusy
	case o.Status >= 500 && o.Status <= 599:
		f.Kind = ErrLunaRouteServiceFailure
	default:
		f.Kind = ErrLunaRouteCauseUnknown
	}
	if f.Kind == ErrLunaRouteBusy {
		f.RetryAfter = NormalizeLunaRouteRetryAfter(o.RetryAfter)
	}
	return f
}
