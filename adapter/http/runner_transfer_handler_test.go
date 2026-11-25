package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// transferKeys authenticates fixed plaintext keys (honouring the key string).
type transferKeys map[string]runner.RunnerKey

func (k transferKeys) Authenticate(_ context.Context, plaintext string) (runner.RunnerKey, error) {
	key, ok := k[plaintext]
	if !ok {
		return runner.RunnerKey{}, runner.ErrKeyNotFound
	}
	return key, nil
}

// transferBundles serves bundles by exact hash.
type transferBundles map[string][]byte

func (b transferBundles) Open(_ context.Context, hash string) (io.ReadCloser, error) {
	data, ok := b[hash]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type transferBus struct {
	mu     sync.Mutex
	events []engine.ScopedEvent
}

func (b *transferBus) Publish(id uuid.UUID, ev engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, engine.ScopedEvent{InstanceID: id, Event: ev})
}
func (b *transferBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) { return nil, func() {} }
func (b *transferBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) { return nil, func() {} }

func (b *transferBus) lastProgress() *instance.ExecutionStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.events) - 1; i >= 0; i-- {
		if st, ok := b.events[i].Event.Data.(instance.ExecutionStatus); ok && st.Progress != nil {
			return &st
		}
	}
	return nil
}

const (
	bundleHash      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherBundleHash = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

type transferFixture struct {
	db      *sql.DB
	leases  *adaptsqlite.ResourceLeaseRepo
	res     engine.ResourceFenceRef
	ledger  *adaptsqlite.RunnerLedgerRepo
	homes   *adaptsqlite.WorkspaceHomeRepo
	store   *checkpoint.Store
	tokens  *runner.TransferTokenService
	bus     *transferBus
	srv     *httptest.Server
	staging string
	runID   uuid.UUID
	fence   runner.Fence
	base    runner.CheckpointRef
}

func newTransferFixture(t *testing.T, mutate ...func(*TransferHandlerDeps)) *transferFixture {
	t.Helper()
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "arbitron.db")) // file-backed: lease transactions use separate connections
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	reg := adaptsqlite.NewRunnerRegistryRepo(db)
	for _, id := range []runner.RunnerID{"mac-1", "mac-2"} {
		require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{ID: id, Name: string(id), StepLimit: 4,
			Liveness: "online", Compat: runner.Compatibility{State: "compatible"}, CreatedAt: time.Now().UTC()}))
	}
	f := &transferFixture{
		db: db, leases: adaptsqlite.NewResourceLeaseRepo(db, "boot-test"),
		ledger: adaptsqlite.NewRunnerLedgerRepo(db), homes: adaptsqlite.NewWorkspaceHomeRepo(db),
		store: checkpoint.NewStore(t.TempDir(), checkpoint.WithCatalog(adaptsqlite.NewCheckpointCatalogRepo(db))),
		bus:   &transferBus{}, staging: t.TempDir(), runID: uuid.New(),
	}
	f.tokens = runner.NewTransferTokenService(adaptsqlite.NewTransferSessionRepo(db), f.ledger, f.leases, time.Now)
	status := runner.NewExecutionStatusProjector(f.ledger)
	status.AttachBus(f.bus) // the real projector publishes moving_workspace progress on this bus
	wsSync := runner.NewWorkspaceSync(runner.SyncDeps{
		Checkpoints: f.store, Homes: f.homes, Leases: f.leases, Ledger: f.ledger, Tokens: f.tokens,
		Events: runner.NewEventRecorder(adaptsqlite.NewPoolEventRepo(db), f.bus, 100, time.Now),
		Status: status, Clock: time.Now,
	})

	server := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(server, "a.txt"), []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(server, "old.txt"), []byte("old"), 0o644))
	f.base, _, err = f.store.Commit(ctx, f.runID, server)
	require.NoError(t, err)
	_, err = f.homes.AcquireResource(ctx, f.runID, "mac-1", 0)
	require.NoError(t, err)
	job, err := f.ledger.EnsureIntent(ctx, runner.JobKey{RunID: f.runID, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}, "A",
		runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpType("process"), Affinity: runner.AffinityWorkspace, RunID: f.runID})
	require.NoError(t, err)
	job, err = f.ledger.Reserve(ctx, job.ID, runner.Decision{Placed: true, Runner: "mac-1", Reason: "home"}, 4, time.Now().Add(time.Hour))
	require.NoError(t, err)
	f.fence = runner.Fence{JobID: job.ID, RunnerID: "mac-1", Generation: job.Generation}
	// The run's live writer claim, taken by this exact job (P3 T3), after the
	// bootstrap baseline (Commit refuses while a writer is active).
	f.res, err = f.leases.ClaimJob(ctx, "run:"+f.runID.String(), runner.JobHolder(job), f.fence)
	require.NoError(t, err)

	deps := TransferHandlerDeps{
		Keys: transferKeys{
			"ark_mac1":      {RunnerID: "mac-1", MachinePublicKey: []byte{1}},
			"ark_mac2":      {RunnerID: "mac-2", MachinePublicKey: []byte{2}},
			"ark_unclaimed": {RunnerID: "mac-1"},
		},
		Tokens: f.tokens, Checkpoints: f.store, Sync: wsSync,
		Bundles:    transferBundles{bundleHash: []byte("bundle-bytes"), otherBundleHash: []byte("other-bytes")},
		StagingDir: f.staging, PartBytes: 64, StagingRetention: time.Hour, Clock: time.Now,
	}
	for _, m := range mutate {
		m(&deps)
	}
	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
	router := NewRouterFromDeps(RouterDeps{Config: &config.Config{}, Auth: auth, RunnerTransfers: NewRunnerTransferHandler(deps)})
	f.srv = httptest.NewUnstartedServer(router)
	f.srv.Config.ReadTimeout = 300 * time.Millisecond // production uses 10 s; transfers must lift it
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

