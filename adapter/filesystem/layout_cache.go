package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

type LayoutCache struct{ repo *WorkflowRepo }

func NewLayoutCache(r *WorkflowRepo) *LayoutCache { return &LayoutCache{repo: r} }

var _ layout.CachePublisher = (*LayoutCache)(nil)
var _ layout.CacheReader = (*LayoutCache)(nil)

func validateCache(wf *workflow.Workflow, e layout.CacheEntry) error {
	if e.Format != layout.CacheFormatV1 || e.Generation <= 0 || e.JobGeneration <= 0 || e.Revision != wf.SavedRevision() || !validJournalRevision(e.Revision) || e.Contract != layout.DefaultContract() || e.Geometry.Contract != e.Contract {
		return fmt.Errorf("filesystem: incompatible layout cache envelope")
	}
	if e.Verdict != layout.VerdictPassed && e.Verdict != layout.VerdictNeedsAttention && e.Verdict != layout.VerdictFailed {
		return fmt.Errorf("filesystem: invalid cache verdict")
	}
	graph, err := layout.BuildGraph(wf)
	if err != nil {
		return err
	}
	return e.Geometry.Validate(graph)
}
func (c *LayoutCache) current(ctx context.Context, s workflow.Scope, n string) (*workflow.Workflow, error) {
	// The caller already owns the destination lock; Load would skip its read gate.
	if c.repo.readSettler != nil {
		if err := c.repo.readSettler.Settle(ctx, s, n); err != nil {
			return nil, err
		}
	}
	return c.repo.Load(ctx, s, n)
}
func (c *LayoutCache) PublishCache(ctx context.Context, d layout.Destination, e layout.CacheEntry, fence func(context.Context) error) error {
	if c == nil || c.repo == nil || fence == nil {
		return fmt.Errorf("filesystem: cache requires repository and store fence")
	}
	return c.repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: d.Scope, Name: d.Name}}, func(owned context.Context) error {
		wf, err := c.current(owned, d.Scope, d.Name)
		if errors.Is(err, workflow.ErrNotFound) {
			return layout.ErrDestinationGone
		}
		if err != nil {
			return err
		}
		if wf.SavedRevision() != e.Revision || d.WorkflowID != uuid.Nil && wf.ID != d.WorkflowID {
			return layout.ErrNotCurrent
		}
		if err = validateCache(wf, e); err != nil {
			return err
		}
		if err = fence(owned); err != nil {
			return err
		}
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		dir, err := c.repo.resolveWorkflowDir(d.Scope, d.Name)
		if err != nil {
			return err
		}
		return c.repo.files.write(owned, filepath.Join(dir, LayoutCacheFile), data, 0600)
	})
}
func (c *LayoutCache) ReadCache(ctx context.Context, s workflow.Scope, n string) (entry layout.CacheEntry, ok bool, err error) {
	if c == nil || c.repo == nil {
		return entry, false, fmt.Errorf("filesystem: cache requires repository")
	}
	err = c.repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: n}}, func(owned context.Context) error {
		wf, e := c.current(owned, s, n)
		if errors.Is(e, workflow.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		dir, e := c.repo.resolveWorkflowDir(s, n)
		if e != nil {
			return e
		}
		data, e := os.ReadFile(filepath.Join(dir, LayoutCacheFile))
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if e = json.Unmarshal(data, &entry); e != nil {
			return e
		}
		if e = validateCache(wf, entry); e != nil {
			return e
		}
		ok = true
		return nil
	})
	if err != nil {
		return layout.CacheEntry{}, false, err
	}
	return
}
