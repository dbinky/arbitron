package layouttest

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// MemStore is the mutex-serialized reference implementation of Store for tests.
type MemStore struct {
	mu      sync.Mutex
	failure error
	rows    map[string]layout.Record
	results map[layout.Fence]layout.StoredResult
}

func NewMemStore() *MemStore {
	return &MemStore{rows: map[string]layout.Record{}, results: map[layout.Fence]layout.StoredResult{}}
}
func (m *MemStore) SetFailure(err error) { m.mu.Lock(); defer m.mu.Unlock(); m.failure = err }
func (m *MemStore) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.failure != nil {
		return fmt.Errorf("%w: %v", layout.ErrStoreUnavailable, m.failure)
	}
	return nil
}
func canonical(d layout.Destination) layout.Destination {
	if d.Scope.Kind == workflow.ScopeShared {
		d.Scope = workflow.Shared()
	}
	return d
}
func (m *MemStore) row(rev string) (layout.Record, error) {
	r, ok := m.rows[rev]
	if !ok {
		return r, layout.ErrRecordNotFound
	}
	return r, nil
}
func (m *MemStore) fenced(f layout.Fence, states ...layout.State) (layout.Record, error) {
	r, err := m.row(f.Revision)
	if err != nil {
		return r, err
	}
	if r.JobGeneration == f.Generation {
		for _, state := range states {
			if state == r.State {
				return r, nil
			}
		}
	}
	return r, layout.ErrStaleGeneration
}
func (m *MemStore) Request(ctx context.Context, req layout.NewRequest) (layout.Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return layout.Record{}, false, err
	}
	if r, ok := m.rows[req.Revision]; ok {
		return copyRecord(r), false, nil
	}
	if req.Revision == "" || len(req.Manifest) == 0 || req.AttemptLimit < 1 {
		return layout.Record{}, false, fmt.Errorf("layout: invalid stored request")
	}
	r := layout.Record{Revision: req.Revision, Destination: canonical(req.Destination), Reason: req.Reason, Manifest: req.Manifest, Retained: req.Retained, State: layout.StatePending, Stage: layout.StageQueued, JobGeneration: 1, AttemptLimit: req.AttemptLimit, Findings: []layout.Finding{}, Unreviewed: true, RequestedAt: req.RequestedAt.UTC(), UpdatedAt: req.RequestedAt.UTC()}
	m.rows[r.Revision] = copyRecord(r)
	return copyRecord(r), true, nil
}
func (m *MemStore) Get(ctx context.Context, rev string) (layout.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return layout.Record{}, err
	}
	r, err := m.row(rev)
	return copyRecord(r), err
}
func (m *MemStore) ListActive(ctx context.Context, d layout.Destination) ([]layout.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return nil, err
	}
	out := []layout.Record{}
	for _, r := range m.rows {
		if r.Destination.Key() == d.Key() && (r.State == layout.StatePending || r.State == layout.StateInProgress) {
			out = append(out, copyRecord(r))
		}
	}
	sortRows(out, false)
	return out, nil
}
func sortRows(rows []layout.Record, updated bool) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].RequestedAt, rows[j].RequestedAt
		if updated {
			a, b = rows[i].UpdatedAt, rows[j].UpdatedAt
		}
		if a.Equal(b) {
			return rows[i].Revision < rows[j].Revision
		}
		return a.Before(b)
	})
}
func claimable(r layout.Record, now time.Time) bool {
	return r.State == layout.StatePending || (r.State == layout.StateInProgress && r.LeaseExpiresAt != nil && r.LeaseExpiresAt.Before(now))
}
func (m *MemStore) Claimable(ctx context.Context, now time.Time, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return nil, err
	}
	rows := []layout.Record{}
	for _, r := range m.rows {
		if claimable(r, now) {
			rows = append(rows, r)
		}
	}
	sortRows(rows, false)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Revision
	}
	return out, nil
}
func (m *MemStore) Claim(ctx context.Context, rev, owner string, now, until time.Time) (layout.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return layout.Record{}, err
	}
	r, err := m.row(rev)
	if err != nil {
		return r, err
	}
	if !claimable(r, now) {
		return layout.Record{}, layout.ErrNotClaimable
	}
	if r.State == layout.StateInProgress {
		r.JobGeneration++
		r.AttemptsUsed = 0
	}
	r.State = layout.StateInProgress
	r.Stage = layout.StageArranging
	r.LeaseOwner = owner
	r.LeaseExpiresAt = copyTime(&until)
	r.StartedAt = copyTime(&now)
	r.UpdatedAt = now.UTC()
	m.rows[rev] = r
	return copyRecord(r), nil
}
func (m *MemStore) Progress(ctx context.Context, f layout.Fence, stage layout.Stage, attempt int, now, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	r, err := m.fenced(f, layout.StateInProgress)
	if err != nil {
		return err
	}
	r.Stage = stage
	r.AttemptsUsed = attempt
	r.UpdatedAt = now.UTC()
	r.LeaseExpiresAt = copyTime(&until)
	m.rows[f.Revision] = r
	return nil
}

