package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/provider"
)

// ProfileRepo implements provider.ProfileStore backed by SQLite. Profiles carry
// NO secrets, so — unlike ConnectionRepo — there is no crypto seam here: the
// endpoint key lives on the referenced connection, not on the profile. The
// AllowedTools slice is (de)serialized to a JSON array; the two axis enums are
// stored as their string form and parsed back through the domain parsers (a
// malformed stored value is a corruption error, never a silent garbled struct).
type ProfileRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// Compile-time assertion that ProfileRepo satisfies the domain port.
var _ provider.ProfileStore = (*ProfileRepo)(nil)

// NewProfileRepo creates a ProfileRepo over the given database connection.
func NewProfileRepo(db *sql.DB) *ProfileRepo {
	return &ProfileRepo{db: db, queries: generated.New(db)}
}

// toolsToJSON marshals the per-profile allowlist to a JSON array, mapping an
// empty/nil slice to the canonical "[]" literal (the column default).
func toolsToJSON(tools []string) (string, error) {
	if len(tools) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// toolsFromJSON unmarshals the stored allowlist back to a non-nil slice so JSON
// callers see [] rather than null.
func toolsFromJSON(s string) ([]string, error) {
	if s == "" || s == "[]" {
		return []string{}, nil
	}
	var tools []string
	if err := json.Unmarshal([]byte(s), &tools); err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []string{}
	}
	return tools, nil
}

// rowToProfile maps a stored row's columns into a domain Profile, parsing the
// JSON allowlist and the two axis enums. A malformed allowed_tools / axis value
// is a corruption error returned to the caller, never a silently garbled struct.
func rowToProfile(alias, kind, connectionRef, model, backend, allowedTools, contextWindow, effort, selectionMode, followProvider, followTier string, revision int64) (provider.Profile, error) {
	tools, err := toolsFromJSON(allowedTools)
	if err != nil {
		return provider.Profile{}, fmt.Errorf("unmarshalling allowed_tools for profile %q: %w", alias, err)
	}
	cw, err := provider.ParseContextWindow(contextWindow)
	if err != nil {
		return provider.Profile{}, fmt.Errorf("parsing context_window %q for profile %q: %w", contextWindow, alias, err)
	}
	eff, err := provider.ParseEffort(effort)
	if err != nil {
		return provider.Profile{}, fmt.Errorf("parsing effort %q for profile %q: %w", effort, alias, err)
	}
	var target *provider.FollowTarget
	if followProvider != "" || followTier != "" {
		target = &provider.FollowTarget{Provider: followProvider, Tier: followTier}
	}
	if revision < 1 {
		return provider.Profile{}, fmt.Errorf("invalid revision for profile %q", alias)
	}
	return provider.NormalizeSelection(provider.Profile{
		SelectionMode: provider.SelectionMode(selectionMode),
		FollowTarget:  target,
		Revision:      revision,
		Alias:         alias,
		Kind:          provider.ProfileKind(kind),
		ConnectionRef: connectionRef,
		Model:         model,
		Backend:       backend,
		AllowedTools:  tools,
		ContextWindow: cw,
		Effort:        eff,
	})
}

