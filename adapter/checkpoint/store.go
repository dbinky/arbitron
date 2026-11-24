// adapter/checkpoint/store.go

// Package checkpoint stores immutable, versioned workspace checkpoint
// artifacts as tar+gzip archives with verified manifests (remote runners
// Phase 3). Artifacts are registered with a runner.CheckpointCatalog: only
// Commit (bootstrap) records committed progress; every other writer prepares a
// candidate that a validated transaction later promotes.
package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Limits bound what an archive may expand to.
type Limits struct {
	MaxEntries   int
	MaxBytes     int64
	MaxPathBytes int
}

// DefaultLimits are the production bounds.
var DefaultLimits = Limits{MaxEntries: 1 << 20, MaxBytes: 64 << 30, MaxPathBytes: 4096}

// Store lays artifacts out as
//
//	<dir>/runs/<run>/v<NNNNNNNNNN>.reserved        version reservation (O_EXCL)
//	<dir>/runs/<run>/v<NNNNNNNNNN>.tar.gz          immutable archive
//	<dir>/runs/<run>/v<NNNNNNNNNN>.manifest.json   its manifest
//	<dir>/cache/<run>/v<NNNNNNNNNN>/               verified read cache (ReadFile)
//	<dir>/staging/op-*/                            StageDelta output
//	<dir>/tmp/op-*/                                Commit/StageBuiltIn scratch
//
// Immutable artifacts never share a directory with mutable execution roots or
// staging. Nothing here deletes an artifact except Delete (workspace removal).
type Store struct {
	dir        string
	catalog    runner.CheckpointCatalog
	create     func(path string, perm os.FileMode) (io.WriteCloser, error)
	git        func(ctx context.Context, args ...string) ([]byte, error)
	limits     Limits
	pruneGrace time.Duration
	cacheMu    sync.Mutex
	runMu      sync.Mutex
	runLocks   map[uuid.UUID]*sync.Mutex
}

// Option configures a Store.
type Option func(*Store)

// WithCatalog sets the durable catalog. Without it every operation that reads
// or records checkpoints fails with runner.ErrNoCheckpointCatalog.
func WithCatalog(c runner.CheckpointCatalog) Option { return func(s *Store) { s.catalog = c } }

// WithFileCreator replaces the exclusive-create used for archives and
// manifests (fault injection). A returned writer with Sync() is fsynced
// before it is closed.
func WithFileCreator(fn func(path string, perm os.FileMode) (io.WriteCloser, error)) Option {
	return func(s *Store) { s.create = fn }
}

// WithGit replaces the git runner used by portable Git materialization.
func WithGit(fn func(ctx context.Context, args ...string) ([]byte, error)) Option {
	return func(s *Store) { s.git = fn }
}

// WithLimits replaces DefaultLimits.
func WithLimits(l Limits) Option { return func(s *Store) { s.limits = l } }

// NewStore returns a store rooted at dir. Production wiring MUST pass
// WithCatalog(sqlite.NewCheckpointCatalogRepo(db)).
func NewStore(dir string, opts ...Option) *Store {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	s := &Store{dir: abs, create: createExclusive, git: runGit, limits: DefaultLimits, pruneGrace: DefaultPruneGrace, runLocks: map[uuid.UUID]*sync.Mutex{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

func createExclusive(path string, perm os.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
}

func (s *Store) lockRun(id uuid.UUID) func() {
	s.runMu.Lock()
	m, ok := s.runLocks[id]
	if !ok {
		m = &sync.Mutex{}
		s.runLocks[id] = m
	}
	s.runMu.Unlock()
	m.Lock()
	return m.Unlock
}

func (s *Store) runDir(id uuid.UUID) string { return filepath.Join(s.dir, "runs", id.String()) }
func (s *Store) versionBase(id uuid.UUID, v int64) string {
	return filepath.Join(s.runDir(id), fmt.Sprintf("v%010d", v))
}
func (s *Store) archivePath(id uuid.UUID, v int64) string { return s.versionBase(id, v) + ".tar.gz" }
func (s *Store) manifestPath(id uuid.UUID, v int64) string {
	return s.versionBase(id, v) + ".manifest.json"
}

func (s *Store) scratch(kind string) (string, error) {
	base := filepath.Join(s.dir, kind)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("checkpoint: %s dir: %w", kind, err)
	}
	return os.MkdirTemp(base, "op-")
}

// refuseStorePath rejects a workspace path inside (or containing) the store,
// so execution roots and immutable artifacts never alias.
func (s *Store) refuseStorePath(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	workspace, err := resolveExistingPath(abs)
	if err != nil {
		return err
	}
	store, err := resolveExistingPath(s.dir)
	if err != nil {
		return err
	}
	if within(store, workspace) || within(workspace, store) {
		return fmt.Errorf("checkpoint: workspace %s overlaps the checkpoint store", p)
	}
	return nil
}

// resolveExistingPath resolves symlinked ancestors even for a not-yet-created
// restore destination or store directory.
func resolveExistingPath(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		return "", err
	}
	resolved, err = resolveExistingPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(p)), nil
}