// scope is the exact P3 T7 TransferScope for this job: pull/push pin the
// committed base, a bundle token pins bundleHash; both carry the live claim.
func (f *transferFixture) scope(direction string) runner.TransferScope {
	s := runner.TransferScope{Fence: f.fence, RunID: f.runID, Direction: direction, Resource: f.res}
	if direction == runner.TransferBundle {
		s.BundleHash = bundleHash
	} else {
		s.BaseRef = f.base.String()
	}
	return s
}

func (f *transferFixture) issueScope(t *testing.T, s runner.TransferScope) string {
	t.Helper()
	tok, err := f.tokens.Issue(context.Background(), s, time.Hour)
	require.NoError(t, err)
	return tok.Token
}

func (f *transferFixture) issue(t *testing.T, direction string) string {
	t.Helper()
	return f.issueScope(t, f.scope(direction))
}

func (f *transferFixture) do(t *testing.T, method, path, key, token string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+"/api/v1/runners/transfers"+path, body)
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("Authorization", "Runner "+key)
	}
	if token != "" {
		req.Header.Set(TransferTokenHeader, token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestRunnerTransfers_PullManifestAndFiles(t *testing.T) {
	f := newTransferFixture(t)
	pull := f.issue(t, runner.TransferPull)

	resp := f.do(t, http.MethodGet, "/manifest", "ark_mac1", pull, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "transfers authenticate with the runner key, not the user session")
	var m runner.Manifest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
	assert.Equal(t, f.base.Version, m.Version)
	assert.Equal(t, f.base.ManifestHash, m.Hash)
	var paths []string
	for _, e := range m.Entries {
		paths = append(paths, e.Path)
	}
	assert.Contains(t, paths, "a.txt")
	assert.Contains(t, paths, "old.txt")

	resp = f.do(t, http.MethodGet, "/files?path=a.txt", "ark_mac1", pull, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "one", readAll(t, resp))
	st := f.bus.lastProgress()
	require.NotNil(t, st, "pull progress is published as the moving-workspace status")
	assert.Equal(t, instance.ExecMovingWorkspace, st.State)
	assert.Equal(t, int64(3), st.Progress.BytesDone)
	assert.Equal(t, m.TotalBytes(), st.Progress.BytesTotal)

	assert.Equal(t, http.StatusBadRequest, f.do(t, http.MethodGet, "/files?path=..%2Fetc%2Fpasswd", "ark_mac1", pull, nil, nil).StatusCode)
	assert.Equal(t, http.StatusNotFound, f.do(t, http.MethodGet, "/files?path=missing.txt", "ark_mac1", pull, nil, nil).StatusCode)
}

func TestRunnerTransfers_RefusesWrongKeyTokenJobOrGeneration(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	pull := f.issue(t, runner.TransferPull)
	push := f.issue(t, runner.TransferPush)

	assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodGet, "/manifest", "", pull, nil, nil).StatusCode)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodGet, "/manifest", "ark_unknown", pull, nil, nil).StatusCode)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodGet, "/manifest", "ark_unclaimed", pull, nil, nil).StatusCode, "a key not bound to a machine cannot transfer")
	assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodGet, "/manifest", "ark_mac1", "", nil, nil).StatusCode)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/manifest", "ark_mac1", "art_forged", nil, nil).StatusCode)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/manifest", "ark_mac2", pull, nil, nil).StatusCode, "another runner's token")
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/manifest", "ark_mac1", push, nil, nil).StatusCode, "wrong direction")

	expiring, err := f.tokens.Issue(ctx, f.scope(runner.TransferPull), time.Millisecond)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/manifest", "ark_mac1", expiring.Token, nil, nil).StatusCode)

	// Failover re-queues the job: generation g's tokens are dead.
	_, err = f.ledger.Requeue(ctx, f.fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/manifest", "ark_mac1", pull, nil, nil).StatusCode)
}

