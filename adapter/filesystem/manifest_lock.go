package filesystem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/gofrs/flock"
)

const lockPollInterval = 5 * time.Millisecond

type manifestLocks struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}
type lockContextKey struct{}
type ownedManifestLocks struct {
	repo   *WorkflowRepo
	keys   map[string]bool
	active atomic.Bool
}

var _ workflow.MutationCoordinator = (*WorkflowRepo)(nil)

// internalArtifact compares case-insensitively: on APFS and NTFS a
// differently cased name resolves to the same directory entry.
func internalArtifact(name string) bool {
	name = strings.ToLower(name)
	return name == ".arbitron-locks" || name == ".arbitron-layout-journal" || name == ".arbitron-tree-commits" || isTreeCopyArtifact(name)
}

func (r *WorkflowRepo) lockKey(t workflow.MutationTarget) (string, error) {
	if t.Name == "" || t.Name == "." || t.Name == ".." || strings.ContainsAny(t.Name, "/\\\x00") || internalArtifact(t.Name) {
		return "", workflow.ErrInvalidName
	}
	if _, err := r.resolveWorkflowDir(t.Scope, t.Name); err != nil {
		return "", err
	}
	if t.Scope.Kind == workflow.ScopeShared {
		return "shared/" + t.Name, nil
	}
	return "private/" + t.Scope.OwnerID.String() + "/" + t.Name, nil
}

func (r *WorkflowRepo) owns(ctx context.Context, key string) bool {
	o, _ := ctx.Value(lockContextKey{}).(*ownedManifestLocks)
	return o != nil && o.repo == r && o.active.Load() && o.keys[key]
}

// WithMutations holds a sorted, fixed set of destination locks for an entire
// authoring operation. Only this repository's live callback can reuse ownership.
func (r *WorkflowRepo) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unique := map[string]bool{}
	for _, t := range targets {
		key, err := r.lockKey(t)
		if err != nil {
			return err
		}
		unique[key] = true
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if o, _ := ctx.Value(lockContextKey{}).(*ownedManifestLocks); o != nil && o.repo == r {
		if !o.active.Load() {
			return fmt.Errorf("filesystem: expired mutation ownership")
		}
		for _, key := range keys {
			if !o.keys[key] {
				return fmt.Errorf("filesystem: mutation lock set cannot expand")
			}
		}
		return fn(ctx)
	}
	var release []func()
	defer func() {
		for i := len(release) - 1; i >= 0; i-- {
			release[i]()
		}
	}()
	for _, key := range keys {
		r.locks.mu.Lock()
		if r.locks.gates == nil {
			r.locks.gates = map[string]chan struct{}{}
		}
		gate := r.locks.gates[key]
		if gate == nil {
			gate = make(chan struct{}, 1)
			r.locks.gates[key] = gate
		}
		r.locks.mu.Unlock()
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		release = append(release, func() { <-gate })
		lockDir := filepath.Join(r.basePath, ".arbitron-locks")
		if err := os.MkdirAll(lockDir, userScopeDirMode); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(key))
		file := flock.New(filepath.Join(lockDir, fmt.Sprintf("%x.lock", digest)))
		ok, err := file.TryLockContext(ctx, lockPollInterval)
		if err != nil {
			_ = file.Close()
			return err
		}
		if !ok {
			_ = file.Close()
			return ctx.Err()
		}
		release = append(release, func() { _ = file.Unlock(); _ = file.Close() })
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owned := &ownedManifestLocks{repo: r, keys: unique}
	owned.active.Store(true)
	defer owned.active.Store(false)
	return fn(context.WithValue(ctx, lockContextKey{}, owned))
}
