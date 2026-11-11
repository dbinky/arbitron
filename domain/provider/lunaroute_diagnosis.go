package provider

import "strings"

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

func (d *LunaRouteDiagnosis) Unwrap() []error {
	observation := d.Observation
	if observation == nil {
		observation = ErrLunaRouteCauseUnknown
	}
	return []error{d.Original, observation}
}