// reserve allocates the next unused version for runID by exclusively creating
// its reservation marker. Versions are never reused; abandoned ones are gaps.
func (s *Store) reserve(ctx context.Context, runID uuid.UUID) (int64, error) {
	unlock := s.lockRun(runID)
	defer unlock()
	dir := s.runDir(runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("checkpoint: run dir: %w", err)
	}
	next := int64(1)
	if latest, _, ok, err := s.catalog.Latest(ctx, runID); err != nil {
		return 0, fmt.Errorf("checkpoint: latest: %w", err)
	} else if ok {
		next = latest.Version + 1
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	for _, e := range es {
		var v int64
		if _, err := fmt.Sscanf(e.Name(), "v%010d", &v); err == nil && v >= next {
			next = v + 1
		}
	}
	for ; ; next++ {
		f, err := os.OpenFile(s.versionBase(runID, next)+".reserved", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("checkpoint: reserve v%d: %w", next, err)
		}
		if err := f.Close(); err != nil {
			return 0, err
		}
		return next, nil
	}
}

type artifact struct {
	ref      runner.CheckpointRef
	manifest runner.Manifest
	archive  string
	size     int64
}

// build snapshots srcDir into an immutable,
// fsynced artifact at a freshly reserved version. Nothing is registered.
func (s *Store) build(ctx context.Context, runID uuid.UUID, srcDir string, portableGit bool) (artifact, error) {
	if err := s.refuseStorePath(srcDir); err != nil {
		return artifact{}, err
	}
	tmp, err := s.scratch("tmp")
	if err != nil {
		return artifact{}, err
	}
	defer forceRemoveAll(tmp)
	items, err := s.collect(ctx, srcDir, portableGit, tmp)
	if err != nil {
		return artifact{}, err
	}
	archive := filepath.Join(tmp, "archive.tar.gz")
	entries, size, err := s.writeArchiveFile(archive, items)
	if err != nil {
		return artifact{}, fmt.Errorf("checkpoint: write archive: %w", err)
	}
	version, err := s.reserve(ctx, runID)
	if err != nil {
		return artifact{}, err
	}
	m := runner.NewManifest(version, entries)
	if err := s.install(runID, m, archive); err != nil {
		return artifact{}, err
	}
	return artifact{
		ref:      runner.CheckpointRef{RunID: runID, Version: version, ManifestHash: m.Hash},
		manifest: m, archive: s.archivePath(runID, version), size: size,
	}, nil
}

// install writes the manifest next to an already fsynced archive, hard-links
// both to their immutable names and fsyncs the run directory.
func (s *Store) install(runID uuid.UUID, m runner.Manifest, archive string) error {
	mtmp := archive + ".manifest.json"
	if err := s.writeJSON(mtmp, m); err != nil {
		return fmt.Errorf("checkpoint: write manifest: %w", err)
	}
	if err := os.Link(archive, s.archivePath(runID, m.Version)); err != nil {
		return fmt.Errorf("checkpoint: link archive: %w", err)
	}
	if err := os.Link(mtmp, s.manifestPath(runID, m.Version)); err != nil {
		return fmt.Errorf("checkpoint: link manifest: %w", err)
	}
	return syncDir(s.runDir(runID))
}

func (s *Store) writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w, err := s.create(path, 0o600)
	if err != nil {
		return err
	}
	_, werr := w.Write(b)
	return finish(w, werr)
}

// finish fsyncs (when supported) and closes w, reporting the first error.
func finish(w io.WriteCloser, werr error) error {
	if werr == nil {
		if sy, ok := w.(interface{ Sync() error }); ok {
			werr = sy.Sync()
		}
	}
	cerr := w.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("checkpoint: fsync %s: %w", dir, err)
	}
	return nil
}

