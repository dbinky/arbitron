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
// runs. Departments is ALWAYS the caller-allowed set (never raw request input).
//
// The "all" and "none" cases are DISTINCT signals, never the same empty slice
// (PR #21): when AllDepartments is true the repo aggregates EVERY department
// (admin, no filter); when AllDepartments is false an empty Departments slice
// means "no visible departments" and the repo returns zeroes (apart from the
// always-visible public lane). Conflating the two previously gave the admin an
// all-zeros first-load dashboard.
type AggregateParams struct {
	// AllDepartments, when true, removes the department filter entirely (admin
	// viewing the whole org). Departments is ignored in this case.
	AllDepartments bool
	Departments    []string
	Window         time.Duration
	Now            time.Time
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

	depts, allDepartments := s.clampDepartments(id, req.Departments)
	window := clampWindow(req.Window)

	out, err := s.repo.Aggregate(ctx, AggregateParams{
		AllDepartments: allDepartments,
		Departments:    depts,
		Window:         window,
		Now:            s.now(),
	})
	if err != nil {
		return Stats{}, err
	}
	// Echo the effective department set the query actually ran over.
	out.Departments = depts
	return out, nil
}

// clampDepartments returns the requested set intersected with the caller's
// effective departments, plus an allDepartments signal that is true ONLY for an
// admin who specified no departments (first-load "show me everything").
//
// The "all" and "none" cases are kept DISTINCT (PR #21): an admin's empty
// request returns (nil, true) so the repo drops the department filter and the
// flagship admin sees the full aggregate on first load — previously this
// collapsed to an empty slice and produced an all-zeros dashboard. A non-admin
// (or an admin who DID request specific departments) never gets the all signal.
func (s *Service) clampDepartments(id identity.Identity, requested []string) (depts []string, allDepartments bool) {
	if id.IsAdmin() {
		if len(requested) == 0 {
			// Admin, no filter ⇒ the whole org (distinct from "no departments").
			return nil, true
		}
		return identity.DedupeDepts(requested), false
	}
	if len(requested) == 0 {
		// Union default: the caller's full effective set.
		return identity.DedupeDepts(id.Departments), false
	}
	allowed := make([]string, 0, len(requested))
	for _, d := range identity.DedupeDepts(requested) {
		if id.InDepartment(d) {
			allowed = append(allowed, d)
		}
	}
	return allowed, false
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
