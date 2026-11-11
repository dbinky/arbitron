package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var ErrInvalidSelection = errors.New("provider: invalid profile selection")

// ErrSelectionPortsMissing marks absent version/route validation or following
// candidate ports. A missing port is a wiring fault, never an empty catalog.
var ErrSelectionPortsMissing = errors.New("provider: model selection ports not configured")

// ErrNoEligibleCandidate is returned when no confirmed, usable version exists
// for a following target, so there is no model to start on.
var ErrNoEligibleCandidate = errors.New("provider: no eligible model candidate")

var targetSlug = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// PreparedSelection retains the prospective profile and its selection proof.
// Candidate is required for a following selection needing validation; pinned
// version validation has no candidate.
type PreparedSelection struct {
	Profile   Profile
	Candidate *EligibleCandidate
}

// Clone returns a selection whose profile and proof are owned by the caller.
func (s PreparedSelection) Clone() PreparedSelection {
	s.Profile = s.Profile.Clone()
	if s.Candidate != nil {
		candidate := s.Candidate.Clone()
		s.Candidate = &candidate
	}
	return s
}

// Clone returns a profile whose mutable nested fields are owned by the caller.
func (p Profile) Clone() Profile {
	p.AllowedTools = slices.Clone(p.AllowedTools)
	if p.FollowTarget != nil {
		target := *p.FollowTarget
		p.FollowTarget = &target
	}
	return p
}

// NormalizeSelection defaults old profiles to pinned, validates selection shape,
// and normalizes only target slugs. It does not claim model identity/usability.
func NormalizeSelection(p Profile) (Profile, error) {
	p = p.Clone()
	if p.SelectionMode == "" {
		p.SelectionMode = SelectionPinned
	}
	switch p.SelectionMode {
	case SelectionPinned:
		if p.FollowTarget != nil {
			return Profile{}, fmt.Errorf("%w: pinned profile cannot have a follow target", ErrInvalidSelection)
		}
	case SelectionFollowing:
		if p.FollowTarget == nil {
			return Profile{}, fmt.Errorf("%w: following profile requires provider and tier", ErrInvalidSelection)
		}
		p.FollowTarget.Provider = strings.ToLower(strings.TrimSpace(p.FollowTarget.Provider))
		p.FollowTarget.Tier = strings.ToLower(strings.TrimSpace(p.FollowTarget.Tier))
		if !targetSlug.MatchString(p.FollowTarget.Provider) || !targetSlug.MatchString(p.FollowTarget.Tier) {
			return Profile{}, fmt.Errorf("%w: follow provider and tier must be non-empty slugs", ErrInvalidSelection)
		}
	default:
		return Profile{}, fmt.Errorf("%w: unknown mode %q", ErrInvalidSelection, p.SelectionMode)
	}
	return p, nil
}

// ValidateSelection prepares a prospective selection outside the mutation
// lease. A pinned selection is only version-checked; a following one is proved. The caller must revalidate the returned proof under that
// lease and persist it through SelectionMutationStore, without rediscovery.
func ValidateSelection(ctx context.Context, p Profile, versions VersionResolver, source EligibleCandidateSource, caps LunaRouteCapabilities) (PreparedSelection, error) {
	p, err := NormalizeSelection(p)
	if err != nil {
		return PreparedSelection{}, err
	}
	if p.SelectionMode != SelectionFollowing {
		// Pinned writes need real routes even when no selection ports were
		// injected. They need neither catalog membership nor usability probes.
		if err := checkRouteVersions(ctx, p, versions, source, caps); err != nil {
			return PreparedSelection{}, err
		}
		return PreparedSelection{Profile: p}, nil
	}
	if source == nil {
		return PreparedSelection{}, fmt.Errorf("%w: candidate source", ErrSelectionPortsMissing)
	}
	if versions == nil {
		return PreparedSelection{}, fmt.Errorf("%w: version resolver", ErrSelectionPortsMissing)
	}
	// Inspect the original routes before any catalog selection, even when the
	// model is empty. LunaRoute is never a following-catalog source.
	requests, err := source.VersionRequests(ctx, p.Clone())
	if err != nil {
		return PreparedSelection{}, err
	}
	if len(requests) == 0 {
		return PreparedSelection{}, fmt.Errorf("%w: no prospective routes", ErrExecutionRevisionUnavailable)
	}
	for _, request := range requests {
		if IsLunaRoute(request.RouteProvider) {
			return PreparedSelection{}, checkLunaRouteProfile(caps, p, request)
		}
	}
	if p.Model == "" {
		c, err := source.Newest(ctx, p.Clone())
		if err != nil {
			return PreparedSelection{}, err
		}
		if c.Model == "" {
			return PreparedSelection{}, fmt.Errorf("%w for %s/%s", ErrNoEligibleCandidate, p.FollowTarget.Provider, p.FollowTarget.Tier)
		}
		p.Model = c.Model
	}
	if err := checkRouteVersions(ctx, p, versions, source, caps); err != nil {
		return PreparedSelection{}, err
	}
	candidate, err := source.ValidateStart(ctx, p.Clone())
	if err != nil {
		return PreparedSelection{}, err
	}
	if candidate.Model != p.Model {
		return PreparedSelection{}, fmt.Errorf("%w: candidate differs from prospective model", ErrInvalidSelection)
	}
	if candidate.CanonicalID == "" || candidate.PathRevision == "" || candidate.ExecutionRevision.ID != candidate.PathRevision {
		return PreparedSelection{}, fmt.Errorf("%w: incomplete selection proof", ErrExecutionRevisionUnavailable)
	}
	return (PreparedSelection{Profile: p, Candidate: &candidate}).Clone(), nil
}

