package provider

// ProfilePatch is a sparse update to a Profile: each field is a pointer where nil
// means "absent — leave the stored value unchanged" and a non-nil value means
// "set this field (including an explicit clear to the empty value)". It is the
// merge-on-update contract that distinguishes "the caller did not touch this
// field" from "the caller cleared this field" — the distinction the MCP/REST
// boundary captures with `_, present := args[k]`.
//
// The merge lives in the domain (ProfileService.UpdatePatch applies it) so every
// surface — MCP today, REST tomorrow — inherits the same safe partial-edit
// semantics rather than each re-implementing a full-replace footgun.
type ProfilePatch struct {
	SelectionMode *string
	// nil omits the target; a pointer to nil explicitly clears it.
	FollowTarget **FollowTarget
	// ExpectedRevision, when set, is the revision the caller last read; the
	// patch is refused with ErrAdvanceConflict if the stored row has moved on.
	ExpectedRevision *int64
	Kind             *string
	ConnectionRef    *string
	Model            *string
	Backend          *string
	AllowedTools     *[]string
	// ContextWindow / Effort are the raw axis strings as received at the boundary;
	// they are parsed (and a malformed value rejected) when the patch is applied,
	// keeping the parse in the same place full writes do it.
	ContextWindow *string
	Effort        *string
}

// apply overlays the present (non-nil) patch fields onto base and returns the
// merged profile. The alias is never patchable — it is the identity key — so it
// is carried from base verbatim. A malformed axis string surfaces
// ErrInvalidContextWindow / ErrInvalidEffort and aborts the merge.
func (p ProfilePatch) apply(base Profile) (Profile, error) {
	out := base.Clone()
	if p.SelectionMode != nil {
		out.SelectionMode = SelectionMode(*p.SelectionMode)
	}
	if p.FollowTarget != nil {
		out.FollowTarget = *p.FollowTarget
	}
	if p.Kind != nil {
		out.Kind = ProfileKind(*p.Kind)
	}
	if p.ConnectionRef != nil {
		out.ConnectionRef = *p.ConnectionRef
	}
	if p.Model != nil {
		out.Model = *p.Model
	}
	if p.Backend != nil {
		out.Backend = *p.Backend
	}
	if p.AllowedTools != nil {
		out.AllowedTools = *p.AllowedTools
	}
	if p.ContextWindow != nil {
		cw, err := ParseContextWindow(*p.ContextWindow)
		if err != nil {
			return Profile{}, err
		}
		out.ContextWindow = cw
	}
	if p.Effort != nil {
		eff, err := ParseEffort(*p.Effort)
		if err != nil {
			return Profile{}, err
		}
		out.Effort = eff
	}
	return NormalizeSelection(out)
}
