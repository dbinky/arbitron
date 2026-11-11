package provider

import (
	"context"
	"errors"
	"fmt"
)

// Effective is the runtime-effective view of a profile's axes echoed
// back on a write. It is what the emitter will actually emit at launch: a
// requested effort the backend/model cannot honor floors to its ceiling, so the
// caller sees the value that will take effect rather than the value requested.
// LunaRoute routes instead preserve validated requested axes without flooring.
type Effective struct {
	ContextWindow ContextWindow
	Effort        Effort
}

// ProfileService is the admin-facing editing surface for profiles. Authz (an
// admin gate on mutation, any authenticated identity on reads), capability
// validation, and the post-write recache all live HERE in the domain so the MCP
// adapter (and any future REST surface) share one rule and never drift. It
// mirrors instance.UserService.
type ProfileService struct {
	store    ProfileStore
	rules    CapabilityRules
	reloader Reloader
	versions VersionResolver
	source   EligibleCandidateSource

	advance   AdvanceStore
	publisher AdvancementPublisher
	journal   PublicationJournal
	gate      *ExecutionGate
}

// NewProfileService wires the persistence port, the capability ruleset, and the
// recache hook (the store-bound ProfileCache). The reloader is invoked after a
// successful write so live edits take effect without a restart.
func NewProfileService(store ProfileStore, rules CapabilityRules, reloader Reloader) *ProfileService {
	return &ProfileService{store: store, rules: rules, reloader: reloader}
}

// SetModelSelection injects the fixed-version resolver and confirmed-candidate
// source for following selection preparation and read-only proof revalidation.
func (s *ProfileService) SetModelSelection(v VersionResolver, src EligibleCandidateSource) {
	s.versions, s.source = v, src
}

// ListInternal is the trusted, unauthenticated reader for in-process refresh
// code. It is never registered on a public surface.
func (s *ProfileService) ListInternal(ctx context.Context) ([]Profile, error) {
	return s.store.List(ctx)
}

