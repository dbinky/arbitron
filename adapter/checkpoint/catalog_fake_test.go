// adapter/checkpoint/catalog_fake_test.go
package checkpoint_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

var errStaleResource = errors.New("memcatalog: stale resource fence")

type memRow struct {
	c     runner.CheckpointCandidate
	state string // prepared | committed
}

// memCatalog honours every load-bearing argument of runner.CheckpointCatalog:
// exact active job and resource fences, the committed base, unique versions,
// idempotent identical re-registration, a committed pointer that only
// Bootstrap and the test's validated publisher (commit) move, and
// committed-only Get. It is the store tests' stand-in for the SQLite catalog,
// which has its own tests in adapter/sqlite.
type memCatalog struct {
	mu           sync.Mutex
	rows         map[uuid.UUID]map[int64]*memRow
	pointer      map[uuid.UUID]int64
	jobs         map[runner.Fence]bool
	resources    map[engine.ResourceFenceRef]bool
	onPublish    func(c runner.CheckpointCandidate)
	retentionErr error
}

func newMemCatalog() *memCatalog {
	return &memCatalog{rows: map[uuid.UUID]map[int64]*memRow{}, pointer: map[uuid.UUID]int64{},
		jobs: map[runner.Fence]bool{}, resources: map[engine.ResourceFenceRef]bool{}}
}

// activate makes a job fence and a resource claim current, as the ledger and
// the P3 T3 lease would.
func (c *memCatalog) activate(f runner.Fence, r engine.ResourceFenceRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobs[f], c.resources[r] = true, true
}

func (c *memCatalog) deactivate(f runner.Fence, r engine.ResourceFenceRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.jobs, f)
	delete(c.resources, r)
}

func (c *memCatalog) Publish(_ context.Context, cand runner.CheckpointCandidate) error {
	if err := cand.ValidatePrepared(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.onPublish != nil {
		c.onPublish(cand)
	}
	if !c.jobs[*cand.Job] {
		return fmt.Errorf("%w: job %s gen %d", runner.ErrFenced, cand.Job.JobID, cand.Job.Generation)
	}
	if !c.resources[cand.Resource] {
		return errStaleResource
	}
	if cur := c.committedLocked(cand.Ref.RunID); cur != cand.Base {
		return fmt.Errorf("%w: base %q is not committed %q", runner.ErrCheckpointConflict, cand.Base, cur)
	}
	runRows := c.rows[cand.Ref.RunID]
	if runRows == nil {
		runRows = map[int64]*memRow{}
		c.rows[cand.Ref.RunID] = runRows
	}
	if have, ok := runRows[cand.Ref.Version]; ok {
		if have.state == "prepared" && sameCandidate(have.c, cand) {
			return nil
		}
		return fmt.Errorf("%w: v%d exists", runner.ErrCheckpointConflict, cand.Ref.Version)
	}
	runRows[cand.Ref.Version] = &memRow{c: cand, state: "prepared"}
	return nil
}

func sameCandidate(a, b runner.CheckpointCandidate) bool {
	return a.Ref == b.Ref && a.Base == b.Base && *a.Job == *b.Job && a.Resource == b.Resource &&
		a.ArchivePath == b.ArchivePath && a.SizeBytes == b.SizeBytes
}

func (c *memCatalog) Bootstrap(_ context.Context, cand runner.CheckpointCandidate) error {
	if err := cand.ValidateBootstrap(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pointer[cand.Ref.RunID] != 0 {
		return fmt.Errorf("%w: run already has committed progress", runner.ErrCheckpointConflict)
	}
	for r := range c.resources {
		if r.Key == "run:"+cand.Ref.RunID.String() {
			return fmt.Errorf("%w: an active writer holds the run", runner.ErrCheckpointConflict)
		}
	}
	runRows := c.rows[cand.Ref.RunID]
	if runRows == nil {
		runRows = map[int64]*memRow{}
		c.rows[cand.Ref.RunID] = runRows
	}
	if _, ok := runRows[cand.Ref.Version]; ok {
		return fmt.Errorf("%w: v%d exists", runner.ErrCheckpointConflict, cand.Ref.Version)
	}
	runRows[cand.Ref.Version] = &memRow{c: cand, state: "committed"}
	c.pointer[cand.Ref.RunID] = cand.Ref.Version
	return nil
}

// commit is the test's validated publisher (the P3 T5 transaction's
// promotion): only an exact prepared candidate on the committed base moves
// the pointer.
func (c *memCatalog) commit(ref runner.CheckpointRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[ref.RunID][ref.Version]
	if !ok || row.state != "prepared" || row.c.Ref != ref {
		return fmt.Errorf("%w: no prepared candidate %s", runner.ErrCheckpointNotFound, ref)
	}
	if c.committedLocked(ref.RunID) != row.c.Base {
		return runner.ErrCheckpointConflict
	}
	row.state = "committed"
	c.pointer[ref.RunID] = ref.Version
	return nil
}

func (c *memCatalog) committedLocked(run uuid.UUID) runner.CheckpointRef {
	v := c.pointer[run]
	if v == 0 {
		return runner.CheckpointRef{}
	}
	return c.rows[run][v].c.Ref
}

func (c *memCatalog) prepared(run uuid.UUID) []runner.CheckpointRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []runner.CheckpointRef
	for _, r := range c.rows[run] {
		if r.state == "prepared" {
			out = append(out, r.c.Ref)
		}
	}
	return out
}

func (c *memCatalog) Latest(_ context.Context, run uuid.UUID) (runner.CheckpointRef, string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.pointer[run]
	if v == 0 {
		return runner.CheckpointRef{}, "", false, nil
	}
	row := c.rows[run][v]
	return row.c.Ref, row.c.ArchivePath, true, nil
}

func (c *memCatalog) Get(_ context.Context, run uuid.UUID, v int64) (runner.CheckpointRef, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[run][v]
	if !ok || row.state != "committed" {
		return runner.CheckpointRef{}, "", fmt.Errorf("%w: %s v%d", runner.ErrCheckpointNotFound, run, v)
	}
	return row.c.Ref, row.c.ArchivePath, nil
}

func (c *memCatalog) DeleteRun(_ context.Context, run uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rows, run)
	delete(c.pointer, run)
	return nil
}

