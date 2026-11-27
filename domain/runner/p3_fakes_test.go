// domain/runner/p3_fakes_test.go
package runner_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// p3MemLedger is an in-memory runner.Ledger that honours fences, generations
// and attempt history exactly as the contract describes (P1 T6).
type p3MemLedger struct {
	mu       sync.Mutex
	jobs     map[uuid.UUID]*runner.Job
	attempts map[uuid.UUID][]runner.Attempt
	now      func() time.Time
}

var _ runner.Ledger = (*p3MemLedger)(nil)

func newP3Ledger(now func() time.Time) *p3MemLedger {
	return &p3MemLedger{jobs: map[uuid.UUID]*runner.Job{}, attempts: map[uuid.UUID][]runner.Attempt{}, now: now}
}

// put stores j verbatim; a job with a runner gets an open attempt.
func (l *p3MemLedger) put(j runner.Job) runner.Job {
	l.mu.Lock()
	defer l.mu.Unlock()
	if j.ID == uuid.Nil {
		j.ID = uuid.New()
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = l.now()
	}
	c := j
	l.jobs[j.ID] = &c
	if j.RunnerID != "" {
		l.attempts[j.ID] = append(l.attempts[j.ID], runner.Attempt{JobID: j.ID, Generation: j.Generation, RunnerID: j.RunnerID, SourceID: j.SourceID, StartedAt: l.now()})
	}
	return j
}

func (l *p3MemLedger) EnsureIntent(_ context.Context, key runner.JobKey, nodeID string, req runner.ExecRequirements) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, j := range l.jobs {
		if j.Key == key {
			return *j, nil
		}
	}
	j := &runner.Job{ID: uuid.New(), Key: key, NodeID: nodeID, Op: req.Op, Group: req.Group, Model: req.Model, State: runner.JobQueued, CreatedAt: l.now(), UpdatedAt: l.now()}
	l.jobs[j.ID] = j
	return *j, nil
}

func (l *p3MemLedger) SetWait(_ context.Context, id uuid.UUID, w *runner.WaitReason) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return runner.ErrJobNotFound
	}
	j.Wait = w
	return nil
}

func (l *p3MemLedger) Reserve(_ context.Context, id uuid.UUID, d runner.Decision, limit int, lease time.Time) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return runner.Job{}, runner.ErrJobNotFound
	}
	if j.State != runner.JobQueued {
		return runner.Job{}, fmt.Errorf("reserve: job %s is %s, not queued", id, j.State)
	}
	n := 0
	for _, o := range l.jobs {
		if o.RunnerID == d.Runner && (o.State == runner.JobOffered || o.State == runner.JobRunning) {
			n++
		}
	}
	if n >= limit {
		return runner.Job{}, runner.ErrSlotTaken
	}
	j.RunnerID, j.SourceID, j.Reason, j.Wait = d.Runner, d.Source, d.Reason, nil
	j.Generation++
	j.State = runner.JobOffered
	lu := lease
	j.LeaseUntil = &lu
	j.UpdatedAt = l.now()
	l.attempts[id] = append(l.attempts[id], runner.Attempt{JobID: id, Generation: j.Generation, RunnerID: d.Runner, SourceID: d.Source, Reason: d.Reason, StartedAt: l.now()})
	return *j, nil
}

func (l *p3MemLedger) live(f runner.Fence) (*runner.Job, error) {
	j, ok := l.jobs[f.JobID]
	if !ok || j.RunnerID != f.RunnerID || j.Generation != f.Generation || (j.State != runner.JobOffered && j.State != runner.JobRunning) {
		return nil, runner.ErrFenced
	}
	return j, nil
}

func (l *p3MemLedger) end(j *runner.Job, outcome string) {
	as := l.attempts[j.ID]
	for i := len(as) - 1; i >= 0; i-- {
		if as[i].Generation == j.Generation && as[i].EndedAt == nil {
			t := l.now()
			as[i].EndedAt, as[i].Outcome = &t, outcome
			break
		}
	}
}

func (l *p3MemLedger) MarkRunning(_ context.Context, f runner.Fence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.live(f)
	if err != nil {
		return err
	}
	j.State = runner.JobRunning
	return nil
}