// axisString returns the stored string form of an axis value, normalizing the
// empty "unset" sentinel to the explicit "default" literal so the column always
// holds a parseable value.
func axisString(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

func followColumns(p provider.Profile) (string, string) {
	if p.FollowTarget == nil {
		return "", ""
	}
	return p.FollowTarget.Provider, p.FollowTarget.Tier
}

func (r *ProfileRepo) Create(ctx context.Context, p provider.Profile) error {
	return createProfile(ctx, r.queries, p)
}

func createProfile(ctx context.Context, queries *generated.Queries, p provider.Profile) error {
	p, err := provider.NormalizeSelection(p)
	if err != nil {
		return err
	}
	followProvider, followTier := followColumns(p)
	toolsJSON, err := toolsToJSON(p.AllowedTools)
	if err != nil {
		return fmt.Errorf("marshalling allowed_tools for profile %q: %w", p.Alias, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	err = queries.CreateProfile(ctx, generated.CreateProfileParams{
		Alias:          p.Alias,
		Kind:           string(p.Kind),
		ConnectionRef:  p.ConnectionRef,
		Model:          p.Model,
		Backend:        p.Backend,
		AllowedTools:   toolsJSON,
		ContextWindow:  axisString(string(p.ContextWindow)),
		Effort:         axisString(string(p.Effort)),
		SelectionMode:  string(p.SelectionMode),
		FollowProvider: followProvider,
		FollowTier:     followTier,
		CreatedAt:      now,
		UpdatedAt:      now,
		Alias_2:        p.Alias, // recreated aliases start above their deleted revision floor
	})
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("profile %q: %w", p.Alias, provider.ErrProfileExists)
		}
		return fmt.Errorf("creating profile %q: %w", p.Alias, err)
	}
	return nil
}

func (r *ProfileRepo) Update(ctx context.Context, p provider.Profile) error {
	p, err := provider.NormalizeSelection(p)
	if err != nil {
		return err
	}
	followProvider, followTier := followColumns(p)
	toolsJSON, err := toolsToJSON(p.AllowedTools)
	if err != nil {
		return fmt.Errorf("marshalling allowed_tools for profile %q: %w", p.Alias, err)
	}
	// Rely on the write's own rows-affected to detect a missing row in ONE
	// statement: a separate pre-check SELECT races a concurrent delete (the UPDATE
	// would then affect zero rows yet report success). Zero rows affected -> the
	// profile does not exist -> the sentinel.
	rows, err := r.queries.UpdateProfile(ctx, generated.UpdateProfileParams{
		Kind:           string(p.Kind),
		ConnectionRef:  p.ConnectionRef,
		Model:          p.Model,
		Backend:        p.Backend,
		AllowedTools:   toolsJSON,
		ContextWindow:  axisString(string(p.ContextWindow)),
		Effort:         axisString(string(p.Effort)),
		SelectionMode:  string(p.SelectionMode),
		FollowProvider: followProvider,
		FollowTier:     followTier,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
		Alias:          p.Alias,
	})
	if err != nil {
		return fmt.Errorf("updating profile %q: %w", p.Alias, err)
	}
	if rows == 0 {
		return fmt.Errorf("profile %q: %w", p.Alias, provider.ErrProfileNotFound)
	}
	return nil
}

func (r *ProfileRepo) Delete(ctx context.Context, alias string) error {
	// Single-statement delete: zero rows affected maps to the sentinel, correct
	// under a concurrent delete and one round-trip instead of a check-then-act
	// pre-check SELECT.
	rows, err := r.queries.DeleteProfile(ctx, alias)
	if err != nil {
		return fmt.Errorf("deleting profile %q: %w", alias, err)
	}
	if rows == 0 {
		return fmt.Errorf("profile %q: %w", alias, provider.ErrProfileNotFound)
	}
	return nil
}

func (r *ProfileRepo) Get(ctx context.Context, alias string) (provider.Profile, error) {
	return getProfile(ctx, r.queries, alias)
}

func getProfile(ctx context.Context, queries *generated.Queries, alias string) (provider.Profile, error) {
	row, err := queries.GetProfile(ctx, alias)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return provider.Profile{}, fmt.Errorf("profile %q: %w", alias, provider.ErrProfileNotFound)
		}
		return provider.Profile{}, fmt.Errorf("getting profile %q: %w", alias, err)
	}
	return rowToProfile(row.Alias, row.Kind, row.ConnectionRef, row.Model, row.Backend, row.AllowedTools, row.ContextWindow, row.Effort, row.SelectionMode, row.FollowProvider, row.FollowTier, row.Revision)
}

func (r *ProfileRepo) List(ctx context.Context) ([]provider.Profile, error) {
	rows, err := r.queries.ListProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	profiles := make([]provider.Profile, len(rows))
	for i, row := range rows {
		p, err := rowToProfile(row.Alias, row.Kind, row.ConnectionRef, row.Model, row.Backend, row.AllowedTools, row.ContextWindow, row.Effort, row.SelectionMode, row.FollowProvider, row.FollowTier, row.Revision)
		if err != nil {
			return nil, err
		}
		profiles[i] = p
	}
	return profiles, nil
}

// Count returns the number of stored profiles.
func (r *ProfileRepo) Count(ctx context.Context) (int, error) {
	n, err := r.queries.CountProfiles(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting profiles: %w", err)
	}
	return int(n), nil
}