func TestRunnerTransferHandler_ConstructionFailsClosed(t *testing.T) {
	assert.Panics(t, func() { NewRunnerTransferHandler(TransferHandlerDeps{}) })
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// runnerDelta restores the committed checkpoint into a runner copy, edits it
// and encodes the push exactly as the runner agent does.
func (f *transferFixture) runnerDelta(t *testing.T) []byte {
	t.Helper()
	ctx := context.Background()
	copyDir := t.TempDir()
	require.NoError(t, f.store.Restore(ctx, f.base, copyDir))
	baseEntries, err := checkpoint.ScanEntries(ctx, copyDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(copyDir, "a.txt"), []byte("two"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(copyDir, "old.txt")))
	require.NoError(t, os.MkdirAll(filepath.Join(copyDir, "dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(copyDir, "dir", "new.txt"), []byte("fresh"), 0o644))
	now, err := checkpoint.ScanEntries(ctx, copyDir)
	require.NoError(t, err)
	changed, deleted := runner.DiffManifests(baseEntries, now)
	paths := make([]string, 0, len(changed))
	for _, c := range changed {
		paths = append(paths, c.Path)
	}
	var buf bytes.Buffer
	require.NoError(t, checkpoint.WriteDelta(&buf, copyDir, paths, runner.DeltaControl{Deleted: deleted, Expected: now}))
	return buf.Bytes()
}

func split(b []byte, size int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n := size
		if n > len(b) {
			n = len(b)
		}
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

func (f *transferFixture) putPart(t *testing.T, token string, i int, data []byte, hash string) *http.Response {
	t.Helper()
	return f.do(t, http.MethodPut, "/push/"+strconv.Itoa(i), "ark_mac1", token, bytes.NewReader(data), map[string]string{PartSHA256Header: hash})
}

func (f *transferFixture) commitPush(t *testing.T, token string, parts int, whole string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"parts": parts, "sha256": whole})
	require.NoError(t, err)
	return f.do(t, http.MethodPost, "/push/commit", "ark_mac1", token, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
}

func TestRunnerTransfers_ResumablePushCommitsVerifiedCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	push := f.issue(t, runner.TransferPush)
	delta := f.runnerDelta(t)
	parts := split(delta, 64)
	require.GreaterOrEqual(t, len(parts), 2)

	assert.Equal(t, http.StatusUnprocessableEntity, f.putPart(t, push, 1, parts[1], sha([]byte("corrupt"))).StatusCode, "a part hash mismatch retries that part")
	for i := len(parts) - 1; i >= 0; i-- { // any order: resumable by index
		require.Equal(t, http.StatusOK, f.putPart(t, push, i, parts[i], sha(parts[i])).StatusCode)
	}
	require.Equal(t, http.StatusOK, f.putPart(t, push, 0, parts[0], sha(parts[0])).StatusCode, "re-sending a part is harmless")
	assert.Equal(t, http.StatusRequestEntityTooLarge, f.putPart(t, push, 0, bytes.Repeat([]byte("x"), 65), sha(bytes.Repeat([]byte("x"), 65))).StatusCode)
	require.Equal(t, http.StatusOK, f.putPart(t, push, 0, parts[0], sha(parts[0])).StatusCode)

	resp := f.commitPush(t, push, len(parts), sha(delta))
	body := readAll(t, resp) // read once: a failure message must not consume the success body
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var ref runner.CheckpointRef
	require.NoError(t, json.Unmarshal([]byte(body), &ref))
	assert.Equal(t, f.base.Version+1, ref.Version)

	// The push only PREPARES the exact candidate for this job and claim; the
	// validated step commit (P3 T5) promotes it later.
	prepared, found, err := f.store.PreparedFor(ctx, f.runID, f.fence, f.res)
	require.NoError(t, err)
	require.True(t, found, "the verified push is the job's prepared candidate")
	assert.Equal(t, ref, prepared)
	latest, _, err := f.store.Latest(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, f.base, latest, "a push commit never advances Latest")
	home, err := f.homes.GetHome(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, f.base.Version, home.CheckpointVersion, "nor the home's committed version")

	again := f.commitPush(t, push, len(parts), sha(delta))
	require.Equal(t, http.StatusOK, again.StatusCode, "a retried commit after a lost response is idempotent")
	var ref2 runner.CheckpointRef
	require.NoError(t, json.NewDecoder(again.Body).Decode(&ref2))
	assert.Equal(t, ref, ref2)
}

func TestRunnerTransfers_InterruptedUploadNeverPublishes(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	push := f.issue(t, runner.TransferPush)
	delta := f.runnerDelta(t)
	parts := split(delta, 64)
	require.Equal(t, http.StatusOK, f.putPart(t, push, 0, parts[0], sha(parts[0])).StatusCode)

	assert.Equal(t, http.StatusConflict, f.commitPush(t, push, len(parts), sha(delta)).StatusCode)
	for i := 1; i < len(parts); i++ {
		require.Equal(t, http.StatusOK, f.putPart(t, push, i, parts[i], sha(parts[i])).StatusCode)
	}
	assert.Equal(t, http.StatusUnprocessableEntity, f.commitPush(t, push, len(parts), sha([]byte("other"))).StatusCode)

	latest, _, err := f.store.Latest(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, f.base, latest, "nothing is published until the whole push verifies")
}

func TestRunnerTransfers_PushFromStaleGenerationRefused(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	push := f.issue(t, runner.TransferPush)
	delta := f.runnerDelta(t)
	parts := split(delta, 64)
	for i, p := range parts {
		require.Equal(t, http.StatusOK, f.putPart(t, push, i, p, sha(p)).StatusCode)
	}

	_, err := f.ledger.Requeue(ctx, f.fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, f.commitPush(t, push, len(parts), sha(delta)).StatusCode, "a re-queued generation cannot publish")
	assert.Equal(t, http.StatusForbidden, f.putPart(t, push, 0, parts[0], sha(parts[0])).StatusCode)
	latest, _, err := f.store.Latest(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, f.base, latest)
}

func TestRunnerTransfers_ReadDeadlineLiftedForSlowParts(t *testing.T) {
	f := newTransferFixture(t)
	push := f.issue(t, runner.TransferPush)
	data := bytes.Repeat([]byte("s"), 60)
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 4; i++ { // 4 x 150 ms: twice the server's 300 ms ReadTimeout
			time.Sleep(150 * time.Millisecond)
			_, _ = pw.Write(data[i*15 : (i+1)*15])
		}
		_ = pw.Close()
	}()
	resp := f.do(t, http.MethodPut, "/push/0", "ark_mac1", push, pr, map[string]string{PartSHA256Header: sha(data)})
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a transfer request must not be cut off by the server-wide read deadline")
}

func TestRunnerTransfers_BundlesByHash(t *testing.T) {
	f := newTransferFixture(t)
	bundle := f.issue(t, runner.TransferBundle)
	resp := f.do(t, http.MethodGet, "/bundles/"+bundleHash, "ark_mac1", bundle, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "bundle-bytes", readAll(t, resp))
	unknown := f.scope(runner.TransferBundle)
	unknown.BundleHash = sha([]byte("x"))
	assert.Equal(t, http.StatusNotFound, f.do(t, http.MethodGet, "/bundles/"+unknown.BundleHash, "ark_mac1", f.issueScope(t, unknown), nil, nil).StatusCode)
	assert.Equal(t, http.StatusBadRequest, f.do(t, http.MethodGet, "/bundles/not-a-hash", "ark_mac1", bundle, nil, nil).StatusCode)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/bundles/"+bundleHash, "ark_mac1", f.issue(t, runner.TransferPull), nil, nil).StatusCode)

	g := newTransferFixture(t, func(d *TransferHandlerDeps) { d.Bundles = nil })
	assert.Equal(t, http.StatusServiceUnavailable,
		g.do(t, http.MethodGet, "/bundles/"+bundleHash, "ark_mac1", g.issue(t, runner.TransferBundle), nil, nil).StatusCode)
}

func TestRunnerTransfers_StaleStagingIsSwept(t *testing.T) {
	f := newTransferFixture(t)
	stale := filepath.Join(f.staging, "abandoned")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))
	push := f.issue(t, runner.TransferPush)
	require.Equal(t, http.StatusOK, f.putPart(t, push, 0, []byte("p"), sha([]byte("p"))).StatusCode)
	assert.NoDirExists(t, stale, "staged parts past the retention expire")
}