func (l *p3MemLedger) Complete(_ context.Context, f runner.Fence, ref string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.live(f)
	if err != nil {
		return err
	}
	j.State, j.ResultRef = runner.JobCompleted, ref
	l.end(j, "completed")
	return nil
}

func (l *p3MemLedger) Fail(_ context.Context, f runner.Fence, class runner.OutcomeClass) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.live(f)
	if err != nil {
		return err
	}
	j.State = runner.JobFailed
	l.end(j, string(class))
	return nil
}

func (l *p3MemLedger) Requeue(_ context.Context, f runner.Fence, class runner.OutcomeClass) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.live(f)
	if err != nil {
		return runner.Job{}, err
	}
	l.end(j, string(class))
	j.State, j.RunnerID, j.SourceID, j.LeaseUntil = runner.JobQueued, "", "", nil
	j.UpdatedAt = l.now()
	return *j, nil
}

func (l *p3MemLedger) FenceCheck(_ context.Context, f runner.Fence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.live(f)
	return err
}

func (l *p3MemLedger) ExtendLease(_ context.Context, f runner.Fence, until time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.live(f)
	if err != nil {
		return err
	}
	u := until
	j.LeaseUntil = &u
	return nil
}

func (l *p3MemLedger) Get(_ context.Context, id uuid.UUID) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return runner.Job{}, runner.ErrJobNotFound
	}
	return *j, nil
}

func (l *p3MemLedger) FindByKey(_ context.Context, key runner.JobKey) (runner.Job, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, j := range l.jobs {
		if j.Key == key {
			return *j, true, nil
		}
	}
	return runner.Job{}, false, nil
}

func (l *p3MemLedger) filter(keep func(runner.Job) bool) []runner.Job {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []runner.Job
	for _, j := range l.jobs {
		if keep(*j) {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.Before(out[b].CreatedAt) })
	return out
}

func activeState(s runner.JobState) bool {
	return s == runner.JobQueued || s == runner.JobOffered || s == runner.JobRunning
}

func (l *p3MemLedger) ListByRun(_ context.Context, run uuid.UUID) ([]runner.Job, error) {
	return l.filter(func(j runner.Job) bool { return j.Key.RunID == run }), nil
}

func (l *p3MemLedger) ListActive(context.Context) ([]runner.Job, error) {
	return l.filter(func(j runner.Job) bool { return activeState(j.State) }), nil
}

func (l *p3MemLedger) ListActiveByRunner(_ context.Context, id runner.RunnerID) ([]runner.Job, error) {
	return l.filter(func(j runner.Job) bool { return j.RunnerID == id && activeState(j.State) }), nil
}

func (l *p3MemLedger) Attempts(_ context.Context, id uuid.UUID) ([]runner.Attempt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]runner.Attempt(nil), l.attempts[id]...), nil
}

// p3MemHomes is an in-memory runner.HomeStore with P3 T3's semantics.
type p3MemHomes struct {
	mu    sync.Mutex
	homes map[uuid.UUID]runner.Home
	moves []runner.HomeMove
	now   func() time.Time
}

var _ runner.HomeStore = (*p3MemHomes)(nil)

func newP3Homes(now func() time.Time) *p3MemHomes {
	return &p3MemHomes{homes: map[uuid.UUID]runner.Home{}, now: now}
}

func (h *p3MemHomes) GetHome(_ context.Context, run uuid.UUID) (runner.Home, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if x, ok := h.homes[run]; ok {
		return x, nil
	}
	return runner.Home{RunID: run}, nil
}

func (h *p3MemHomes) moveLocked(run uuid.UUID, to runner.RunnerID, reason string, bytes, expect int64) (runner.Home, error) {
	cur, ok := h.homes[run]
	if !ok {
		cur = runner.Home{RunID: run}
	}
	if cur.Generation != expect {
		return runner.Home{}, runner.ErrHomeGenerationConflict
	}
	if cur.RunnerID == to && ok {
		return cur, nil
	}
	h.moves = append(h.moves, runner.HomeMove{RunID: run, From: cur.RunnerID, To: to, Reason: reason, Bytes: bytes, Generation: cur.Generation + 1, At: h.now()})
	cur.RunnerID, cur.Generation, cur.Dirty = to, cur.Generation+1, false
	h.homes[run] = cur
	return cur, nil
}

