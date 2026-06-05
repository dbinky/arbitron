# RBAC Phase 4 — Task 1: Dashboard domain service + StatsRepository port

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a `domain/dashboard` service that composes four department-scoped aggregates behind a `StatsRepository` port and applies the caller-department clamp (admins exempt).

**Architecture:** Pure-domain hexagonal service. The service reads the caller `identity.Identity` from context, clamps the requested department set to the caller's effective departments (unless admin), then delegates to a `StatsRepository` port for the four aggregates. No infra imports. Window parsing/clamping lives here so the HTTP handler stays thin.

**Tech Stack:** Go, hexagonal architecture, `domain/identity` for caller context, strict TDD with `testify`.

**Dependencies:** Phase 1 (`identity.Identity.Departments`, `LedDepartments`, `InDepartment`, `IsAdmin`) and Phase 3 (`instances.department` + `instances.visibility` columns + `idx_instances_dept_vis`) are assumed present. This task has no dependency on other Phase 4 tasks; Tasks 2 and 3 depend on it.

---

## Context You Need

- `domain/identity/identity.go` defines `Identity` and `MustFromContext(ctx)`. Phase 1 adds `Departments []string`, `LedDepartments []string`, and predicates `InDepartment(dept string) bool`, `LeadsDepartment(dept string) bool`. `IsAdmin()` already exists.
- The existing aggregate pattern is `domain/instance` `WorkflowStats` (struct) returned by `InstanceRepository.Stats`. We mirror that style: domain owns the result structs, the port returns them.
- Hex edge rule: domain packages import only other domain packages + stdlib. `domain/dashboard` may import `domain/identity`.

## File Structure

- Create `domain/dashboard/dashboard.go` — result structs + the `Stats` aggregate envelope.
- Create `domain/dashboard/service.go` — the `Service`, the `StatsRepository` port, window parsing, and the department clamp.
- Create `domain/dashboard/service_test.go` — TDD scenarios.

---

### Task 1: Dashboard domain service + StatsRepository port

**Files:**
- Create: `domain/dashboard/dashboard.go`
- Create: `domain/dashboard/service.go`
- Test: `domain/dashboard/service_test.go`

- [ ] **Step 1: Write the failing test for the result types + a fake repo**

Create `domain/dashboard/service_test.go`:

```go
package dashboard_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/dashboard"
	"github.com/arbitron/arbitron/domain/identity"
)

// fakeRepo records the AggregateParams it was called with and returns canned data.
type fakeRepo struct {
	gotParams dashboard.AggregateParams
	stats     dashboard.Stats
	err       error
}

func (f *fakeRepo) Aggregate(_ context.Context, p dashboard.AggregateParams) (dashboard.Stats, error) {
	f.gotParams = p
	return f.stats, f.err
}

func ctxWith(id identity.Identity) context.Context {
	return identity.WithIdentity(context.Background(), id)
}

func member(depts ...string) identity.Identity {
	return identity.Identity{
		UserID:      uuid.New(),
		Username:    "m",
		Role:        identity.RoleUser,
		Departments: depts,
	}
}

func admin() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleAdmin}
}

func TestGetStats_ClampsRequestedToCallerDepartments(t *testing.T) {
	repo := &fakeRepo{stats: dashboard.Stats{Departments: []string{"engineering"}}}
	svc := dashboard.NewService(repo)

	// Caller is in engineering only but requests engineering + sales.
	out, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{
		Departments: []string{"engineering", "sales"},
		Window:      24 * time.Hour,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"engineering"}, repo.gotParams.Departments,
		"foreign department must be dropped before querying")
	assert.Equal(t, []string{"engineering"}, out.Departments)
}
```

- [ ] **Step 2: Run the test to verify it fails to compile**

Run: `go test ./domain/dashboard/ -run TestGetStats_ClampsRequestedToCallerDepartments -v`
Expected: FAIL — `undefined: dashboard.Stats`, `dashboard.NewService`, etc.

- [ ] **Step 3: Create the result types**

Create `domain/dashboard/dashboard.go`:

```go
// Package dashboard aggregates department-scoped operational statistics over
// the department-stamped instances produced in Phase 3. It owns the result
// structs and a StatsRepository port; the HTTP adapter stays thin.
package dashboard

import "time"

// QueueHealth is the current count of instances by lifecycle state, plus a
// derived "failures needing attention" signal. These are point-in-time counts
// (no window) for live operational monitoring.
type QueueHealth struct {
	Running                int `json:"running"`
	Queued                 int `json:"queued"`
	Waiting                int `json:"waiting"`
	Paused                 int `json:"paused"`
	FailedNeedingAttention int `json:"failedNeedingAttention"`
}

// ThroughputBucket is the number of runs created within one time bucket.
type ThroughputBucket struct {
	Bucket time.Time `json:"bucket"` // bucket start (RFC3339 when serialized)
	Runs   int       `json:"runs"`
}

// Outcomes summarises success/failure and duration over the window.
type Outcomes struct {
	TotalRuns          int     `json:"totalRuns"`
	SuccessRate        float64 `json:"successRate"` // 0.0..1.0
	FailureRate        float64 `json:"failureRate"` // 0.0..1.0
	AvgDurationSeconds float64 `json:"avgDurationSeconds"`
	P95DurationSeconds float64 `json:"p95DurationSeconds"`
}

// WorkflowBreakdown is one row of the per-workflow ranking.
type WorkflowBreakdown struct {
	WorkflowName       string  `json:"workflowName"`
	Runs               int     `json:"runs"`
	FailureRate        float64 `json:"failureRate"` // 0.0..1.0
	AvgDurationSeconds float64 `json:"avgDurationSeconds"`
}

// Stats is the full dashboard envelope returned to the caller.
type Stats struct {
	Departments []string            `json:"departments"`
	Queue       QueueHealth         `json:"queue"`
	Throughput  []ThroughputBucket  `json:"throughput"`
	Outcomes    Outcomes            `json:"outcomes"`
	PerWorkflow []WorkflowBreakdown `json:"perWorkflow"`
}
```

- [ ] **Step 4: Create the service, port, and clamp**

Create `domain/dashboard/service.go`:

```go
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
```

- [ ] **Step 5: Run the clamp test to verify it passes**

Run: `go test ./domain/dashboard/ -run TestGetStats_ClampsRequestedToCallerDepartments -v`
Expected: PASS

- [ ] **Step 6: Add the union-default, admin-exempt, and window-clamp tests**

Append to `domain/dashboard/service_test.go`:

```go
func TestGetStats_UnionDefaultWhenNoDepartmentsRequested(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(member("engineering", "sales")), dashboard.Request{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"engineering", "sales"}, repo.gotParams.Departments,
		"omitted departments ⇒ caller's full effective set")
}

func TestGetStats_AdminMayRequestAnyDepartment(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(admin()), dashboard.Request{
		Departments: []string{"finance", "legal"},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"finance", "legal"}, repo.gotParams.Departments,
		"admin requests are not clamped")
}

func TestGetStats_NonMemberRequestReducesToEmpty(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{
		Departments: []string{"sales"},
	})
	require.NoError(t, err)
	assert.Empty(t, repo.gotParams.Departments,
		"requesting only foreign departments yields an empty allowed set")
}

func TestGetStats_WindowClamping(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero defaults", 0, dashboard.DefaultWindow},
		{"negative defaults", -5 * time.Hour, dashboard.DefaultWindow},
		{"below min clamps up", 30 * time.Minute, dashboard.MinWindow},
		{"above max clamps down", 90 * 24 * time.Hour, dashboard.MaxWindow},
		{"in range preserved", 6 * time.Hour, 6 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := dashboard.NewService(repo)
			_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{Window: tc.in})
			require.NoError(t, err)
			assert.Equal(t, tc.want, repo.gotParams.Window)
		})
	}
}

func TestGetStats_PropagatesRepoError(t *testing.T) {
	repo := &fakeRepo{err: context.DeadlineExceeded}
	svc := dashboard.NewService(repo)
	_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestGetStats_PanicsWithoutIdentity(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)
	assert.Panics(t, func() {
		_, _ = svc.GetStats(context.Background(), dashboard.Request{})
	}, "missing identity is a programming error (route bypassed auth middleware)")
}
```

- [ ] **Step 7: Run the full package test suite**

Run: `go test ./domain/dashboard/ -v`
Expected: PASS (all scenarios green)

- [ ] **Step 8: Vet and build**

Run: `go vet ./domain/dashboard/ && go build ./...`
Expected: no output / exit 0

- [ ] **Step 9: Commit**

```bash
git add domain/dashboard/
git commit -m "feat(dashboard): department-scoped stats service + StatsRepository port

Composes four aggregates behind a port and clamps requested departments to
the caller's effective set (admins exempt); window clamped to [1h,30d].

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec coverage: "server-side clamp to caller's effective departments (admins exempt)", "union default when omitted" — covered by clamp + tests.
- The five TDD categories are present: happy-path (clamp/union), edge (window boundaries, dedupe), error (repo error propagation), authorization (admin-exempt, non-member reduced to empty), mode-ish (identity required → panic). Local-mode behavior is exercised end-to-end in Task 3 (admin identity ⇒ unconstrained).
- `Stats.Departments` is overwritten with the clamped set so the response always reflects what was actually queried.
