// adapter/runneragent/transfer/transfertest/server.go
// Package transfertest provides a transfer.Transfer backed by a real
// checkpoint.Store, for runner-agent tests. Like P3 T7's handler, a push
// commit only PREPARES the job's exact candidate on the token's pinned base;
// Promote is the test's stand-in for the validated commit transaction (P3 T5).
package transfertest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
)

const (
	PullToken   = "pull-token"
	PushToken   = "push-token"
	BundleToken = "bundle-token"
)

// Server serves one run, written by one job fence under one resource claim.
// Tokens are checked on every call (pull token for reads, push token for
// writes, bundle token for bundles).
type Server struct {
	store *checkpoint.Store
	cat   *memCatalog
	runID uuid.UUID
	job   runner.Fence
	res   engine.ResourceFenceRef

	mu            sync.Mutex
	pinned        runner.CheckpointRef
	bundles       map[string][]byte
	parts         map[int][]byte
	fetches       []string
	manifests     int
	commits       int
	lastParts     int
	bundleFetches int
	failPart      int
	failTimes     int
}

var _ transfer.Transfer = (*Server)(nil)

func NewServer(storeDir string, runID uuid.UUID) *Server {
	cat := newMemCatalog()
	return &Server{
		store: checkpoint.NewStore(storeDir, checkpoint.WithCatalog(cat)),
		cat:   cat, runID: runID,
		job:     runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 1},
		res:     engine.ResourceFenceRef{Key: "run:" + runID.String(), Owner: "mac-1", Generation: 1, Token: uuid.New()},
		bundles: map[string][]byte{}, parts: map[int][]byte{}, failPart: -1,
	}
}

// Seed bootstraps dir as the run's first committed baseline (P3 T6 EnsureBaseline).
func (s *Server) Seed(ctx context.Context, dir string) (runner.CheckpointRef, error) {
	ref, _, err := s.store.Commit(ctx, s.runID, dir)
	return ref, err
}

// Plan returns the sync plan the hub would send for the committed checkpoint,
// with its pinned base and resource claim (P3 T8 BuildWorkspaceJob), and pins
// the transfer tokens to that base.
func (s *Server) Plan(ctx context.Context, workDir string, homeGen int64) (runneragent.WorkspaceJob, error) {
	ref, err := s.latest(ctx)
	if err != nil {
		return runneragent.WorkspaceJob{}, err
	}
	s.mu.Lock()
	s.pinned = ref
	s.mu.Unlock()
	return runneragent.WorkspaceJob{RunID: s.runID.String(), WorkDir: workDir, HomeGeneration: homeGen,
		CheckpointVersion: ref.Version, ManifestHash: ref.ManifestHash, BaseRef: ref.String(),
		FenceKey: s.res.Key, FenceOwner: s.res.Owner, FenceGeneration: s.res.Generation, FenceToken: s.res.Token.String(),
		PullToken: PullToken, PushToken: PushToken, PartBytes: 1 << 20}, nil
}

func (s *Server) latest(ctx context.Context) (runner.CheckpointRef, error) {
	ref, ok, err := s.store.Latest(ctx, s.runID)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	if !ok {
		return runner.CheckpointRef{}, errors.New("transfertest: no checkpoint")
	}
	return ref, nil
}

func (s *Server) pinnedBase() (runner.CheckpointRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinned.IsZero() {
		return runner.CheckpointRef{}, errors.New("transfertest: status 403: no plan issued")
	}
	return s.pinned, nil
}

// LatestManifest is the COMMITTED checkpoint's manifest.
func (s *Server) LatestManifest(ctx context.Context) (runner.Manifest, error) {
	ref, err := s.latest(ctx)
	if err != nil {
		return runner.Manifest{}, err
	}
	return s.store.Manifest(ctx, ref)
}

