package provider

import "context"

// Effective is the floored, runtime-effective view of a profile's axes echoed
// back on a write. It is what the emitter will actually emit at launch: a
// requested effort the backend/model cannot honor floors to its ceiling, so the
// caller sees the value that will take effect rather than the value requested.
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
}

// NewProfileService wires the persistence port, the capability ruleset, and the
// recache hook (the store-bound ProfileCache). The reloader is invoked after a
// successful write so live edits take effect without a restart.
func NewProfileService(store ProfileStore, rules CapabilityRules, reloader Reloader) *ProfileService {
	return &ProfileService{store: store, rules: rules, reloader: reloader}
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
	return s.write(ctx, p, s.store.Create)
}

// Update overwrites an existing profile by alias (admin-only), with the same
// validate-then-recache flow as Create.
func (s *ProfileService) Update(ctx context.Context, p Profile) (Profile, Effective, error) {
	return s.write(ctx, p, s.store.Update)
}

// write is the shared admin-gated mutation path: gate → validate (NOTHING is
// persisted on a capability rejection) → persist → best-effort recache. The
// effective backend defaults to claude-cli when the profile names neither a
// backend nor a connection (the no-key subscription path), matching the runtime
// emitter's resolution so write-time validation and launch never disagree.
func (s *ProfileService) write(ctx context.Context, p Profile, persist func(context.Context, Profile) error) (Profile, Effective, error) {
	if err := requireAdmin(ctx); err != nil {
		return Profile{}, Effective{}, err
	}
	backend := p.Backend
	if backend == "" && p.ConnectionRef == "" {
		backend = "claude-cli"
	}
	// Normalize the unset ("") axes to their explicit default sentinel before
	// validating, exactly as the seed path does: an empty string is not a Valid()
	// enum value, but a profile that omits the new axes must validate as
	// default/default (always valid). A genuinely malformed axis string fails to
	// parse and surfaces ErrInvalidContextWindow / ErrInvalidEffort.
	tier, err := ParseContextWindow(string(p.ContextWindow))
	if err != nil {
		return Profile{}, Effective{}, err
	}
	effort, err := ParseEffort(string(p.Effort))
	if err != nil {
		return Profile{}, Effective{}, err
	}
	effTier, effEffort, err := s.rules.Validate(backend, p.Model, tier, effort)
	if err != nil {
		return Profile{}, Effective{}, err
	}
	if err := persist(ctx, p); err != nil {
		return Profile{}, Effective{}, err
	}
	// Best-effort recache: the write is already durable, so a reload failure does
	// not fail the operation. The next boot reloads cleanly from the DB.
	_ = s.reloader.Reload(ctx)
	return p, Effective{ContextWindow: effTier, Effort: effEffort}, nil
}

// Delete removes a profile by alias (admin-only) and recaches. A missing alias
// surfaces ErrProfileNotFound and triggers no reload.
func (s *ProfileService) Delete(ctx context.Context, alias string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, alias); err != nil {
		return err
	}
	_ = s.reloader.Reload(ctx)
	return nil
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