// promote is the test's validated publisher: what P3 T5's step-commit
// transaction does to the catalog (promote the exact row and move the pointer).
func (f *transferFixture) promote(t *testing.T, ref runner.CheckpointRef) {
	t.Helper()
	tx, err := f.db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE workspace_checkpoints SET state = 'committed' WHERE run_id = ? AND version = ? AND manifest_hash = ? AND state = 'prepared'`,
		ref.RunID.String(), ref.Version, ref.ManifestHash)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE workspace_homes SET checkpoint_version = ? WHERE run_id = ?`, ref.Version, ref.RunID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func (f *transferFixture) pushAll(t *testing.T, token string) runner.CheckpointRef {
	t.Helper()
	delta := f.runnerDelta(t)
	parts := split(delta, 64)
	for i, p := range parts {
		require.Equal(t, http.StatusOK, f.putPart(t, token, i, p, sha(p)).StatusCode)
	}
	resp := f.commitPush(t, token, len(parts), sha(delta))
	body := readAll(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var ref runner.CheckpointRef
	require.NoError(t, json.Unmarshal([]byte(body), &ref))
	return ref
}

// A pull token pins ONE manifest: after a newer candidate is committed, the
// same token still serves the base it was issued for (manifest and files).
func TestRunnerTransfer_PullTokenPinsOneManifestAcrossNewCandidate(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	pull := f.issue(t, runner.TransferPull)
	next := f.pushAll(t, f.issue(t, runner.TransferPush))
	f.promote(t, next)
	latest, _, err := f.store.Latest(ctx, f.runID)
	require.NoError(t, err)
	require.Equal(t, next, latest, "precondition: a newer checkpoint is now committed")

	resp := f.do(t, http.MethodGet, "/manifest", "ark_mac1", pull, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var m runner.Manifest
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
	assert.Equal(t, f.base.Version, m.Version, "the pull token's manifest is its pinned base")
	assert.Equal(t, f.base.ManifestHash, m.Hash)
	file := f.do(t, http.MethodGet, "/files?path=a.txt", "ark_mac1", pull, nil, nil)
	require.Equal(t, http.StatusOK, file.StatusCode)
	assert.Equal(t, "one", readAll(t, file), "files come from the same pinned ref as the manifest")
	assert.Equal(t, http.StatusOK, f.do(t, http.MethodGet, "/files?path=old.txt", "ark_mac1", pull, nil, nil).StatusCode,
		"a file the newer candidate deleted is still served from the pinned base")
}

// A bundle token reads exactly its pinned bundle, even when another bundle exists.
func TestRunnerTransfer_BundleTokenRejectsOtherHash(t *testing.T) {
	f := newTransferFixture(t)
	bundle := f.issue(t, runner.TransferBundle)
	require.Equal(t, http.StatusOK, f.do(t, http.MethodGet, "/bundles/"+bundleHash, "ark_mac1", bundle, nil, nil).StatusCode)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/bundles/"+otherBundleHash, "ark_mac1", bundle, nil, nil).StatusCode,
		"a token for one bundle cannot read another, existing bundle")
}

// Once the run's claim is released or superseded, the old claim's push token
// can neither upload nor commit, and nothing is prepared.
func TestRunnerTransfer_StaleResourceTokenCannotPush(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	push := f.issue(t, runner.TransferPush)
	delta := f.runnerDelta(t)
	parts := split(delta, 64)
	require.Equal(t, http.StatusOK, f.putPart(t, push, 0, parts[0], sha(parts[0])).StatusCode)

	require.NoError(t, f.leases.Release(ctx, f.res))
	assert.Equal(t, http.StatusForbidden, f.putPart(t, push, 1, parts[1], sha(parts[1])).StatusCode)
	assert.Equal(t, http.StatusForbidden, f.commitPush(t, push, len(parts), sha(delta)).StatusCode)
	_, found, err := f.store.PreparedFor(ctx, f.runID, f.fence, f.res)
	require.NoError(t, err)
	assert.False(t, found, "a stale claim prepares nothing")
}

// A lost commit response is retried with the same token: it returns the SAME
// prepared ref (held by the durable session row) and prepares nothing new.
func TestRunnerTransfer_PushResponseLossReplaysSamePreparedRef(t *testing.T) {
	f := newTransferFixture(t)
	push := f.issue(t, runner.TransferPush)
	delta := f.runnerDelta(t)
	parts := split(delta, 64)
	for i, p := range parts {
		require.Equal(t, http.StatusOK, f.putPart(t, push, i, p, sha(p)).StatusCode)
	}
	first := f.commitPush(t, push, len(parts), sha(delta))
	require.Equal(t, http.StatusOK, first.StatusCode)
	var ref runner.CheckpointRef
	require.NoError(t, json.NewDecoder(first.Body).Decode(&ref))

	again := f.commitPush(t, push, len(parts), sha(delta))
	require.Equal(t, http.StatusOK, again.StatusCode)
	var ref2 runner.CheckpointRef
	require.NoError(t, json.NewDecoder(again.Body).Decode(&ref2))
	assert.Equal(t, ref, ref2)
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ? AND state = 'prepared'`, f.runID.String()).Scan(&n))
	assert.Equal(t, 1, n, "the replay prepares no second candidate")
}

// Commit returns a prepared ref only; Latest and the home stay on the base.
func TestRunnerTransfer_PushCommitDoesNotAdvanceLatest(t *testing.T) {
	ctx := context.Background()
	f := newTransferFixture(t)
	ref := f.pushAll(t, f.issue(t, runner.TransferPush))
	latest, _, err := f.store.Latest(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, f.base, latest)
	assert.NotEqual(t, ref, latest)
	home, err := f.homes.GetHome(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, f.base.Version, home.CheckpointVersion)
	_, err = f.store.Manifest(ctx, ref)
	assert.ErrorIs(t, err, runner.ErrCheckpointNotFound, "a prepared candidate is not served as committed")
}