// ReadLatest reads one file of the COMMITTED checkpoint.
func (s *Server) ReadLatest(ctx context.Context, path string) ([]byte, error) {
	ref, err := s.latest(ctx)
	if err != nil {
		return nil, err
	}
	rc, err := s.store.ReadFile(ctx, ref, path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// PreparedFor returns this job's exact prepared candidate, if any.
func (s *Server) PreparedFor(ctx context.Context) (runner.CheckpointRef, bool, error) {
	return s.store.PreparedFor(ctx, s.runID, s.job, s.res)
}

// Promote is the validated commit transaction: only the exact prepared
// candidate on the committed base becomes Latest.
func (s *Server) Promote(_ context.Context, ref runner.CheckpointRef) error {
	return s.cat.commit(ref)
}

// PromotePrepared promotes this job's single prepared candidate.
func (s *Server) PromotePrepared(ctx context.Context) (runner.CheckpointRef, error) {
	ref, ok, err := s.PreparedFor(ctx)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	if !ok {
		return runner.CheckpointRef{}, errors.New("transfertest: no prepared candidate")
	}
	return ref, s.Promote(ctx, ref)
}

func (s *Server) AddBundle(hash string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundles[hash] = data
}

// FailPart makes PutPart(index) fail the next `times` calls.
func (s *Server) FailPart(index, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPart, s.failTimes = index, times
}

func (s *Server) ResetCounters() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches, s.manifests, s.bundleFetches = nil, 0, 0
}

func (s *Server) Fetches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fetches...)
}

func (s *Server) ManifestCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifests
}

// Commits counts accepted push commits (each PREPARES one candidate).
func (s *Server) Commits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

func (s *Server) LastCommitParts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastParts
}

func (s *Server) BundleFetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bundleFetches
}

var errToken = errors.New("transfertest: status 403: wrong transfer token")

func (s *Server) Manifest(ctx context.Context, token string) (runner.Manifest, error) {
	if token != PullToken {
		return runner.Manifest{}, errToken
	}
	base, err := s.pinnedBase()
	if err != nil {
		return runner.Manifest{}, err
	}
	s.mu.Lock()
	s.manifests++
	s.mu.Unlock()
	return s.store.Manifest(ctx, base)
}

func (s *Server) FetchFile(ctx context.Context, token, path string) (io.ReadCloser, error) {
	if token != PullToken {
		return nil, errToken
	}
	base, err := s.pinnedBase()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.fetches = append(s.fetches, path)
	s.mu.Unlock()
	return s.store.ReadFile(ctx, base, path)
}