// Commit is BOOTSTRAP ONLY (P3 T6 EnsureBaseline before any dispatch): it
// records srcDir as the run's first committed baseline. The catalog rejects it
// when committed progress or an active writer exists.
func (s *Store) Commit(ctx context.Context, runID uuid.UUID, srcDir string) (runner.CheckpointRef, runner.Manifest, error) {
	if s.catalog == nil {
		return runner.CheckpointRef{}, runner.Manifest{}, runner.ErrNoCheckpointCatalog
	}
	art, err := s.build(ctx, runID, srcDir, true)
	if err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, err
	}
	c := runner.CheckpointCandidate{Ref: art.ref, ArchivePath: art.archive, SizeBytes: art.size}
	if err := c.ValidateBootstrap(); err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, err
	}
	if err := s.catalog.Bootstrap(ctx, c); err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, err
	}
	return art.ref, art.manifest, nil
}

// StageBuiltIn prepares srcDir as a candidate under the exact job and resource
// fences, based on the run's committed checkpoint. It never moves Latest.
func (s *Store) StageBuiltIn(ctx context.Context, runID uuid.UUID, srcDir string, f runner.Fence, resource engine.ResourceFenceRef) (runner.CheckpointRef, runner.Manifest, error) {
	if s.catalog == nil {
		return runner.CheckpointRef{}, runner.Manifest{}, runner.ErrNoCheckpointCatalog
	}
	if f.JobID == uuid.Nil || f.RunnerID == "" || f.Generation < 1 {
		return runner.CheckpointRef{}, runner.Manifest{}, fmt.Errorf("%w: built-in staging needs an exact job fence", runner.ErrInvalidCandidate)
	}
	if err := resource.Validate(); err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, fmt.Errorf("%w: %v", runner.ErrInvalidCandidate, err)
	}
	base, _, _, err := s.catalog.Latest(ctx, runID)
	if err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, fmt.Errorf("checkpoint: latest: %w", err)
	}
	art, err := s.build(ctx, runID, srcDir, true)
	if err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, err
	}
	job := f
	c := runner.CheckpointCandidate{Ref: art.ref, Base: base, Job: &job, Resource: resource, ArchivePath: art.archive, SizeBytes: art.size}
	if err := c.ValidatePrepared(); err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, err
	}
	if err := s.catalog.Publish(ctx, c); err != nil {
		return runner.CheckpointRef{}, runner.Manifest{}, err
	}
	return art.ref, art.manifest, nil
}

// Latest returns the run's COMMITTED checkpoint (the catalog's pointer).
func (s *Store) Latest(ctx context.Context, runID uuid.UUID) (runner.CheckpointRef, bool, error) {
	if s.catalog == nil {
		return runner.CheckpointRef{}, false, runner.ErrNoCheckpointCatalog
	}
	ref, _, ok, err := s.catalog.Latest(ctx, runID)
	if err != nil {
		return runner.CheckpointRef{}, false, fmt.Errorf("checkpoint: latest: %w", err)
	}
	return ref, ok, nil
}

// Manifest returns a committed checkpoint's verified manifest. The zero ref is
// the empty manifest.
func (s *Store) Manifest(ctx context.Context, ref runner.CheckpointRef) (runner.Manifest, error) {
	if ref.IsZero() {
		return runner.Manifest{}, nil
	}
	_, m, err := s.resolve(ctx, ref)
	return m, err
}

func (s *Store) readManifest(ref runner.CheckpointRef) (runner.Manifest, error) {
	b, err := os.ReadFile(s.manifestPath(ref.RunID, ref.Version))
	if errors.Is(err, fs.ErrNotExist) {
		return runner.Manifest{}, fmt.Errorf("%w: manifest for %s", runner.ErrCheckpointNotFound, ref)
	}
	if err != nil {
		return runner.Manifest{}, fmt.Errorf("checkpoint: read manifest: %w", err)
	}
	var m runner.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return runner.Manifest{}, fmt.Errorf("%w: %s: %v", runner.ErrCorruptCheckpoint, ref, err)
	}
	if m.Version != ref.Version || m.Hash != ref.ManifestHash {
		return runner.Manifest{}, fmt.Errorf("%w: manifest differs from ref %s", runner.ErrCorruptCheckpoint, ref)
	}
	if err := m.Verify(); err != nil {
		return runner.Manifest{}, err
	}
	return m, nil
}

