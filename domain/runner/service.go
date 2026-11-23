package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// AuditRecorder records admin actions (*audit.Service satisfies it).
type AuditRecorder interface {
	Record(ctx context.Context, action, target, detail string)
}

// ServiceDeps wires the operator-facing pool service.
type ServiceDeps struct {
	Registry   Registry
	Ledger     Ledger
	Events     EventLog
	Recorder   *EventRecorder
	Audit      AuditRecorder
	Visibility RunVisibility
	Monitor    *HealthMonitor
	Telemetry  TelemetrySource
	// Window is optional; nil yields an empty recent telemetry list.
	Window TelemetryWindow
	// Homes is optional until workspace mobility is configured.
	Homes    HomeStore
	Notifier *PoolNotifier
	Clock    func() time.Time
}

// RunnerView is one runner as the pool page lists it.
type RunnerView struct {
	Runner          Runner
	Sources         []Source
	SlotsUsed       int
	Queued          int
	Telemetry       *Telemetry
	HomedWorkspaces int
}

// RunnerDetail adds what is running there now.
type RunnerDetail struct {
	RunnerView
	Assignments     []AssignmentView
	RecentTelemetry []Telemetry
	Homes           []HomeView
}

// AssignmentView is one active job; RunID nil + Hidden when the caller cannot view the run.
type AssignmentView struct {
	JobID    uuid.UUID
	RunID    *uuid.UUID
	NodeID   string
	State    JobState
	SourceID SourceID
	Hidden   bool
}

const (
	svcDefaultEventLimit = 100
	svcMaxEventLimit     = 500
)

// Service is the single authority for pool reads and admin writes; REST and MCP
// are thin adapters over it so the two surfaces cannot drift.
type Service struct{ d ServiceDeps }

// NewService validates the wiring (missing dependencies panic at boot).
func NewService(d ServiceDeps) *Service {
	if d.Registry == nil || d.Ledger == nil || d.Events == nil || d.Recorder == nil || d.Audit == nil ||
		d.Visibility == nil || d.Monitor == nil || d.Notifier == nil {
		panic("runner: NewService requires Registry, Ledger, Events, Recorder, Audit, Visibility, Monitor and Notifier")
	}
	if d.Telemetry == nil {
		d.Telemetry = EmptyTelemetry{}
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &Service{d: d}
}

func svcCaller(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Identity{}, identity.ErrUnauthenticated
	}
	return id, nil
}

func svcAdmin(ctx context.Context) (identity.Identity, error) {
	id, err := svcCaller(ctx)
	if err != nil {
		return id, err
	}
	if !id.IsAdmin() {
		return id, identity.ErrForbidden
	}
	return id, nil
}

// List returns every runner (built-in first) to any authenticated caller.
func (s *Service) List(ctx context.Context) ([]RunnerView, error) {
	if _, err := svcCaller(ctx); err != nil {
		return nil, err
	}
	snap, active, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	tel := s.d.Telemetry.Latest()
	out := make([]RunnerView, 0, len(snap.Runners))
	for _, r := range snap.Runners {
		out = append(out, svcView(r, snap, active, tel))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Runner.BuiltIn != out[j].Runner.BuiltIn {
			return out[i].Runner.BuiltIn
		}
		return out[i].Runner.ID < out[j].Runner.ID
	})
	return out, nil
}

// Get returns one runner with its active assignments, hiding runs the caller cannot view.
func (s *Service) Get(ctx context.Context, id RunnerID) (RunnerDetail, error) {
	if _, err := svcCaller(ctx); err != nil {
		return RunnerDetail{}, err
	}
	rn, _, err := s.d.Registry.GetRunner(ctx, id)
	if err != nil {
		return RunnerDetail{}, err
	}
	snap, active, err := s.state(ctx)
	if err != nil {
		return RunnerDetail{}, err
	}
	jobs, err := s.d.Ledger.ListActiveByRunner(ctx, id)
	if err != nil {
		return RunnerDetail{}, fmt.Errorf("runner: list assignments: %w", err)
	}
	d := RunnerDetail{RunnerView: svcView(rn, snap, active, s.d.Telemetry.Latest())}
	for _, j := range jobs {
		if j.State != JobOffered && j.State != JobRunning {
			continue
		}
		a := AssignmentView{JobID: j.ID, State: j.State, SourceID: j.SourceID}
		if s.d.Visibility.CanView(ctx, j.Key.RunID) {
			run := j.Key.RunID
			a.RunID, a.NodeID = &run, j.NodeID
		} else {
			a.Hidden = true
		}
		d.Assignments = append(d.Assignments, a)
	}
	d.RecentTelemetry = s.recentTelemetry(id)
	if d.Homes, err = s.homeViews(ctx, id); err != nil {
		return RunnerDetail{}, err
	}
	return d, nil
}