func (s *Server) PutPart(_ context.Context, token string, i int, data []byte) error {
	if token != PushToken {
		return errToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i == s.failPart && s.failTimes > 0 {
		s.failTimes--
		return errors.New("transfertest: connection reset mid-transfer")
	}
	s.parts[i] = append([]byte(nil), data...)
	return nil
}

// CommitPush verifies the whole stream, stages it on the pinned base and
// PREPARES the job's candidate. It never moves Latest.
func (s *Server) CommitPush(ctx context.Context, token string, c transfer.PushCommit) (runner.CheckpointRef, error) {
	if token != PushToken {
		return runner.CheckpointRef{}, errToken
	}
	base, err := s.pinnedBase()
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	s.mu.Lock()
	var all []byte
	for i := 0; i < c.Parts; i++ {
		p, ok := s.parts[i]
		if !ok {
			s.mu.Unlock()
			return runner.CheckpointRef{}, fmt.Errorf("transfertest: status 409: missing part %d", i)
		}
		all = append(all, p...)
	}
	s.parts = map[int][]byte{}
	s.mu.Unlock()
	sum := sha256.Sum256(all)
	if hex.EncodeToString(sum[:]) != c.SHA256 {
		return runner.CheckpointRef{}, errors.New("transfertest: status 422: delta hash mismatch")
	}
	staged, err := s.store.StageDelta(ctx, s.runID, base, bytes.NewReader(all))
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	staged.Resource = s.res // from the durable session, never the archive
	ref, err := s.store.Publish(ctx, staged, s.job)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	s.mu.Lock()
	s.commits++
	s.lastParts = c.Parts
	s.mu.Unlock()
	return ref, nil
}

func (s *Server) FetchBundle(_ context.Context, token, hash string) (io.ReadCloser, error) {
	if token != BundleToken {
		return nil, errToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundleFetches++
	b, ok := s.bundles[hash]
	if !ok {
		return nil, errors.New("transfertest: status 404: bundle not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// memCatalog is an in-memory runner.CheckpointCatalog with prepared and
// committed rows. It checks the committed base, unique versions and the exact
// job/resource identity of PreparedFor. Liveness of the job and resource
// fences is P3 T2/T3's concern and is tested against SQLite there.
type memCatalog struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]map[int64]*catRow
	pointer map[uuid.UUID]int64
}

type catRow struct {
	c         runner.CheckpointCandidate
	committed bool
}

var _ runner.CheckpointCatalog = (*memCatalog)(nil)

func newMemCatalog() *memCatalog {
	return &memCatalog{rows: map[uuid.UUID]map[int64]*catRow{}, pointer: map[uuid.UUID]int64{}}
}

func (c *memCatalog) committedLocked(run uuid.UUID) runner.CheckpointRef {
	v := c.pointer[run]
	if v == 0 {
		return runner.CheckpointRef{}
	}
	return c.rows[run][v].c.Ref
}

func (c *memCatalog) insertLocked(cand runner.CheckpointCandidate, committed bool) error {
	run := c.rows[cand.Ref.RunID]
	if run == nil {
		run = map[int64]*catRow{}
		c.rows[cand.Ref.RunID] = run
	}
	if _, ok := run[cand.Ref.Version]; ok {
		return fmt.Errorf("%w: v%d exists", runner.ErrCheckpointConflict, cand.Ref.Version)
	}
	run[cand.Ref.Version] = &catRow{c: cand, committed: committed}
	return nil
}

func (c *memCatalog) Publish(_ context.Context, cand runner.CheckpointCandidate) error {
	if err := cand.ValidatePrepared(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.committedLocked(cand.Ref.RunID); cur != cand.Base {
		return fmt.Errorf("%w: base %q is not committed %q", runner.ErrCheckpointConflict, cand.Base, cur)
	}
	return c.insertLocked(cand, false)
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
	if err := c.insertLocked(cand, true); err != nil {
		return err
	}
	c.pointer[cand.Ref.RunID] = cand.Ref.Version
	return nil
}

// commit is the validated commit transaction's promotion.
func (c *memCatalog) commit(ref runner.CheckpointRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[ref.RunID][ref.Version]
	if !ok || row.committed || row.c.Ref != ref {
		return fmt.Errorf("%w: no prepared candidate %s", runner.ErrCheckpointNotFound, ref)
	}
	if c.committedLocked(ref.RunID) != row.c.Base {
		return runner.ErrCheckpointConflict
	}
	row.committed = true
	c.pointer[ref.RunID] = ref.Version
	return nil
}

func (c *memCatalog) Latest(_ context.Context, runID uuid.UUID) (runner.CheckpointRef, string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.pointer[runID]
	if v == 0 {
		return runner.CheckpointRef{}, "", false, nil
	}
	row := c.rows[runID][v]
	return row.c.Ref, row.c.ArchivePath, true, nil
}

func (c *memCatalog) Get(_ context.Context, runID uuid.UUID, v int64) (runner.CheckpointRef, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[runID][v]
	if !ok || !row.committed {
		return runner.CheckpointRef{}, "", runner.ErrCheckpointNotFound
	}
	return row.c.Ref, row.c.ArchivePath, nil
}

func (c *memCatalog) DeleteRun(_ context.Context, runID uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rows, runID)
	delete(c.pointer, runID)
	return nil
}

func (c *memCatalog) Retention(_ context.Context, runID uuid.UUID) (runner.CheckpointRetention, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := runner.CheckpointRetention{Versions: map[int64]bool{}, Committed: c.pointer[runID], Held: true}
	for v := range c.rows[runID] {
		r.Versions[v] = true
	}
	return r, nil
}

func (c *memCatalog) PreparedFor(_ context.Context, runID uuid.UUID, job runner.Fence, res engine.ResourceFenceRef) (runner.CheckpointRef, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []runner.CheckpointRef
	for _, row := range c.rows[runID] {
		if !row.committed && row.c.Job != nil && *row.c.Job == job && row.c.Resource == res {
			found = append(found, row.c.Ref)
		}
	}
	switch len(found) {
	case 0:
		return runner.CheckpointRef{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return runner.CheckpointRef{}, false, fmt.Errorf("%w: %d prepared candidates for one job", runner.ErrCheckpointConflict, len(found))
}
