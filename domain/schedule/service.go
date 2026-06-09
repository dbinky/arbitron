package schedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// Authz sentinels for the management surface. They map to HTTP 403 at the
// adapter seam (the validation sentinels in schedule.go map to 400, ErrNotFound
// to 404).
var (
	ErrForbidden             = errors.New("schedule: forbidden")
	ErrNotAuthorizedWorkflow = errors.New("schedule: cannot schedule a workflow you may not trigger")
	ErrDepartmentNotMember   = errors.New("schedule: department must be one of your departments")
	// ErrQuotaExceeded is returned when a non-admin creator already holds the
	// maximum number of schedules. It maps to HTTP 400 at the adapter seam.
	ErrQuotaExceeded = errors.New("schedule: per-user schedule quota exceeded")
	// ErrIntervalTooFrequent is returned when a cron expression fires more often
	// than the configured minimum interval (a resource/cost-exhaustion floor). It
	// maps to HTTP 400 at the adapter seam.
	ErrIntervalTooFrequent = errors.New("schedule: cron interval is finer than the allowed minimum")
)

// Default bounds. They turn the unbounded-schedule abuse vector (unattended,
// recurring agent runs amplifying token spend on a shared single-instance
// deployment) into a contained one without getting in a normal user's way.
const (
	// DefaultMaxSchedulesPerCreator caps how many schedules ONE non-admin user
	// may own. Generous by design; admins are exempt.
	DefaultMaxSchedulesPerCreator = 100
	// DefaultMinInterval is the floor on how frequently a schedule may fire. The
	// finest a standard 5-field cron can express is one minute, so the default
	// permits every valid standard expression; an operator may raise it. Admins
	// are exempt.
	DefaultMinInterval = time.Minute
)

// Bounds are the per-creator quantity/frequency limits enforced on Create (and
// on a cron change in Update). Admins bypass both bounds. A non-positive field
// falls back to its default in NewService.
type Bounds struct {
	MaxSchedulesPerCreator int
	MinInterval            time.Duration
}

// CanTrigger reports whether the caller may start workflowName — the SAME gate
// the engine trigger path applies (publication/visibility). "Can schedule" ==
// "can trigger". The composition root wires it over the published-workflow repo
// + workflow.CanSeePublication.
type CanTrigger interface {
	CanTrigger(ctx context.Context, id identity.Identity, workflowName string) (bool, error)
}

// CreateInput is the create payload. CreatorID is intentionally present but
// IGNORED by the service (the caller identity is authoritative); it exists only
// so a transport that decodes it cannot smuggle a different creator.
type CreateInput struct {
	WorkflowName string
	CronExpr     string
	Timezone     string
	Department   string
	Variables    map[string]any
	Enabled      bool
	CreatorID    uuid.UUID // ignored; creator is always the caller
}

// UpdateInput carries the mutable fields. Nil fields are left unchanged.
// CreatorID and ID are NOT mutable.
type UpdateInput struct {
	WorkflowName *string
	CronExpr     *string
	Timezone     *string
	Department   *string
	Variables    map[string]any
	Enabled      *bool
}