// List returns all profiles. Read-open: any authenticated identity (profiles are
// not secret per spec §6); a missing identity is rejected.
func (s *ProfileService) List(ctx context.Context) ([]Profile, error) {
	if _, err := requireAuthed(ctx); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

// Get returns one profile by alias. Read-open like List.
func (s *ProfileService) Get(ctx context.Context, alias string) (Profile, error) {
	if _, err := requireAuthed(ctx); err != nil {
		return Profile{}, err
	}
	return s.store.Get(ctx, alias)
}

// Create persists a new profile (admin-only), after capability validation, then
// recaches. It returns the stored profile plus the floored Effective values.
func (s *ProfileService) Create(ctx context.Context, p Profile) (Profile, Effective, error) {
	return s.write(ctx, p, func(ctx context.Context, p Profile) (Profile, error) {
		if err := s.store.Create(ctx, p); err != nil {
			return Profile{}, err
		}
		// The store owns the revision; report the stored row, not the request.
		return s.store.Get(ctx, p.Alias)
	}, nil)
}

// Update overwrites an existing profile by alias (admin-only), with the same
// validate-then-recache flow as Create. It is a FULL replace; UpdatePatch is the
// merge-on-update path that preserves omitted fields.
func (s *ProfileService) Update(ctx context.Context, p Profile) (Profile, Effective, error) {
	if err := requireAdmin(ctx); err != nil {
		return Profile{}, Effective{}, err
	}
	prior, err := s.store.Get(ctx, p.Alias)
	if err != nil {
		return Profile{}, Effective{}, err
	}
	return s.write(ctx, p, s.updateExpected(prior), &prior)
}

// UpdatePatch is the merge-on-update path (admin-only): it loads the existing
// profile, overlays ONLY the fields present in the sparse patch, then runs the
// same validate-then-recache flow as a full write. Omitting an optional field
// leaves the stored value unchanged rather than blanking it; an explicitly
// cleared field (present, empty) IS applied. The merge lives HERE in the domain
// (not the MCP handler) so a future REST surface inherits the safe partial-edit
// semantics, mirroring how the authz gate is a shared service rule.
//
// SECURITY: this closes the destructive-full-replace footgun where omitting
// connection_ref silently blanked it and re-routed the profile to the claude-cli
// no-key subscription path (a different trust domain, no BaseURL policy).
func (s *ProfileService) UpdatePatch(ctx context.Context, alias string, patch ProfilePatch) (Profile, Effective, error) {
	if err := requireAdmin(ctx); err != nil {
		return Profile{}, Effective{}, err
	}
	existing, err := s.store.Get(ctx, alias)
	if err != nil {
		return Profile{}, Effective{}, err
	}
	if patch.ExpectedRevision != nil && *patch.ExpectedRevision != existing.Revision {
		return Profile{}, Effective{}, fmt.Errorf("%w: profile %q is at revision %d, not %d", ErrAdvanceConflict, alias, existing.Revision, *patch.ExpectedRevision)
	}
	merged, err := patch.apply(existing)
	if err != nil {
		return Profile{}, Effective{}, err
	}
	return s.write(ctx, merged, s.updateExpected(existing), &existing)
}

// updateExpected keeps the original read as the persistence precondition. Never
// fall back to unconditional CRUD when the conditional port is missing.
func (s *ProfileService) updateExpected(prior Profile) func(context.Context, Profile) (Profile, error) {
	return func(ctx context.Context, next Profile) (Profile, error) {
		store, ok := s.store.(ProfileMutationStore)
		if !ok {
			return Profile{}, ErrProfileMutationUnavailable
		}
		return store.UpdateExpected(ctx, prior, next)
	}
}

// write is the shared admin-gated path: prepare and validate capabilities, then
// lease → revalidate proof → persist → recache. The effective backend
// the capability ruleset is keyed on comes from the shared EffectiveBackend(p)
// rule (claude-cli when the profile names neither a backend nor a connection),
// the SAME rule the seed path uses so write-time validation and launch never
// disagree.
//
// prior is the stored profile an edit replaces (nil for a create). A following
// selection is validated and initialized only when it is new or the edit changes
// the mode, target, model, route, axes or tools, so unchanged selections work while
// catalog discovery is down. Nothing is fetched or probed under any store lock.
func (s *ProfileService) write(ctx context.Context, p Profile, persist func(context.Context, Profile) (Profile, error), prior *Profile) (Profile, Effective, error) {
	if err := requireAdmin(ctx); err != nil {
		return Profile{}, Effective{}, err
	}
	changed := prior == nil || selectionChanged(*prior, p)
	var selection PreparedSelection
	var err error
	if changed {
		selection, err = ValidateSelection(ctx, p, s.versions, s.source, s.rules)
	} else {
		selection.Profile, err = NormalizeSelection(p)
	}
	if err != nil {
		return Profile{}, Effective{}, &preparationError{err}
	}
	p = selection.Profile
	eff, err := s.validateAxes(ctx, p)
	if err != nil {
		return Profile{}, Effective{}, &preparationError{err}
	}
	var out Profile
	err = s.mutate(ctx, func(ctx context.Context) error {
		// Report axes from the route metadata protected by this lease, not
		// a native-provider floor computed before a connection edit.
		eff, err = s.validateAxes(ctx, p)
		if err != nil {
			return &preparationError{err}
		}
		if changed {
			out, err = s.persistPrepared(ctx, selection, prior, persist)
		} else {
			out, err = persist(ctx, p)
		}
		if err == nil {
			s.recache(ctx)
		}
		return err
	})
	if err != nil {
		return Profile{}, Effective{}, err
	}
	return out, eff, nil
}

// persistPrepared runs only under the caller's mutation lease. Following proof
// revalidation is read-only; pinned routes repeat local route/version/axis
// policy checks, never discovery or probing.
func (s *ProfileService) persistPrepared(ctx context.Context, selection PreparedSelection, prior *Profile, persist func(context.Context, Profile) (Profile, error)) (Profile, error) {
	if err := s.revalidatePrepared(ctx, selection); err != nil {
		return Profile{}, err
	}
	return s.storePrepared(ctx, selection, prior, persist)
}

func (s *ProfileService) revalidatePrepared(ctx context.Context, selection PreparedSelection) error {
	p := selection.Profile
	if _, err := s.validateAxes(ctx, p); err != nil {
		return &preparationError{err}
	}
	if p.SelectionMode != SelectionFollowing {
		if err := checkRouteVersions(ctx, p, s.versions, s.source, s.rules); err != nil {
			return &preparationError{err}
		}
		return nil
	}
	if selection.Candidate == nil || s.source == nil {
		return &preparationError{ErrExecutionRevisionUnavailable}
	}
	if err := s.source.RevalidateStart(ctx, p.Clone(), selection.Candidate.Clone()); err != nil {
		return &preparationError{err}
	}
	return nil
}

// storePrepared follows revalidation under the same lease. Separating these
// phases lets a seed batch recheck every proof before its own inserts change
// the installation's profile inventory.
func (s *ProfileService) storePrepared(ctx context.Context, selection PreparedSelection, prior *Profile, persist func(context.Context, Profile) (Profile, error)) (Profile, error) {
	if selection.Profile.SelectionMode != SelectionFollowing {
		return persist(ctx, selection.Profile)
	}
	store, ok := s.store.(SelectionMutationStore)
	if !ok {
		return Profile{}, ErrProfileMutationUnavailable
	}
	if prior == nil {
		return store.CreateValidated(ctx, selection)
	}
	return store.UpdateValidated(ctx, *prior, selection)
}

// preparationError marks a failure of the profile itself (selection, proof or
// axes) as opposed to the store or the lease. It unwraps, so errors.Is on the
// cause still works for every existing caller.
type preparationError struct{ err error }

func (e *preparationError) Error() string { return e.err.Error() }
func (e *preparationError) Unwrap() error { return e.err }

// IsInvalidProfile reports whether a write was refused because the profile
// could not be prepared or proved, so nothing was persisted and retrying the
// same input cannot succeed until the profile or the catalog changes.
func IsInvalidProfile(err error) bool {
	var pe *preparationError
	return errors.As(err, &pe)
}

// mutate runs fn under the shared execution gate's exclusive lease when one is
// injected (SetAdvancementDependencies). A nil gate writes directly; the root
// wiring test proves production injects one.
func (s *ProfileService) mutate(ctx context.Context, fn func(context.Context) error) error {
	if s.gate == nil {
		return fn(ctx)
	}
	return s.gate.WithMutation(ctx, fn)
}

// validateAxes checks local route and axis policy during preparation and again
// under the mutation lease. It never discovers or probes models.
func (s *ProfileService) validateAxes(ctx context.Context, p Profile) (Effective, error) {
	backend := EffectiveBackend(p)
	// Normalize the unset ("") axes to their explicit default sentinel before
	// validating, exactly as the seed path does: an empty string is not a Valid()
	// enum value, but a profile that omits the new axes must validate as
	// default/default (always valid). A genuinely malformed axis string fails to
	// parse and surfaces ErrInvalidContextWindow / ErrInvalidEffort.
	tier, err := ParseContextWindow(string(p.ContextWindow))
	if err != nil {
		return Effective{}, err
	}
	effort, err := ParseEffort(string(p.Effort))
	if err != nil {
		return Effective{}, err
	}
	requested := Effective{ContextWindow: tier, Effort: effort}
	lunaRoute, err := profileUsesLunaRoute(ctx, s.source, p)
	if err != nil {
		return Effective{}, err
	}
	var hasLunaRoute bool
	var otherRoutes []VersionRequest
	if lunaRoute {
		requests, err := s.source.VersionRequests(ctx, p.Clone())
		if err != nil {
			return Effective{}, err
		}
		if len(requests) == 0 {
			return Effective{}, fmt.Errorf("%w: no prospective routes", ErrExecutionRevisionUnavailable)
		}
		for _, request := range requests {
			if request.Model != p.Model {
				return Effective{}, fmt.Errorf("%w: route model differs from prospective model", ErrInvalidSelection)
			}
			if IsLunaRoute(request.RouteProvider) {
				hasLunaRoute = true
				if err := checkLunaRouteProfile(s.rules, p, request); err != nil {
					return Effective{}, err
				}
			} else {
				otherRoutes = append(otherRoutes, request)
			}
		}
		if hasLunaRoute {
			for _, request := range otherRoutes {
				otherBackend := backend
				if p.Kind == ProfileKindAgent {
					otherBackend = request.Backend
				}
				otherTier, otherEffort, err := s.resolveAxes(p, otherBackend, tier, effort)
				if err != nil {
					return Effective{}, err
				}
				if requested != (Effective{ContextWindow: otherTier, Effort: otherEffort}) {
					return Effective{}, fmt.Errorf("%w: mixed routes require different effective axes", ErrInvalidSelection)
				}
			}
			return requested, nil
		}
	}
	effTier, effEffort, err := s.resolveAxes(p, backend, tier, effort)
	if err != nil {
		return Effective{}, err
	}
	return Effective{ContextWindow: effTier, Effort: effEffort}, nil
}

// resolveAxes validates and floors a profile's (context, effort) axes and returns
// the effective values the emitter will use. A profile whose effective backend has
// a capability entry validates against it (the agent path: claude-code/codex/...).
//
// A connection-backed CHAT profile has no agent backend — EffectiveBackend is ""
// (the connection supplies the routing) and is absent from the agent ruleset. Its
// axes are instead governed by the OpenAI-compatible chat tier: the reasoning
// effort is honored (floored via ChatReasoningEffort, the SAME mapping the
// openrouter adapter emits as reasoning_effort, so write-time and launch agree),
// and there is no context-window knob, so a non-default tier is ErrAxisNotApplicable.
// Any OTHER backend-less profile (e.g. an agent that named no backend) still fails
// through Validate with ErrUnknownBackend — an agent must name a concrete backend.
func (s *ProfileService) resolveAxes(p Profile, backend string, tier ContextWindow, effort Effort) (ContextWindow, Effort, error) {
	if !s.rules.Has(backend) && p.Kind == ProfileKindChat {
		if tier != ContextDefault {
			return "", "", fmt.Errorf("%w: context %q on the connection-backed chat path", ErrAxisNotApplicable, tier)
		}
		effEffort, _, _ := ChatReasoningEffort(effort)
		return ContextDefault, effEffort, nil
	}
	return s.rules.Validate(backend, p.Model, tier, effort)
}

// Delete removes a profile by alias (admin-only) and recaches. A missing alias
// surfaces ErrProfileNotFound and triggers no reload.
func (s *ProfileService) Delete(ctx context.Context, alias string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	prior, err := s.store.Get(ctx, alias)
	if err != nil {
		return err
	}
	store, ok := s.store.(ProfileMutationStore)
	if !ok {
		return ErrProfileMutationUnavailable
	}
	return s.mutate(ctx, func(ctx context.Context) error {
		if err := store.DeleteExpected(ctx, prior); err != nil {
			return err
		}
		s.recache(ctx)
		return nil
	})
}

// recache runs the best-effort post-write reload. The error is intentionally not
// returned (a durable write must not be failed by a stale cache), but it is NOT
// silently discarded: the Reloader is the observability seam — the wiring layer
// wraps it in a decorator that WARN-logs a reload failure so the cache/DB
// split-brain on this live admin surface is surfaced. Keeping the discard here in
// one place (rather than three bare `_ =` sites) makes the best-effort contract
// explicit and the logging boundary unambiguous.
func (s *ProfileService) recache(ctx context.Context) {
	_ = s.reloader.Reload(ctx)
}

// Capabilities exposes the capability ruleset for the read-only
// provider_capabilities tool so a caller can discover valid (tier, effort)
// values before writing. Read-open: any authenticated identity.
func (s *ProfileService) Capabilities(ctx context.Context) (CapabilityRules, error) {
	if _, err := requireAuthed(ctx); err != nil {
		return CapabilityRules{}, err
	}
	return s.rules, nil
}
