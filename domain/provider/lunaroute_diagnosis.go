package provider

import (
	"errors"
	"strings"
)

// LunaRouteDiagnosis preserves an opaque agent failure alongside a later gateway
// observation. The observation is not evidence of the original failure's cause.
type LunaRouteDiagnosis struct {
	Original    error
	Observation error
	ModelListed bool
	Checked     bool
}

func (d *LunaRouteDiagnosis) Error() string {
	var b strings.Builder
	b.WriteString("lunaroute agent run failed: ")
	if d.Original != nil {
		b.WriteString(RedactLunaRoute(d.Original.Error()))
	}
	switch {
	case !d.Checked:
		b.WriteString("; the gateway could not be checked afterwards, so the cause is unknown")
	case d.Observation != nil:
		b.WriteString("; gateway check after the failure (a current observation, not proof of the original cause): ")
		b.WriteString(RedactLunaRoute(d.Observation.Error()))
	case d.ModelListed:
		b.WriteString("; gateway check after the failure (a current observation, not proof of the original cause): the credential was accepted and the model is listed now, so the cause is unknown")
	default:
		b.WriteString("; gateway check after the failure (a current observation, not proof of the original cause): the cause is unknown")
	}
	return b.String()
}

// Unwrap exposes only the failed mission's own error and the fact that its
// cause is unknown. The later gateway observation is deliberately outside the
// causal chain: errors.Is(d, ErrLunaRouteCredentialsRejected) (or Busy, or
// ModelUnavailable) must not claim that what the gateway says now classifies the
// mission that already failed. Read the observation with errors.As to a
// *LunaRouteDiagnosis and ObservationIs, or its Observation field.
func (d *LunaRouteDiagnosis) Unwrap() []error {
	if d.Original == nil {
		return []error{ErrLunaRouteCauseUnknown}
	}
	return []error{d.Original, ErrLunaRouteCauseUnknown}
}

// ObservationIs reports whether the gateway check made after the failure
// matched target (for example ErrLunaRouteModelUnavailable). It is false when
// the gateway could not be checked or the check produced no classified fault.
func (d *LunaRouteDiagnosis) ObservationIs(target error) bool {
	return d.Observation != nil && errors.Is(d.Observation, target)
}
