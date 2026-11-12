package provider

import (
	"errors"
	"fmt"
	"strings"
)

// Connection-write sentinels. None carries submitted key material.
var (
	ErrLunaRouteKeyPrefix              = errors.New(`provider: LunaRoute keys start with "lr_" followed by the key; keys from other providers are rejected, never forwarded through LunaRoute`)
	ErrLunaRouteKeyRequired            = errors.New("provider: a LunaRoute connection needs its own lr_ key; supply api_key when creating it or switching an existing connection to lunaroute")
	ErrLunaRouteDestinationKeyRequired = errors.New("provider: switching away from LunaRoute requires a new api_key for the destination provider")
)

// ErrLunaRouteStoredCredential fails a launch with an invalid stored key.
var ErrLunaRouteStoredCredential = errors.New("provider: the stored LunaRoute credential is not an lr_ key; update the connection with a LunaRoute key")

// Profile-save and launch reasons retain the existing refusal classification.
var (
	ErrLunaRouteFamilyUnresolved   = fmt.Errorf("%w: LunaRoute model family is unresolved", ErrInvalidSelection)
	ErrLunaRouteIncompatible       = fmt.Errorf("%w: LunaRoute model is known to be incompatible with this selection", ErrInvalidSelection)
	ErrLunaRouteAxisUnsupported    = fmt.Errorf("%w: LunaRoute setting is not supported for this model and backend", ErrInvalidSelection)
	ErrLunaRouteLimitsUnknown      = fmt.Errorf("%w: LunaRoute model limits are unknown", ErrInvalidSelection)
	ErrLunaRouteBackendUnsupported = fmt.Errorf("%w: LunaRoute is not yet supported on this agent backend", ErrInvalidSelection)
	ErrLunaRouteBackendRequired    = fmt.Errorf("%w: LunaRoute agent profiles must name their backend explicitly", ErrInvalidSelection)
	ErrLunaRoutePinnedOnly         = fmt.Errorf("%w: LunaRoute selections are pinned-only", ErrInvalidSelection)
	ErrLunaRouteSettingConflict    = fmt.Errorf("%w: LunaRoute request setting conflicts with the model's known limits", ErrInvalidSelection)
)

// Gateway outcomes are refined only by documented codes or authoritative
// availability evidence, never by upstream prose.
var (
	ErrLunaRouteCredentialsRejected = errors.New("LunaRoute rejected the credential; update the connection with a valid lr_ key")
	ErrLunaRouteModelUnavailable    = errors.New("the selected model is not available on this LunaRoute connection; run connection_models and choose a listed model")
	ErrLunaRouteBusy                = errors.New("LunaRoute is busy (all lanes in use); retry later")
	ErrLunaRouteServiceFailure      = errors.New("LunaRoute service failure; retry later")
	ErrLunaRouteCauseUnknown        = errors.New("LunaRoute request failed and the cause is unknown")
	ErrLunaRouteRequestRefused      = errors.New("LunaRoute refused this request for account or pricing reasons (spend cap reached or model not priced); it will not succeed on retry")
)

// ErrLunaRouteLocalFailure marks reveal/config/filesystem failures that must
// not trigger online gateway diagnosis.
var ErrLunaRouteLocalFailure = errors.New("provider: LunaRoute local preparation failed")

// LunaRouteGatewaySentinels returns the six outcomes in a fixed order.
func LunaRouteGatewaySentinels() []error {
	return []error{ErrLunaRouteCredentialsRejected, ErrLunaRouteModelUnavailable, ErrLunaRouteBusy, ErrLunaRouteServiceFailure, ErrLunaRouteCauseUnknown, ErrLunaRouteRequestRefused}
}

// LunaRouteRefusal describes an invalid selection, without connection names or
// credentials. Detail names the corrective action.
type LunaRouteRefusal struct {
	Reason  error
	Model   string
	Backend string
	Detail  string
}

func (e *LunaRouteRefusal) Error() string {
	var b strings.Builder
	b.WriteString(e.Reason.Error())
	fmt.Fprintf(&b, " (model %q", e.Model)
	if e.Backend != "" {
		fmt.Fprintf(&b, ", backend %q", e.Backend)
	}
	b.WriteString(")")
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	return b.String()
}

func (e *LunaRouteRefusal) Unwrap() error { return e.Reason }

// sanitizedLunaRouteError changes only the displayed text, not Is/As identity.
type sanitizedLunaRouteError struct {
	text  string
	cause error
}

func (e *sanitizedLunaRouteError) Error() string { return e.text }
func (e *sanitizedLunaRouteError) Unwrap() error { return e.cause }

// SanitizeLunaRouteError bounds and redacts diagnostics before an error sink.
func SanitizeLunaRouteError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return &sanitizedLunaRouteError{text: RedactLunaRoute(err.Error(), secrets...), cause: err}
}

// LocalLunaRouteError safely marks preparation errors while retaining their
// original identity. A successful operation stays nil.
func LocalLunaRouteError(stage string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return SanitizeLunaRouteError(fmt.Errorf("lunaroute %s: %w: %w", stage, ErrLunaRouteLocalFailure, err), secrets...)
}
