package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

var _ workflow.TreeCommitter = (*WorkflowRepo)(nil)

func (r *WorkflowRepo) treeRoot() string { return filepath.Join(r.basePath, ".arbitron-tree-commits") }
func (r *WorkflowRepo) treeIntent(id string) (workflow.TreeCommit, error) {
	var in workflow.TreeCommit
	if !validJournalID(id) {
		return in, fmt.Errorf("filesystem: invalid tree commit ID")
	}
	data, err := os.ReadFile(filepath.Join(r.treeRoot(), id, "ready.json"))
	if err != nil {
		return in, err
	}
	if err = json.Unmarshal(data, &in); err != nil {
		return in, err
	}
	if in.ID != id || !validJournalRevision(in.Revision) {
		return in, fmt.Errorf("filesystem: invalid tree intent")
	}
	if _, err = r.lockKey(in.Destination); err != nil {
		return in, err
	}
	return in, nil
}

// syncTree persists every file and then every directory, bottom-up. Symlinks
// cannot stand in for saved bytes outside the workflow's tree.
func syncTree(ctx context.Context, root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("filesystem: symbolic link in workflow tree: %s", path)
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		return errors.Join(f.Sync(), f.Close())
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}
func copyTreeExact(ctx context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		target := filepath.Join(dst, rel)
		info, e := d.Info()
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("filesystem: cannot back up symbolic link")
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("filesystem: cannot back up nonregular file")
		}
		if e = copyFile(path, target); e != nil {
			return e
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}

func (r *WorkflowRepo) PrepareTreeCommit(ctx context.Context, dest workflow.MutationTarget, rev string) (out workflow.TreeCommit, err error) {
	if !validJournalRevision(rev) {
		return out, fmt.Errorf("filesystem: invalid tree revision")
	}
	err = r.WithMutations(ctx, []workflow.MutationTarget{dest}, func(owned context.Context) error {
		dir, e := r.resolveWorkflowDir(dest.Scope, dest.Name)
		if e != nil {
			return e
		}
		out = workflow.TreeCommit{ID: uuid.NewString(), Destination: dest, Revision: rev}
		exists, e := pathExists(dir)
		if e != nil {
			return e
		}
		out.HadPrior = exists
		if exists {
			wf, e := r.load(owned, dest.Scope, dest.Name)
			if e != nil {
				return e
			}
			out.PriorRevision = wf.SavedRevision()
		}
		root := filepath.Join(r.treeRoot(), out.ID)
		if e = durableMkdir(root); e != nil {
			return e
		}
		ready := false
		defer func() {
			if !ready {
				_ = os.RemoveAll(root)
				_ = syncDir(r.treeRoot())
			}
		}()
		if exists {
			backup := filepath.Join(root, "backup")
			if e = copyTreeExact(owned, dir, backup); e != nil {
				return e
			}
			if e = syncTree(owned, backup); e != nil {
				return e
			}
		}
		data, e := json.Marshal(out)
		if e != nil {
			return e
		}
		if e = r.files.write(owned, filepath.Join(root, "ready.json"), data, 0600); e != nil {
			return e
		}
		ready = true
		return nil
	})
	return
}
func (r *WorkflowRepo) FinishTreeCommit(ctx context.Context, id string) error {
	in, err := r.treeIntent(id)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(filepath.Join(r.treeRoot(), id)); os.IsNotExist(statErr) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	return r.WithMutations(ctx, []workflow.MutationTarget{in.Destination}, func(context.Context) error {
		// Rename the entire decided intent before deleting bytes. A crash during
		// cleanup cannot turn a successful finish into an incomplete backup.
		dir := filepath.Join(r.treeRoot(), id)
		finished := filepath.Join(r.treeRoot(), ".finished-"+id)
		if err := os.Rename(dir, finished); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		if err := syncDir(r.treeRoot()); err != nil {
			return err
		}
		if err := os.RemoveAll(finished); err != nil {
			return err
		}
		return syncDir(r.treeRoot())
	})
}
func (r *WorkflowRepo) RollbackTreeCommit(ctx context.Context, id string) error {
	in, err := r.treeIntent(id)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(filepath.Join(r.treeRoot(), id)); os.IsNotExist(statErr) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	return r.WithMutations(ctx, []workflow.MutationTarget{in.Destination}, func(owned context.Context) error {
		dest := in.Destination
		dir, e := r.resolveWorkflowDir(dest.Scope, dest.Name)
		if e != nil {
			return e
		}
		current, e := r.load(owned, dest.Scope, dest.Name)
		if e != nil && !errors.Is(e, workflow.ErrNotFound) {
			return e
		}
		if e == nil && current.SavedRevision() != in.Revision && (!in.HadPrior || current.SavedRevision() != in.PriorRevision) {
			return fmt.Errorf("%w: tree advanced beyond rollback revision", workflow.ErrMutationPending)
		}
		if in.HadPrior {
			backup := filepath.Join(r.treeRoot(), id, "backup")
			if _, e := os.Stat(filepath.Join(backup, "workflow.json")); e != nil {
				return fmt.Errorf("filesystem: incomplete rollback backup: %w", e)
			}
			staging := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".tmp-"+id)
			defer os.RemoveAll(staging)
			if e = copyTreeExact(owned, backup, staging); e != nil {
				return e
			}
			if e = syncTree(owned, staging); e != nil {
				return e
			}
			aside := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".old-"+id)
			exists, e := pathExists(dir)
			if e != nil {
				return e
			}
			if exists {
				if e = r.files.renamePath(dir, aside); e != nil {
					return e
				}
				if e = syncDir(filepath.Dir(dir)); e != nil {
					return e
				}
			}
			if e = r.files.renamePath(staging, dir); e != nil {
				if exists {
					e = errors.Join(e, r.files.renamePath(aside, dir))
				}
				return e
			}
			if e = syncDir(filepath.Dir(dir)); e != nil {
				return e
			}
			if e = os.RemoveAll(aside); e != nil {
				return e
			}
		} else {
			if e = os.RemoveAll(dir); e != nil {
				return e
			}
			if e = syncDir(filepath.Dir(dir)); e != nil {
				return e
			}
		}
		return r.FinishTreeCommit(owned, id)
	})
}
func (r *WorkflowRepo) PendingTreeCommits(ctx context.Context) ([]workflow.TreeCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := os.ReadDir(r.treeRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []workflow.TreeCommit
	var errs []error
	for _, f := range files {
		if !f.IsDir() {
			continue
		}
		if strings.HasPrefix(f.Name(), ".finished-") && validJournalID(strings.TrimPrefix(f.Name(), ".finished-")) {
			if e := os.RemoveAll(filepath.Join(r.treeRoot(), f.Name())); e != nil {
				errs = append(errs, e)
			} else if e = syncDir(r.treeRoot()); e != nil {
				errs = append(errs, e)
			}
			continue
		}
		in, e := r.treeIntent(f.Name())
		if e != nil {
			errs = append(errs, fmt.Errorf("tree commit %s: %w", f.Name(), e))
			continue
		}
		out = append(out, in)
	}
	return out, errors.Join(errs...)
}