// Service holds the single authz + validation path shared by the REST and MCP
// adapters. Handlers are dumb translators: decode → call service → map errors.
type Service struct {
	repo       ScheduleRepository
	canTrigger CanTrigger
	now        func() time.Time
	bounds     Bounds
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithBounds overrides the per-creator quantity/frequency bounds. Non-positive
// fields keep their defaults.
func WithBounds(b Bounds) ServiceOption {
	return func(s *Service) {
		if b.MaxSchedulesPerCreator > 0 {
			s.bounds.MaxSchedulesPerCreator = b.MaxSchedulesPerCreator
		}
		if b.MinInterval > 0 {
			s.bounds.MinInterval = b.MinInterval
		}
	}
}

// WithServiceClock injects a clock for deterministic timestamps in tests. (The
// Service clock is a func() time.Time; the Evaluator's WithClock takes the Clock
// interface, hence the distinct name.)
func WithServiceClock(now func() time.Time) ServiceOption {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService constructs a Service. The clock defaults to time.Now and the bounds
// to the package defaults unless overridden via options.
func NewService(repo ScheduleRepository, canTrigger CanTrigger, opts ...ServiceOption) *Service {
	s := &Service{
		repo:       repo,
		canTrigger: canTrigger,
		now:        time.Now,
		bounds: Bounds{
			MaxSchedulesPerCreator: DefaultMaxSchedulesPerCreator,
			MinInterval:            DefaultMinInterval,
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NewServiceWithClock is like NewService but injects a clock for deterministic
// timestamps in tests. Retained for existing callers; prefer NewService with
// WithClock for new code.
func NewServiceWithClock(repo ScheduleRepository, canTrigger CanTrigger, now func() time.Time) *Service {
	return NewService(repo, canTrigger, WithServiceClock(now))
}

// caller reads the request identity; absence is a forbidden (never leaks).
func caller(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Identity{}, ErrForbidden
	}
	return id, nil
}

// authorize reports whether c may manage s (owner or admin).
func authorize(c identity.Identity, s Schedule) bool {
	return c.IsAdmin() || s.CreatorID == c.UserID
}

// Create builds a schedule attributed to the caller, validates it, checks the
// can-trigger gate, validates any department override, and persists it.
func (s *Service) Create(ctx context.Context, in CreateInput) (Schedule, error) {
	c, err := caller(ctx)
	if err != nil {
		return Schedule{}, err
	}
	now := s.now().UTC()
	sched := Schedule{
		ID:           uuid.New(),
		WorkflowName: in.WorkflowName,
		CronExpr:     in.CronExpr,
		Timezone:     NormalizeTimezone(in.Timezone),
		CreatorID:    c.UserID, // authoritative; in.CreatorID is ignored
		Department:   in.Department,
		Enabled:      in.Enabled,
		Variables:    in.Variables,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := Validate(sched); err != nil {
		return Schedule{}, err
	}
	if err := s.enforceIntervalFloor(c, sched); err != nil {
		return Schedule{}, err
	}
	if err := s.enforceCreateQuota(ctx, c); err != nil {
		return Schedule{}, err
	}
	if in.Department != "" && !c.InDepartment(in.Department) {
		return Schedule{}, fmt.Errorf("%w: %q", ErrDepartmentNotMember, in.Department)
	}
	if err := s.assertCanTrigger(ctx, c, in.WorkflowName); err != nil {
		return Schedule{}, err
	}
	if err := s.repo.Create(ctx, &sched); err != nil {
		return Schedule{}, err
	}
	return sched, nil
}

// enforceCreateQuota rejects a Create that would push a non-admin creator over
// the per-creator schedule count cap. Admins are exempt. The creator's own
// schedules are counted via the SAME owner-filtered List the service uses
// elsewhere (the filter is service-set from the identity, never request input).
func (s *Service) enforceCreateQuota(ctx context.Context, c identity.Identity) error {
	if c.IsAdmin() || s.bounds.MaxSchedulesPerCreator <= 0 {
		return nil
	}
	uid := c.UserID
	existing, err := s.repo.List(ctx, ListFilters{CreatorID: &uid})
	if err != nil {
		return fmt.Errorf("counting existing schedules: %w", err)
	}
	if len(existing) >= s.bounds.MaxSchedulesPerCreator {
		return fmt.Errorf("%w: %d/%d", ErrQuotaExceeded, len(existing), s.bounds.MaxSchedulesPerCreator)
	}
	return nil
}

// enforceIntervalFloor rejects a schedule whose cron fires more often than the
// configured minimum interval. Admins are exempt. The actual interval is the gap
// between the next two occurrences from the creation instant; a schedule already
// validated (parseable cron) is assumed here.
func (s *Service) enforceIntervalFloor(c identity.Identity, sched Schedule) error {
	if c.IsAdmin() || s.bounds.MinInterval <= 0 {
		return nil
	}
	base := s.now().UTC()
	first, err := NextFire(sched, base)
	if err != nil {
		return err
	}
	second, err := NextFire(sched, first)
	if err != nil {
		return err
	}
	if interval := second.Sub(first); interval < s.bounds.MinInterval {
		return fmt.Errorf("%w: %s < %s", ErrIntervalTooFrequent, interval, s.bounds.MinInterval)
	}
	return nil
}

// Get loads a schedule, enforcing owner-or-admin access.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Schedule, error) {
	c, err := caller(ctx)
	if err != nil {
		return Schedule{}, err
	}
	sched, err := s.repo.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !authorize(c, *sched) {
		return Schedule{}, ErrForbidden
	}
	return *sched, nil
}

// List returns the caller's own schedules (non-admin) or all (admin). The
// creator filter is SERVICE-set from the identity, never from request input.
func (s *Service) List(ctx context.Context) ([]Schedule, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var filters ListFilters
	if !c.IsAdmin() {
		uid := c.UserID
		filters.CreatorID = &uid
	}
	return s.repo.List(ctx, filters)
}

// Update applies the allowed mutable fields, re-validates, re-checks the
// can-trigger gate when the workflow changes, and persists. CreatorID/ID are
// never mutated.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Schedule, error) {
	c, err := caller(ctx)
	if err != nil {
		return Schedule{}, err
	}
	sched, err := s.repo.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !authorize(c, *sched) {
		return Schedule{}, ErrForbidden
	}

	workflowChanged := false
	cronChanged := false
	updated := *sched
	if in.WorkflowName != nil && *in.WorkflowName != updated.WorkflowName {
		updated.WorkflowName = *in.WorkflowName
		workflowChanged = true
	}
	if in.CronExpr != nil && *in.CronExpr != updated.CronExpr {
		updated.CronExpr = *in.CronExpr
		cronChanged = true
	}
	if in.Timezone != nil {
		updated.Timezone = NormalizeTimezone(*in.Timezone)
	}
	if in.Department != nil {
		updated.Department = *in.Department
	}
	if in.Variables != nil {
		updated.Variables = in.Variables
	}
	if in.Enabled != nil {
		updated.Enabled = *in.Enabled
	}

	if err := Validate(updated); err != nil {
		return Schedule{}, err
	}
	if cronChanged {
		if err := s.enforceIntervalFloor(c, updated); err != nil {
			return Schedule{}, err
		}
	}
	if updated.Department != "" && !c.InDepartment(updated.Department) {
		return Schedule{}, fmt.Errorf("%w: %q", ErrDepartmentNotMember, updated.Department)
	}
	if workflowChanged {
		if err := s.assertCanTrigger(ctx, c, updated.WorkflowName); err != nil {
			return Schedule{}, err
		}
	}
	updated.UpdatedAt = s.now().UTC()
	if err := s.repo.Update(ctx, &updated); err != nil {
		return Schedule{}, err
	}
	return updated, nil
}

// SetEnabled flips a schedule's enabled flag (the enable/disable convenience).
func (s *Service) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) (Schedule, error) {
	c, err := caller(ctx)
	if err != nil {
		return Schedule{}, err
	}
	sched, err := s.repo.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !authorize(c, *sched) {
		return Schedule{}, ErrForbidden
	}
	sched.Enabled = enabled
	sched.UpdatedAt = s.now().UTC()
	if err := s.repo.Update(ctx, sched); err != nil {
		return Schedule{}, err
	}
	return *sched, nil
}

// Delete removes a schedule, enforcing owner-or-admin access.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	c, err := caller(ctx)
	if err != nil {
		return err
	}
	sched, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !authorize(c, *sched) {
		return ErrForbidden
	}
	return s.repo.Delete(ctx, id)
}

// assertCanTrigger maps the can-trigger gate to ErrNotAuthorizedWorkflow.
func (s *Service) assertCanTrigger(ctx context.Context, c identity.Identity, workflowName string) error {
	ok, err := s.canTrigger.CanTrigger(ctx, c, workflowName)
	if err != nil {
		return fmt.Errorf("checking trigger authorization for %q: %w", workflowName, err)
	}
	if !ok {
		return ErrNotAuthorizedWorkflow
	}
	return nil
}