// insert validates before touching either map, preserving atomicity on conflicts.
func (m *MemStore) insert(f layout.Fence, result layout.StoredResult) error {
	if result.Revision != f.Revision || result.Generation != f.Generation {
		return fmt.Errorf("layout: result does not match fence")
	}
	result.CreatedAt = result.CreatedAt.UTC()
	if old, ok := m.results[f]; ok {
		if !reflect.DeepEqual(old, result) {
			return fmt.Errorf("layout: conflicting immutable result")
		}
		return nil
	}
	m.results[f] = copyResult(result)
	return nil
}
func replace(r *layout.Record, result layout.StoredResult) {
	r.ResultGeneration = result.Generation
	r.Verdict = result.Verdict
	r.Findings = result.UnresolvedFindings()
	r.Unreviewed = result.IsUnreviewed()
	r.CacheGeneration = 0
}
func (m *MemStore) Complete(ctx context.Context, f layout.Fence, c layout.Completion, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	r, err := m.fenced(f, layout.StateInProgress, layout.StateSuperseded)
	if err != nil {
		return err
	}
	if !c.State.Terminal() {
		return fmt.Errorf("layout: completion requires a terminal state")
	}
	if c.Result != nil {
		if err = m.insert(f, *c.Result); err != nil {
			return err
		}
		if c.ReplaceBest {
			replace(&r, *c.Result)
		}
	}
	if r.State != layout.StateSuperseded {
		r.State = c.State
	}
	r.Stage = layout.StageDone
	r.AttemptsUsed = c.AttemptsUsed
	r.FailureStage = c.FailureStage
	r.FailureReason = c.FailureReason
	r.LeaseOwner = ""
	r.LeaseExpiresAt = nil
	r.FinishedAt = copyTime(&now)
	r.UpdatedAt = now.UTC()
	m.rows[f.Revision] = r
	return nil
}
func (m *MemStore) Checkpoint(ctx context.Context, f layout.Fence, result layout.StoredResult, replaceBest bool, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	r, err := m.fenced(f, layout.StateInProgress)
	if err != nil {
		return err
	}
	if err = m.insert(f, result); err != nil {
		return err
	}
	if replaceBest {
		replace(&r, result)
	}
	r.UpdatedAt = now.UTC()
	m.rows[f.Revision] = r
	return nil
}
func (m *MemStore) Supersede(ctx context.Context, rev string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	r, err := m.row(rev)
	if err != nil {
		return err
	}
	if r.State == layout.StatePending || r.State == layout.StateInProgress {
		r.State = layout.StateSuperseded
		r.UpdatedAt = now.UTC()
		m.rows[rev] = r
	}
	return nil
}
func (m *MemStore) Retry(ctx context.Context, rev string, limit int, now time.Time) (layout.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return layout.Record{}, err
	}
	r, err := m.row(rev)
	if err != nil {
		return r, err
	}
	if !(r.State == layout.StateNeedsAttention || r.State == layout.StateFailed || (r.State == layout.StateSuperseded && r.Verdict != layout.VerdictPassed)) {
		return layout.Record{}, layout.ErrRetryConflict
	}
	if limit < 1 {
		return layout.Record{}, fmt.Errorf("layout: invalid attempt limit")
	}
	r.JobGeneration++
	r.State = layout.StatePending
	r.Stage = layout.StageQueued
	r.AttemptsUsed = 0
	r.AttemptLimit = limit
	r.FailureStage = ""
	r.FailureReason = ""
	r.LeaseOwner = ""
	r.LeaseExpiresAt = nil
	r.StartedAt = nil
	r.FinishedAt = nil
	r.CacheGeneration = 0
	r.UpdatedAt = now.UTC()
	m.rows[rev] = r
	return copyRecord(r), nil
}
func (m *MemStore) Relocate(ctx context.Context, from, to layout.Destination, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return 0, err
	}
	to = canonical(to)
	var count int64
	for rev, r := range m.rows {
		if r.Destination.Key() != from.Key() {
			continue
		}
		target := to
		if target.WorkflowID == uuid.Nil {
			target.WorkflowID = r.Destination.WorkflowID
		}
		if r.Destination == target {
			continue
		}
		r.Destination = target
		r.CacheGeneration = 0
		r.UpdatedAt = now.UTC()
		m.rows[rev] = r
		count++
	}
	return count, nil
}
func (m *MemStore) Result(ctx context.Context, rev string, gen int64) (layout.StoredResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return layout.StoredResult{}, err
	}
	r, ok := m.results[layout.Fence{Revision: rev, Generation: gen}]
	if !ok {
		return layout.StoredResult{}, layout.ErrRecordNotFound
	}
	return copyResult(r), nil
}
func (m *MemStore) PendingCache(ctx context.Context, limit int) ([]layout.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return nil, err
	}
	out := []layout.Record{}
	for _, r := range m.rows {
		if r.ResultGeneration > 0 && r.CacheGeneration != r.ResultGeneration {
			out = append(out, copyRecord(r))
		}
	}
	sortRows(out, true)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *MemStore) MarkCached(ctx context.Context, f layout.Fence, gen int64, dest layout.Destination) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	r, err := m.row(f.Revision)
	if err != nil {
		return err
	}
	if r.JobGeneration != f.Generation || r.ResultGeneration != gen || gen < 1 || r.Destination != canonical(dest) {
		return layout.ErrStaleGeneration
	}
	r.CacheGeneration = gen
	m.rows[f.Revision] = r
	return nil
}
func (m *MemStore) InvalidateResult(ctx context.Context, f layout.Fence, gen int64, reason string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(ctx); err != nil {
		return err
	}
	r, err := m.row(f.Revision)
	if err != nil {
		return err
	}
	if r.JobGeneration != f.Generation || r.ResultGeneration != gen || !(r.State == layout.StatePassed || (r.State == layout.StateSuperseded && r.Verdict == layout.VerdictPassed)) {
		return layout.ErrStaleGeneration
	}
	r.State = layout.StateNeedsAttention
	r.FailureReason = reason
	r.UpdatedAt = now.UTC()
	m.rows[f.Revision] = r
	return nil
}

var _ layout.Store = (*MemStore)(nil)