func (c *memCatalog) PreparedFor(_ context.Context, run uuid.UUID, job runner.Fence, res engine.ResourceFenceRef) (runner.CheckpointRef, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []runner.CheckpointRef
	for _, r := range c.rows[run] {
		if r.state == "prepared" && r.c.Job != nil && *r.c.Job == job && r.c.Resource == res {
			found = append(found, r.c.Ref)
		}
	}
	switch len(found) {
	case 0:
		return runner.CheckpointRef{}, false, nil
	case 1:
		return found[0], true, nil
	default:
		return runner.CheckpointRef{}, false, fmt.Errorf("%w: %d prepared candidates", runner.ErrCheckpointConflict, len(found))
	}
}

// commitResource is the test's stand-in for P3 T13 ResourceCommitRepo: it
// inserts a staged server-side artifact directly as committed on its base.
func (c *memCatalog) commitResource(staged runner.StagedCheckpoint) (runner.CheckpointRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.committedLocked(staged.RunID) != staged.Base {
		return runner.CheckpointRef{}, runner.ErrCheckpointConflict
	}
	ref := runner.CheckpointRef{RunID: staged.RunID, Version: staged.Version, ManifestHash: staged.Manifest.Hash}
	if c.rows[staged.RunID] == nil {
		c.rows[staged.RunID] = map[int64]*memRow{}
	}
	c.rows[staged.RunID][ref.Version] = &memRow{c: runner.CheckpointCandidate{Ref: ref, Base: staged.Base, ArchivePath: staged.ArchivePath, SizeBytes: staged.SizeBytes}, state: "committed"}
	c.pointer[staged.RunID] = ref.Version
	return ref, nil
}

// appended to adapter/checkpoint/catalog_fake_test.go
// Retention reports every row, the pointer and whether a run:<uuid> claim is
// active; retentionErr simulates a catalog outage.
func (c *memCatalog) Retention(_ context.Context, run uuid.UUID) (runner.CheckpointRetention, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retentionErr != nil {
		return runner.CheckpointRetention{}, c.retentionErr
	}
	ret := runner.CheckpointRetention{Versions: map[int64]bool{}, Committed: c.pointer[run]}
	for v := range c.rows[run] {
		ret.Versions[v] = true
	}
	for r := range c.resources {
		if r.Key == "run:"+run.String() {
			ret.Held = true
		}
	}
	return ret, nil
}