// resolve maps a COMMITTED ref to its archive and verified manifest. Prepared
// candidates are not served (catalog Get returns committed rows only).
func (s *Store) resolve(ctx context.Context, ref runner.CheckpointRef) (string, runner.Manifest, error) {
	if s.catalog == nil {
		return "", runner.Manifest{}, runner.ErrNoCheckpointCatalog
	}
	got, path, err := s.catalog.Get(ctx, ref.RunID, ref.Version)
	if err != nil {
		return "", runner.Manifest{}, err
	}
	if got.ManifestHash != ref.ManifestHash {
		return "", runner.Manifest{}, fmt.Errorf("%w: catalog hash differs for %s", runner.ErrCorruptCheckpoint, ref)
	}
	if path != s.archivePath(ref.RunID, ref.Version) {
		return "", runner.Manifest{}, fmt.Errorf("%w: catalog path %q is not this store's artifact", runner.ErrCorruptCheckpoint, path)
	}
	m, err := s.readManifest(ref)
	if err != nil {
		return "", runner.Manifest{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return "", runner.Manifest{}, fmt.Errorf("%w: archive for %s: %v", runner.ErrCheckpointNotFound, ref, err)
	}
	return path, m, nil
}

// Restore extracts a committed checkpoint into a private sibling staging tree,
// verifies it against its manifest and only then replaces dstDir's contents.
// A failed or partial extraction leaves dstDir untouched. The zero ref
// restores an empty workspace.
func (s *Store) Restore(ctx context.Context, ref runner.CheckpointRef, dstDir string) error {
	if err := s.refuseStorePath(dstDir); err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: restore dir: %w", err)
	}
	if ref.IsZero() {
		return wipeDir(dstDir)
	}
	path, m, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}
	clean := filepath.Clean(dstDir)
	tmp, err := os.MkdirTemp(filepath.Dir(clean), "."+filepath.Base(clean)+".restore-")
	if err != nil {
		return fmt.Errorf("checkpoint: restore staging: %w", err)
	}
	defer forceRemoveAll(tmp)
	if err := extractVerified(path, tmp, m, s.limits); err != nil {
		return err
	}
	if err := wipeDir(clean); err != nil {
		return fmt.Errorf("checkpoint: clear %s: %w", clean, err)
	}
	es, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	for _, e := range es {
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(clean, e.Name())); err != nil {
			return fmt.Errorf("checkpoint: install %s: %w", e.Name(), err)
		}
	}
	return nil
}

// Delete removes every artifact and catalog row of a run (workspace removal,
// P3 T11 CheckpointDeleter).
func (s *Store) Delete(ctx context.Context, runID uuid.UUID) error {
	if s.catalog == nil {
		return runner.ErrNoCheckpointCatalog
	}
	unlock := s.lockRun(runID)
	defer unlock()
	if err := s.catalog.DeleteRun(ctx, runID); err != nil {
		return fmt.Errorf("checkpoint: delete catalog rows: %w", err)
	}
	forceRemoveAll(s.runDir(runID))
	forceRemoveAll(filepath.Join(s.dir, "cache", runID.String()))
	return nil
}

func wipeDir(dir string) error {
	es, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range es {
		p := filepath.Join(dir, e.Name())
		forceRemoveAll(p)
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("checkpoint: could not remove %s", p)
		}
	}
	return nil
}

// forceRemoveAll makes directories writable first so read-only trees (git
// objects, 0555 dirs) can be removed. It never follows symlinks.
func forceRemoveAll(p string) {
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(p)
}

// appended to adapter/checkpoint/store.go
func (s *Store) cachePath(id uuid.UUID, v int64) string {
	return filepath.Join(s.dir, "cache", id.String(), fmt.Sprintf("v%010d", v))
}

var _ runner.CheckpointStore = (*Store)(nil)

