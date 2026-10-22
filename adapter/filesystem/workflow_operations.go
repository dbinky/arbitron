package filesystem

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

const LayoutCacheFile = "layout.json"

func reservedWorkflowFile(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	// Case-insensitive filesystems (APFS, NTFS) resolve Workflow.json to the
	// real manifest, so the guard must fold case too.
	if strings.EqualFold(parts[0], LayoutCacheFile) || strings.EqualFold(parts[0], "workflow.json") {
		return true
	}
	for _, part := range parts {
		if internalArtifact(part) {
			return true
		}
	}
	return false
}

type readSettlementError struct{ error }

func (e *readSettlementError) Unwrap() error { return e.error }

func withWorkflow[T any](r *WorkflowRepo, ctx context.Context, scope workflow.Scope, name string, fn func(context.Context) (T, error)) (out T, err error) {
	err = r.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: name}}, func(owned context.Context) error {
		var e error
		out, e = fn(owned)
		return e
	})
	return
}

func (r *WorkflowRepo) Load(ctx context.Context, scope workflow.Scope, name string) (*workflow.Workflow, error) {
	key, err := r.lockKey(workflow.MutationTarget{Scope: scope, Name: name})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	internal := r.owns(ctx, key)
	return withWorkflow(r, ctx, scope, name, func(owned context.Context) (*workflow.Workflow, error) {
		if !internal && r.readSettler != nil {
			if err := r.readSettler.Settle(owned, scope, name); err != nil {
				return nil, &readSettlementError{err}
			}
		}
		return r.load(owned, scope, name)
	})
}
func (r *WorkflowRepo) Create(ctx context.Context, s workflow.Scope, n string, w *workflow.Workflow) error {
	return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: n}}, func(c context.Context) error { return r.create(c, s, n, w) })
}
func (r *WorkflowRepo) Update(ctx context.Context, s workflow.Scope, n string, w *workflow.Workflow) error {
	return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: n}}, func(c context.Context) error { return r.update(c, s, n, w) })
}
func (r *WorkflowRepo) Delete(ctx context.Context, s workflow.Scope, n string) error {
	return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: n}}, func(c context.Context) error { return r.delete(c, s, n) })
}
func (r *WorkflowRepo) Exists(ctx context.Context, s workflow.Scope, n string) (bool, error) {
	return withWorkflow(r, ctx, s, n, func(c context.Context) (bool, error) { return r.exists(c, s, n) })
}
func (r *WorkflowRepo) WriteFile(ctx context.Context, s workflow.Scope, n, p string, b []byte) error {
	return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: n}}, func(c context.Context) error { return r.writeFile(c, s, n, p, b) })
}
func (r *WorkflowRepo) DeleteFile(ctx context.Context, s workflow.Scope, n, p string) error {
	return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: n}}, func(c context.Context) error { return r.deleteFile(c, s, n, p) })
}
func (r *WorkflowRepo) ListFiles(ctx context.Context, s workflow.Scope, n string) ([]string, error) {
	return withWorkflow(r, ctx, s, n, func(c context.Context) ([]string, error) { return r.listFiles(c, s, n) })
}