// ValidatePinRoutes checks one model against every actual route. Its nonempty
// result is a D8 native routing exception, not immutable-version evidence.
// Only pinned, wholly native Azure or Bedrock route sets may use an exception.
func ValidatePinRoutes(ctx context.Context, p Profile, requests []VersionRequest, versions VersionResolver, caps LunaRouteCapabilities) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p, err := NormalizeSelection(p)
	if err != nil {
		return "", err
	}
	if versions == nil {
		return "", fmt.Errorf("%w: version resolver", ErrSelectionPortsMissing)
	}
	if len(requests) == 0 {
		return "", fmt.Errorf("%w: no prospective routes", ErrExecutionRevisionUnavailable)
	}
	if strings.TrimSpace(p.Model) == "" {
		return "", fmt.Errorf("%w: model is empty; set the profile to a fixed model version", ErrSpecificVersionUnavailable)
	}
	var opaque string
	strict := false
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if request.Model != p.Model {
			return "", fmt.Errorf("%w: route model differs from prospective model", ErrInvalidSelection)
		}
		if IsLunaRoute(request.RouteProvider) {
			if err := checkLunaRouteProfile(caps, p, request); err != nil {
				return "", err
			}
		}
		// Codex agent dispatch authenticates via its subscription, ignoring the
		// profile's connection. That connection cannot grant a D8 exception or
		// impose its native provider's version grammar on the subscription.
		if p.Kind == ProfileKindAgent && request.Backend == "codex" {
			request.RouteProvider = ""
			request.ConnectionRef = ""
		}
		family := Canonicalize(request.RouteProvider)
		native := family == CanonicalAzure || family == CanonicalBedrock
		if native && p.SelectionMode == SelectionFollowing {
			return "", fmt.Errorf("%w: %s routes are pinned-only", ErrInvalidSelection, family)
		}
		if native && request.Backend == "" {
			if strict || opaque != "" && opaque != family {
				return "", fmt.Errorf("%w: incompatible native pin routes", ErrInvalidSelection)
			}
			opaque = family
			continue
		}
		if opaque != "" {
			return "", fmt.Errorf("%w: mixed native and exact pin routes", ErrInvalidSelection)
		}
		strict = true
		version, err := versions.SpecificVersion(ctx, request)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(version) == "" {
			return "", fmt.Errorf("%w: route returned no fixed model version", ErrSpecificVersionUnavailable)
		}
	}
	return opaque, nil
}

// checkRouteVersions validates the prospective profile's full route set without
// catalog reads, allowing only the native pinned-only D8 exception.
func checkRouteVersions(ctx context.Context, p Profile, versions VersionResolver, source EligibleCandidateSource, caps LunaRouteCapabilities) error {
	if versions == nil {
		return fmt.Errorf("%w: version resolver", ErrSelectionPortsMissing)
	}
	if source == nil {
		return fmt.Errorf("%w: route source", ErrSelectionPortsMissing)
	}
	requests, err := source.VersionRequests(ctx, p.Clone())
	if err != nil {
		return err
	}
	_, err = ValidatePinRoutes(ctx, p, requests, versions, caps)
	return err
}

// checkLunaRouteProfile checks the original route before subscription routing
// can discard its provider. Backend intent belongs to the profile, not defaults.
func checkLunaRouteProfile(caps LunaRouteCapabilities, p Profile, request VersionRequest) error {
	if p.SelectionMode == SelectionFollowing {
		return &LunaRouteRefusal{Reason: ErrLunaRoutePinnedOnly, Model: p.Model, Backend: request.Backend,
			Detail: "set selection_mode to pinned with an explicit model; LunaRoute models are never auto-selected"}
	}
	route := LunaRouteRoute{Kind: p.Kind, Model: p.Model, ContextWindow: p.ContextWindow, Effort: p.Effort}
	if p.Kind == ProfileKindAgent {
		if strings.TrimSpace(p.Backend) == "" {
			return &LunaRouteRefusal{Reason: ErrLunaRouteBackendRequired, Model: p.Model,
				Detail: "set backend to claude-code, opencode, codex or pi explicitly; deployment defaults never choose a LunaRoute backend"}
		}
		route.Backend = request.Backend
	}
	return CheckLunaRouteRoute(caps, route)
}

// selectionChanged reports whether an edit alters anything that decides which
// model a following profile executes: mode, target, model, route, axes or tools.
// Edits that leave these alone keep their previously validated selection and
// need no fresh discovery.
func selectionChanged(old, next Profile) bool {
	if old.SelectionMode == "" {
		old.SelectionMode = SelectionPinned
	}
	if next.SelectionMode == "" {
		next.SelectionMode = SelectionPinned
	}
	if (old.FollowTarget == nil) != (next.FollowTarget == nil) ||
		old.FollowTarget != nil && *old.FollowTarget != *next.FollowTarget {
		return true
	}
	return old.SelectionMode != next.SelectionMode || old.Model != next.Model ||
		old.Kind != next.Kind || old.ConnectionRef != next.ConnectionRef || old.Backend != next.Backend ||
		old.ContextWindow != next.ContextWindow || old.Effort != next.Effort ||
		!slices.Equal(old.AllowedTools, next.AllowedTools)
}