// StageDelta rebuilds the COMMITTED base in a private staging tree, applies
// the pushed deletions and changes, verifies the result equals the runner's
// declared manifest and writes the next archive. The base is never mutated and
// nothing is registered. The caller assigns Resource from its durable
// transfer session before Publish.
func (s *Store) StageDelta(ctx context.Context, runID uuid.UUID, base runner.CheckpointRef, delta io.Reader) (runner.StagedCheckpoint, error) {
	if s.catalog == nil {
		return runner.StagedCheckpoint{}, runner.ErrNoCheckpointCatalog
	}
	stage, err := s.scratch("staging")
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	ok := false
	defer func() {
		if !ok {
			forceRemoveAll(stage)
		}
	}()
	treeDir := filepath.Join(stage, "tree")
	if err := os.Mkdir(treeDir, 0o700); err != nil {
		return runner.StagedCheckpoint{}, err
	}
	root, err := os.OpenRoot(treeDir)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	defer root.Close()
	modes := map[string]fs.FileMode{}
	if !base.IsZero() {
		if base.RunID != runID {
			return runner.StagedCheckpoint{}, fmt.Errorf("%w: base %s belongs to another run", runner.ErrCheckpointConflict, base)
		}
		path, m, err := s.resolve(ctx, base)
		if err != nil {
			return runner.StagedCheckpoint{}, err
		}
		if modes, err = extractInto(path, root, m, s.limits); err != nil {
			return runner.StagedCheckpoint{}, err
		}
	}
	ctl, err := applyDelta(delta, root, modes, s.limits)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	if err := applyDirModes(root, modes); err != nil {
		return runner.StagedCheckpoint{}, err
	}
	items, err := s.collect(ctx, treeDir, false, "")
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	archive := filepath.Join(stage, "archive.tar.gz")
	entries, size, err := s.writeArchiveFile(archive, items)
	if err != nil {
		return runner.StagedCheckpoint{}, fmt.Errorf("checkpoint: write staged archive: %w", err)
	}
	if !runner.SameEntries(entries, ctl.Expected) {
		return runner.StagedCheckpoint{}, fmt.Errorf("%w: %s", runner.ErrManifestMismatch, firstDifference(entries, ctl.Expected))
	}
	version, err := s.reserve(ctx, runID)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	ok = true
	return runner.StagedCheckpoint{
		RunID: runID, Base: base, Version: version,
		Manifest: runner.NewManifest(version, entries),
		Dir:      stage, ArchivePath: archive, SizeBytes: size,
	}, nil
}

func firstDifference(got, want []runner.ManifestEntry) string {
	changed, deleted := runner.DiffManifests(want, got)
	switch {
	case len(changed) > 0:
		return "path " + changed[0].Path + " differs"
	case len(deleted) > 0:
		return "path " + deleted[0] + " missing"
	default:
		return "entry sets differ"
	}
}

// Publish PREPARES a staged checkpoint: it installs the fsynced artifact and
// registers a candidate carrying the exact job fence f, staged.Resource and
// staged.Base. It never moves Latest. The staging directory is removed whether
// or not registration succeeds; an unregistered installed artifact is an
// unreferenced orphan and never recovery state.
func (s *Store) Publish(ctx context.Context, staged runner.StagedCheckpoint, f runner.Fence) (runner.CheckpointRef, error) {
	if s.catalog == nil {
		return runner.CheckpointRef{}, runner.ErrNoCheckpointCatalog
	}
	if err := s.ownsStaging(staged); err != nil {
		return runner.CheckpointRef{}, err
	}
	defer forceRemoveAll(staged.Dir)
	if staged.Manifest.Version != staged.Version || staged.Manifest.Verify() != nil {
		return runner.CheckpointRef{}, fmt.Errorf("%w: staged manifest does not match v%d", runner.ErrCorruptCheckpoint, staged.Version)
	}
	job := f
	c := runner.CheckpointCandidate{
		Ref:  runner.CheckpointRef{RunID: staged.RunID, Version: staged.Version, ManifestHash: staged.Manifest.Hash},
		Base: staged.Base, Job: &job, Resource: staged.Resource,
		ArchivePath: s.archivePath(staged.RunID, staged.Version), SizeBytes: staged.SizeBytes,
	}
	if err := c.ValidatePrepared(); err != nil {
		return runner.CheckpointRef{}, err
	}
	if err := s.install(staged.RunID, staged.Manifest, staged.ArchivePath); err != nil {
		return runner.CheckpointRef{}, err
	}
	if err := s.catalog.Publish(ctx, c); err != nil {
		return runner.CheckpointRef{}, err
	}
	return c.Ref, nil
}

// Discard drops a staged checkpoint that will not be published.
func (s *Store) Discard(_ context.Context, staged runner.StagedCheckpoint) error {
	if err := s.ownsStaging(staged); err != nil {
		return err
	}
	forceRemoveAll(staged.Dir)
	return nil
}

func (s *Store) ownsStaging(staged runner.StagedCheckpoint) error {
	rel, err := filepath.Rel(filepath.Join(s.dir, "staging"), staged.Dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) ||
		staged.ArchivePath != filepath.Join(staged.Dir, "archive.tar.gz") {
		return fmt.Errorf("checkpoint: %q is not a staging directory of this store", staged.Dir)
	}
	return nil
}

