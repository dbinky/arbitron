package dashboard

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
)

// Window bounds. The handler parses the raw query param; the service clamps the
// resulting duration into a sane operational range.
const (
	DefaultWindow = 24 * time.Hour
	MinWindow     = time.Hour
	MaxWindow     = 30 * 24 * time.Hour
)

// AggregateParams is the fully-resolved, security-clamped query the repository
// runs. Departments is ALWAYS the caller-allowed set (never raw request input);
// an empty slice means "no visible departments" and the repo must return zeroes.
type AggregateParams struct {
	Departments []string
	Window      time.Duration
	Now         time.Time
}

// StatsRepository is the port the dashboard service depends on. A single method
// returns the whole envelope so the adapter can run the four aggregates in one
// place (and, later, in one transaction/connection).
type StatsRepository interface {
	Aggregate(ctx context.Context, p AggregateParams) (Stats, error)
}

// Request is the caller-supplied, UNVALIDATED query from the HTTP layer.
type Request struct {
	Departments []string      // requested departments; empty ⇒ caller's full set
	Window      time.Duration // requested window; <=0 ⇒ DefaultWindow
}

// Service composes the aggregates and enforces the department clamp.
type Service struct {
	repo StatsRepository
	now  func() time.Time
}

// NewService builds the service over a StatsRepository.
func NewService(repo StatsRepository) *Service {
	return &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// GetStats clamps the request to the caller's effective departments (admins
// exempt) and the window to [MinWindow, MaxWindow], then queries the repo.
func (s *Service) GetStats(ctx context.Context, req Request) (Stats, error) {
	id := identity.MustFromContext(ctx)

	depts := s.clampDepartments(id, req.Departments)
	window := clampWindow(req.Window)

	out, err := s.repo.Aggregate(ctx, AggregateParams{
		Departments: depts,
		Window:      window,
		Now:         s.now(),
	})
	if err != nil {
		return Stats{}, err
	}
	// Echo the effective department set the query actually ran over.
	out.Departments = depts
	return out, nil
}

// clampDepartments returns the requested set intersected with the caller's
// effective departments. Admins may request any department; an admin with an
// empty request gets an empty slice (meaning "all" is the repo's job to handle
// — but for the union-default the handler passes the caller's full set, so an
// empty admin request here is intentional and means "no department filter").
func (s *Service) clampDepartments(id identity.Identity, requested []string) []string {
	if id.IsAdmin() {
		return dedupe(requested)
	}
	if len(requested) == 0 {
		// Union default: the caller's full effective set.
		return dedupe(id.Departments)
	}
	allowed := make([]string, 0, len(requested))
	for _, d := range dedupe(requested) {
		if id.InDepartment(d) {
			allowed = append(allowed, d)
		}
	}
	return allowed
}

func clampWindow(w time.Duration) time.Duration {
	if w <= 0 {
		return DefaultWindow
	}
	if w < MinWindow {
		return MinWindow
	}
	if w > MaxWindow {
		return MaxWindow
	}
	return w
}

// dedupe returns the input with duplicates removed, order preserved, nil-safe.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