func (h *p3MemHomes) AcquireResource(_ context.Context, run uuid.UUID, owner runner.RunnerID, expect int64) (runner.Home, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[run]
	if cur.RunnerID == owner && cur.Generation > 0 {
		return cur, nil
	}
	if cur.RunnerID != "" && cur.RunnerID != owner {
		return runner.Home{}, runner.ErrHomeOwned
	}
	return h.moveLocked(run, owner, "acquire", 0, expect)
}

func (h *p3MemHomes) HandOff(_ context.Context, run uuid.UUID, to runner.RunnerID, reason string, bytes, expect int64, _ runner.HomeMoveCause) (runner.Home, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.moveLocked(run, to, reason, bytes, expect)
}

func (h *p3MemHomes) SetCheckpoint(_ context.Context, run uuid.UUID, v int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[run]
	cur.RunID = run
	if v > cur.CheckpointVersion {
		cur.CheckpointVersion = v
	}
	h.homes[run] = cur
	return nil
}

func (h *p3MemHomes) MarkDirty(_ context.Context, run uuid.UUID, dirty bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[run]
	cur.RunID, cur.Dirty = run, dirty
	h.homes[run] = cur
	return nil
}

func (h *p3MemHomes) History(_ context.Context, run uuid.UUID) ([]runner.HomeMove, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []runner.HomeMove
	for _, m := range h.moves {
		if m.RunID == run {
			out = append(out, m)
		}
	}
	return out, nil
}

func (h *p3MemHomes) ListByRunner(_ context.Context, id runner.RunnerID) ([]runner.Home, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []runner.Home
	for _, x := range h.homes {
		if x.RunnerID == id && id != "" {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].RunID.String() < out[b].RunID.String() })
	return out, nil
}

func (h *p3MemHomes) Release(_ context.Context, run uuid.UUID, expect int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.moveLocked(run, "", "release", 0, expect)
	return err
}

func (h *p3MemHomes) SetNonPortable(_ context.Context, run uuid.UUID, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[run]
	cur.RunID, cur.NonPortableReason = run, reason
	h.homes[run] = cur
	return nil
}

// BootstrapLocalCheckpoint is P3 T16's HomeStore addition (runs before this task).
func (h *p3MemHomes) BootstrapLocalCheckpoint(_ context.Context, run uuid.UUID, ref string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.homes[run]
	if cur.LocalCheckpoint != "" && cur.LocalCheckpoint != ref {
		return runner.ErrCheckpointConflict
	}
	cur.RunID, cur.LocalCheckpoint, cur.Dirty = run, ref, false
	h.homes[run] = cur
	return nil
}

// p3MemLeases is an in-memory runner.ResourceLeaseStore with P3 T3's evidence
// rules: no TTL; Supersede needs the old job fenced in the ledger AND the
// kind's evidence (stop log, liveness hook, earlier boot ID).
type p3MemLeases struct {
	mu      sync.Mutex
	ledger  runner.Ledger
	bootID  string
	lost    func(runner.RunnerID) bool // runner_lost / revoked evidence; nil = never proven
	active  map[string]runner.LeaseRecord
	gen     map[string]int64
	stopped map[runner.Fence]bool // mirrors builtin_processes.state = 'stopped'
	absent  map[runner.Fence]bool // mirrors runner_job_absences (decision D6)
	trail   *[]string
}

var _ runner.ResourceLeaseStore = (*p3MemLeases)(nil)

func newP3Leases(l runner.Ledger, bootID string, trail *[]string) *p3MemLeases {
	return &p3MemLeases{ledger: l, bootID: bootID, active: map[string]runner.LeaseRecord{}, gen: map[string]int64{},
		stopped: map[runner.Fence]bool{}, absent: map[runner.Fence]bool{}, trail: trail}
}

func (m *p3MemLeases) note(s string) {
	if m.trail != nil {
		*m.trail = append(*m.trail, s)
	}
}

// MarkAbsent mirrors RemoteInboxRepo.RecordAbsent: the runner reconnected
// without the job (decision D6).
func (m *p3MemLeases) MarkAbsent(f runner.Fence) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.absent[f] = true
}

// MarkStopped is called by the stopper fake after a confirmed stop.
func (m *p3MemLeases) MarkStopped(f runner.Fence) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped[f] = true
}