// ReadFile opens one manifest-listed regular file of a COMMITTED checkpoint,
// served from a verified extraction of that immutable artifact. Directories,
// symlinks, unlisted paths and prepared candidates are refused; the mutable
// work dir is never read.
func (s *Store) ReadFile(ctx context.Context, ref runner.CheckpointRef, p string) (io.ReadCloser, error) {
	clean, err := runner.CleanRelPath(p)
	if err != nil {
		return nil, err
	}
	if ref.IsZero() {
		return nil, fmt.Errorf("%w: empty checkpoint has no %s", runner.ErrCheckpointNotFound, clean)
	}
	path, m, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	var entry *runner.ManifestEntry
	for i := range m.Entries {
		if m.Entries[i].Path == clean {
			entry = &m.Entries[i]
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("%w: %s in %s", runner.ErrCheckpointNotFound, clean, ref)
	}
	if !fs.FileMode(entry.Mode).IsRegular() {
		return nil, fmt.Errorf("checkpoint: %s is not a regular file", clean)
	}
	cache, err := s.ensureCache(ref, path, m)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cache)
	if err != nil {
		return nil, err
	}
	fi, err := root.Lstat(clean)
	if err != nil || !fi.Mode().IsRegular() {
		root.Close()
		return nil, fmt.Errorf("%w: cached %s is not a regular file", runner.ErrCorruptCheckpoint, clean)
	}
	f, err := root.Open(clean)
	if err != nil {
		root.Close()
		return nil, err
	}
	return rootFile{File: f, root: root}, nil
}

type rootFile struct {
	*os.File
	root *os.Root
}

func (f rootFile) Close() error {
	err := f.File.Close()
	f.root.Close()
	return err
}

func (s *Store) ensureCache(ref runner.CheckpointRef, archive string, m runner.Manifest) (string, error) {
	cache := s.cachePath(ref.RunID, ref.Version)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if fi, err := os.Stat(cache); err == nil && fi.IsDir() {
		return cache, nil
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(cache), "fill-")
	if err != nil {
		return "", err
	}
	if err := extractVerified(archive, tmp, m, s.limits); err != nil {
		forceRemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, cache); err != nil {
		forceRemoveAll(tmp)
		return "", err
	}
	return cache, nil
}

// StageResource writes srcDir as an immutable artifact for a server-side
// resource mutation (P3 T13 group mutation) on the committed base the caller
// holds under its claim. It registers nothing: P3 T13
// ResourceCommitRepo.CommitResource inserts the row as committed in its own
// transaction. Portable Git is materialized exactly as for StageBuiltIn
// (linked worktrees, alternates), so group workspaces that are linked
// worktrees are portable; RR-08 ErrPromisorObjectsMissing and every other
// walker error propagate unchanged. The returned StagedCheckpoint has
// no staging Dir: its ArchivePath is already the immutable artifact, so
// Publish/Discard do not apply.
func (s *Store) StageResource(ctx context.Context, runID uuid.UUID, srcDir string, base runner.CheckpointRef) (runner.StagedCheckpoint, error) {
	if s.catalog == nil {
		return runner.StagedCheckpoint{}, runner.ErrNoCheckpointCatalog
	}
	if !base.IsZero() && base.RunID != runID {
		return runner.StagedCheckpoint{}, fmt.Errorf("%w: base %s belongs to another run", runner.ErrCheckpointConflict, base)
	}
	art, err := s.build(ctx, runID, srcDir, true)
	if err != nil {
		return runner.StagedCheckpoint{}, err
	}
	return runner.StagedCheckpoint{
		RunID: runID, Base: base, Version: art.ref.Version, Manifest: art.manifest,
		ArchivePath: art.archive, SizeBytes: art.size,
	}, nil
}

// PreparedFor returns the exact prepared candidate of one job fence and
// resource claim from the catalog (P3 T6 FinishSuccess after a restart).
func (s *Store) PreparedFor(ctx context.Context, runID uuid.UUID, job runner.Fence, resource engine.ResourceFenceRef) (runner.CheckpointRef, bool, error) {
	if s.catalog == nil {
		return runner.CheckpointRef{}, false, runner.ErrNoCheckpointCatalog
	}
	return s.catalog.PreparedFor(ctx, runID, job, resource)
}