// Events returns recent pool events, newest first, dropping run-scoped events
// of runs the caller cannot view. limit <= 0 uses the default; it is capped.
func (s *Service) Events(ctx context.Context, limit int) ([]PoolEvent, error) {
	if _, err := svcCaller(ctx); err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = svcDefaultEventLimit
	case limit > svcMaxEventLimit:
		limit = svcMaxEventLimit
	}
	evs, err := s.d.Events.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("runner: list events: %w", err)
	}
	out := make([]PoolEvent, 0, len(evs))
	for _, ev := range evs {
		if ev.RunID != nil && !s.d.Visibility.CanView(ctx, *ev.RunID) {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// Update changes the step limit and/or labels (admin only, audited).
func (s *Service) Update(ctx context.Context, id RunnerID, p RunnerConfigPatch) (Runner, error) {
	who, err := svcAdmin(ctx)
	if err != nil {
		return Runner{}, err
	}
	if p.StepLimit != nil && !ValidStepLimit(*p.StepLimit) {
		return Runner{}, fmt.Errorf("%w: %d", ErrInvalidStepLimit, *p.StepLimit)
	}
	r, err := s.d.Registry.UpdateConfig(ctx, id, p)
	if err != nil {
		return Runner{}, err
	}
	detail := map[string]any{}
	if p.StepLimit != nil {
		detail["stepLimit"] = *p.StepLimit
	}
	if p.Labels != nil {
		detail["labels"] = p.Labels
	}
	s.d.Audit.Record(ctx, audit.ActionRunnerUpdate, string(id), svcAuditDetail(detail))
	s.operatorEvent(ctx, EventRunnerUpdated, id, who, detail)
	s.d.Notifier.Notify()
	return r, nil
}

// Cordon stops new placements on the runner (admin only, audited).
func (s *Service) Cordon(ctx context.Context, id RunnerID) error {
	return s.setCordon(ctx, id, true)
}

// Uncordon allows placements again (admin only, audited) and wakes waiters.
func (s *Service) Uncordon(ctx context.Context, id RunnerID) error {
	return s.setCordon(ctx, id, false)
}

func (s *Service) setCordon(ctx context.Context, id RunnerID, on bool) error {
	who, err := svcAdmin(ctx)
	if err != nil {
		return err
	}
	if err := s.d.Registry.SetCordoned(ctx, id, on); err != nil {
		return err
	}
	action, kind := audit.ActionRunnerUncordon, EventRunnerUncordoned
	if on {
		action, kind = audit.ActionRunnerCordon, EventRunnerCordoned
	}
	s.d.Audit.Record(ctx, action, string(id), svcAuditDetail(map[string]any{"cordoned": on}))
	s.operatorEvent(ctx, kind, id, who, map[string]any{"cordoned": on})
	s.d.Notifier.Notify()
	return nil
}

// ProbeSource runs the read-only probe now (admin only, audited).
func (s *Service) ProbeSource(ctx context.Context, id SourceID) (SourceHealth, error) {
	if _, err := svcAdmin(ctx); err != nil {
		return SourceHealth{}, err
	}
	h, err := s.d.Monitor.ProbeNow(ctx, id)
	if err != nil {
		return SourceHealth{}, err
	}
	s.d.Audit.Record(ctx, audit.ActionRunnerSourceProbe, string(id),
		svcAuditDetail(map[string]any{"auth": string(h.Auth), "capacity": string(h.Capacity)}))
	return h, nil
}

func (s *Service) state(ctx context.Context) (PoolSnapshot, []Job, error) {
	snap, err := s.d.Registry.Snapshot(ctx)
	if err != nil {
		return PoolSnapshot{}, nil, fmt.Errorf("runner: snapshot: %w", err)
	}
	active, err := s.d.Ledger.ListActive(ctx)
	if err != nil {
		return PoolSnapshot{}, nil, fmt.Errorf("runner: list jobs: %w", err)
	}
	return snap, active, nil
}

func (s *Service) operatorEvent(ctx context.Context, kind string, id RunnerID, who identity.Identity, detail map[string]any) {
	s.d.Recorder.Record(ctx, PoolEvent{
		ID: uuid.New(), At: s.d.Clock(), Kind: kind, RunnerID: id,
		Cause: Cause{Kind: CauseOperator, Actor: who.Username}, Detail: detail,
	})
}

func svcView(r Runner, snap PoolSnapshot, active []Job, tel map[RunnerID]Telemetry) RunnerView {
	v := RunnerView{Runner: r, SlotsUsed: snap.SlotsInUse[r.ID]}
	for _, src := range snap.Sources {
		if src.RunnerID == r.ID {
			v.Sources = append(v.Sources, src)
		}
	}
	sort.Slice(v.Sources, func(i, j int) bool { return v.Sources[i].ID < v.Sources[j].ID })
	for _, j := range active {
		if j.State == JobQueued && j.Wait != nil && j.Wait.Kind == WaitSlot && svcServes(v.Sources, j.Group) {
			v.Queued++
		}
	}
	if t, ok := tel[r.ID]; ok {
		t := t
		v.Telemetry = &t
	}
	for _, h := range snap.Homes {
		if h.RunnerID == r.ID {
			v.HomedWorkspaces++
		}
	}
	return v
}

// svcServes reports whether any of the runner's sources serves group g.
func svcServes(srcs []Source, g ModelGroup) bool {
	for _, s := range srcs {
		if s.Kind != g.Kind {
			continue
		}
		if g.ConnectionRef != "" && s.CredentialRef != g.ConnectionRef {
			continue
		}
		if g.Kind == CredNone {
			return true
		}
		for _, f := range s.Families {
			if f == g.Family {
				return true
			}
		}
	}
	return false
}

func svcAuditDetail(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