func (m *p3MemLeases) grantLocked(key, holder string, owner runner.RunnerID, job *runner.Fence, boot string) engine.ResourceFenceRef {
	m.gen[key]++
	ref := engine.ResourceFenceRef{Key: key, Owner: string(owner), Generation: m.gen[key], Token: uuid.New()}
	m.active[key] = runner.LeaseRecord{Fence: ref, Holder: holder, Job: job, BootID: boot}
	return ref
}

// seedServerClaim plants a server-side claim taken by an earlier server boot.
func (m *p3MemLeases) seedServerClaim(key, holder, boot string) engine.ResourceFenceRef {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.grantLocked(key, holder, runner.BuiltInRunnerID, nil, boot)
}

func (m *p3MemLeases) Claim(_ context.Context, key, holder string, owner runner.RunnerID) (engine.ResourceFenceRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.active[key]; ok {
		return engine.ResourceFenceRef{}, runner.ErrResourceBusy
	}
	m.note("claim")
	return m.grantLocked(key, holder, owner, nil, m.bootID), nil
}

func (m *p3MemLeases) ClaimJob(ctx context.Context, key, holder string, job runner.Fence) (engine.ResourceFenceRef, error) {
	if err := m.ledger.FenceCheck(ctx, job); err != nil {
		return engine.ResourceFenceRef{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.active[key]; ok {
		if rec.Job != nil && *rec.Job == job && rec.Holder == holder {
			return rec.Fence, nil
		}
		return engine.ResourceFenceRef{}, runner.ErrResourceBusy
	}
	j := job
	return m.grantLocked(key, holder, job.RunnerID, &j, ""), nil
}

func (m *p3MemLeases) checkLocked(f engine.ResourceFenceRef) error {
	if rec, ok := m.active[f.Key]; !ok || rec.Fence != f {
		return runner.ErrResourceFenced
	}
	return nil
}

func (m *p3MemLeases) Check(_ context.Context, f engine.ResourceFenceRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkLocked(f)
}

func (m *p3MemLeases) Active(_ context.Context, key string) (runner.LeaseRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.active[key]
	return rec, ok, nil
}

func (m *p3MemLeases) Release(_ context.Context, f engine.ResourceFenceRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(f); err != nil {
		return err
	}
	delete(m.active, f.Key)
	m.note("release")
	return nil
}

func (m *p3MemLeases) Supersede(ctx context.Context, old engine.ResourceFenceRef, proof runner.SupersedeProof, holder string, owner runner.RunnerID) (engine.ResourceFenceRef, error) {
	m.mu.Lock()
	rec, ok := m.active[old.Key]
	m.mu.Unlock()
	if !ok || rec.Fence != old {
		return engine.ResourceFenceRef{}, runner.ErrResourceFenced
	}
	if (rec.Job == nil) != (proof.OldJob == nil) || (rec.Job != nil && *rec.Job != *proof.OldJob) {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: proof does not name the claim's job", runner.ErrSupersedeUnproven)
	}
	if rec.Job != nil && m.ledger.FenceCheck(ctx, *rec.Job) == nil {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: old job still authorized", runner.ErrSupersedeUnproven)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	remote := rec.Fence.Owner != string(runner.BuiltInRunnerID)
	proven := false
	switch proof.Kind {
	case runner.SupersedeRunnerLost, runner.SupersedeRevoked:
		proven = remote && m.lost != nil && m.lost(runner.RunnerID(rec.Fence.Owner))
	case runner.SupersedeStopped:
		proven = rec.Job != nil && !remote && m.stopped[*rec.Job]
	case runner.SupersedeServerRestart:
		proven = rec.Job == nil && rec.BootID != m.bootID
	case runner.SupersedeReconnectAbsent:
		proven = rec.Job != nil && remote && m.absent[*rec.Job]
	}
	if !proven {
		return engine.ResourceFenceRef{}, fmt.Errorf("%w: %s", runner.ErrSupersedeUnproven, proof.Kind)
	}
	if err := m.checkLocked(old); err != nil {
		return engine.ResourceFenceRef{}, err
	}
	m.note("supersede")
	return m.grantLocked(old.Key, holder, owner, nil, m.bootID), nil
}

// p3Clock is a settable clock.
type p3Clock struct {
	mu sync.Mutex
	t  time.Time
}

func newP3Clock() *p3Clock { return &p3Clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)} }
func (c *p3Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *p3Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
