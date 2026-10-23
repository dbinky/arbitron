package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const rootOld = "deepseek/deepseek-v4-flash"
const rootNew = "deepseek/deepseek-v4.1-flash"

// These leaves replace HTTP only. Settings, inventory, candidate collection,
// policy, proof transactions, cache publication and execution remain real.
type lifecycleHTTP struct {
	mu            sync.Mutex
	models        []string
	source        []string
	down          bool
	wrong         bool
	actual        map[string]string // Explicit execution-evidence overrides, including missing identity.
	fetches       int
	entered       chan struct{}
	executed      []string
	arrived       chan struct{}
	release       chan struct{}
	gate          sync.Once
	probeGate     sync.Once
	probeEntered  chan struct{}
	probeRelease  chan struct{}
	key           string
	tickerStopped chan struct{}
}

func (h *lifecycleHTTP) fetch(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
		return nil, fmt.Errorf("wrong source inputs")
	}
	h.mu.Lock()
	if h.entered != nil {
		entered := h.entered
		h.mu.Unlock()
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	defer h.mu.Unlock()
	h.fetches++
	if h.down {
		return nil, fmt.Errorf("source unavailable")
	}
	rows := map[string]any{}
	for _, m := range h.source {
		rows[m] = map[string]any{"id": m, "release_date": "2026-10-18"}
	}
	raw, err := json.Marshal(map[string]any{"openrouter": map[string]any{"id": "openrouter", "models": rows}})
	if err != nil {
		return nil, err
	}
	return lifecycleResponse(r, string(raw)), nil
}
func (h *lifecycleHTTP) provider(r *http.Request) (*http.Response, error) {
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct{ Role, Content string }
		Tools    []any
	}
	h.mu.Lock()
	key := h.key
	h.mu.Unlock()
	if key == "" {
		key = "lifecycle-key"
	}
	if r.Method != http.MethodPost || r.URL.String() != "https://provider.example.test/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" || body.Stream || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content == "" || len(body.Tools) != 0 {
		return nil, fmt.Errorf("missing execution inputs")
	}
	h.mu.Lock()
	index := slices.Index(h.source, body.Model)
	if index < 0 {
		h.mu.Unlock()
		return nil, fmt.Errorf("model is not in the execution fixture")
	}
	executedModel := h.source[index]
	h.models = append(h.models, body.Model)
	if body.Messages[0].Content == "hello" {
		h.executed = append(h.executed, body.Model)
	}
	arrived, release := h.arrived, h.release
	wrong := h.wrong
	actual, override := h.actual[body.Model]
	probeEntered, probeRelease := h.probeEntered, h.probeRelease
	h.mu.Unlock()
	if body.Messages[0].Content == "hello" && release != nil {
		h.gate.Do(func() {
			close(arrived)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
	}
	if probeRelease != nil && body.Messages[0].Content != "hello" {
		h.probeGate.Do(func() {
			close(probeEntered)
			select {
			case <-probeRelease:
			case <-r.Context().Done():
			}
		})
	}
	if body.Messages[0].Content != "hello" {
		if wrong {
			executedModel = "fallback"
		}
		if override {
			executedModel = actual
		}
	}
	return lifecycleResponse(r, fmt.Sprintf(`{"model":%q,"choices":[{"message":{"role":"assistant","content":"root-executed"},"finish_reason":"stop"}]}`, executedModel)), nil
}
func lifecycleResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func buildLifecycleRoot(t *testing.T, profiles string, leaf *lifecycleHTTP) (*domainStack, chan time.Time, chan time.Duration) {
	t.Helper()
	return buildLifecycleRootWithDependencies(t, profiles, leaf, ModelDependencies{})
}

func buildLifecycleRootWithDependencies(t *testing.T, profiles string, leaf *lifecycleHTTP, deps ModelDependencies) (*domainStack, chan time.Time, chan time.Duration) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARBITRON_MASTER_KEY", "XV1dXV1dXV1dXV1dXV1dXV1dXV1dXV1dXV1dXV1dXV0=")
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0700))
	writeWorkflow(t, ws, "lifecycle", `{"name":"lifecycle","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}},{"id":"after","type":"llm_call","config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[{"source":"ask","target":"after"}]}`)
	writePinTemplate(t, ws, "lifecycle")
	cfg := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "arbitron.db") + "\nworkspace:\n  path: " + ws + "\nsamples:\n  seed: false\nlogging:\n  level: warn\n  file: " + filepath.Join(root, "arbitron.log") + "\n" + profiles
	path := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0600))
	ticks := make(chan time.Time)
	intervals := make(chan time.Duration, 4)
	if deps.FetchTransport == nil {
		deps.FetchTransport = catalogHTTPLeaf(leaf.fetch)
	}
	if deps.ProviderTransport == nil {
		deps.ProviderTransport = catalogHTTPLeaf(leaf.provider)
	}
	leaf.tickerStopped = make(chan struct{})
	var stopOnce sync.Once
	deps.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
		intervals <- d
		return ticks, func() { stopOnce.Do(func() { close(leaf.tickerStopped) }) }
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }
	}
	ds, err := initDomainWithModelDependencies(context.Background(), path, deps)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds, ticks, intervals
}

const lifecycleFollowing = `profiles:
  - alias: worker
    kind: chat
    connectionref: root
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
  - alias: other
    kind: chat
    connectionref: root
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`

func lifecycleConnection(t *testing.T, ds *domainStack) context.Context {
	t.Helper()
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "root", Provider: "openrouter", BaseURL: "https://provider.example.test"}, "lifecycle-key"))
	return ctx
}
func lifecycleExecute(t *testing.T, ds *domainStack, ctx context.Context, want string) {
	t.Helper()
	id, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
	require.NoError(t, err)
	run := waitTerminal(t, ds, id)
	require.Equal(t, instance.StateCompleted, run.State)
	assert.Equal(t, want, run.ModelPins["worker"])
	var output string
	require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&output))
	assert.Contains(t, output, "root-executed")
}
func TestColdFollowingSeedFirstBootRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
	ds, _, intervals := buildLifecycleRoot(t, lifecycleFollowing, leaf)
	var profiles int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
	assert.Zero(t, profiles, "construction must not seed or start discovery")
	leaf.mu.Lock()
	assert.Zero(t, leaf.fetches)
	leaf.mu.Unlock()
	ctx := lifecycleConnection(t, ds)
	require.NoError(t, ds.Start(ctx))
	for _, alias := range []string{"worker", "other"} {
		p, err := ds.profileSvc.Get(ctx, alias)
		require.NoError(t, err)
		assert.Equal(t, rootNew, p.Model)
	}
	select {
	case d := <-intervals:
		assert.Equal(t, 4*time.Hour, d)
	case <-time.After(2 * time.Second):
		t.Fatal("shared root ticker not started")
	}
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), view.Catalog.FetchedAt, "the shared clock reaches persisted source state")
	require.NotEmpty(t, view.ProbeCursor)
	require.NotEmpty(t, view.Evidence)
	frozen := catalogFrozenPayloads(t, ds)
	require.Len(t, frozen, 2)
	for _, raw := range frozen {
		var event modelcatalog.AnnouncementPayload
		require.NoError(t, json.Unmarshal([]byte(raw), &event))
		assert.Empty(t, event.Changes)
		assert.NotEmpty(t, event.Paths)
	}
	lifecycleExecute(t, ds, ctx, rootNew)
	leaf.mu.Lock()
	assert.Contains(t, leaf.models, rootNew)
	leaf.mu.Unlock()
	require.NoError(t, ds.Start(ctx), "start is idempotent")
	select {
	case <-intervals:
		t.Fatal("second loop started")
	default:
	}
}

func TestModelProfilesBuildStartCancellationRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}, entered: make(chan struct{})}
	ds, _, _ := buildLifecycleRoot(t, lifecycleFollowing, leaf)
	ctx := lifecycleConnection(t, ds)
	done := make(chan error, 1)
	go func() { done <- ds.Start(ctx) }()
	select {
	case <-leaf.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not reach source")
	}
	closed := make(chan struct{})
	go func() { ds.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join startup")
	}
	require.Error(t, <-done)
	require.Error(t, ds.db.Ping())
	leaf.mu.Lock()
	assert.Empty(t, leaf.models)
	leaf.mu.Unlock()
	assert.Error(t, ds.Start(ctx), "closed stack must not restart")
}

// serve and MCP give Start their serving lifetime, rather than a request's
// context. Cancellation ends discovery admission; a late create must fail
// without leaving a profile behind while the server finishes shutting down.
func TestModelProfilesServingContextCancellationRefusesFollowingCreateRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	admin := lifecycleConnection(t, ds)
	ctx, cancel := context.WithCancel(admin)
	defer cancel()
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	cancel()
	select {
	case <-leaf.tickerStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("serving cancellation did not stop the four-hour loop")
	}
	require.Eventually(t, func() bool {
		return ds.modelRefresh.coordinator.Do(admin, func(context.Context) error { return nil }) == modelcatalog.ErrRefreshClosed
	}, 5*time.Second, time.Millisecond, "serving cancellation must close discovery admission")
	_, _, err := ds.profileSvc.Create(admin, provider.Profile{
		Alias: "late", Kind: provider.ProfileKindChat, ConnectionRef: "root",
		Model: rootNew, SelectionMode: provider.SelectionFollowing,
		FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"},
	})
	require.ErrorIs(t, err, modelcatalog.ErrRefreshClosed)
	_, err = ds.profileSvc.Get(admin, "late")
	require.ErrorIs(t, err, provider.ErrProfileNotFound)
	_, err = ds.profileResolver.Resolve(admin, "late")
	require.ErrorIs(t, err, provider.ErrUnknownProfile)
	require.NoError(t, ds.db.Ping(), "storage remains live until the serving entry defers Close")
	ds.Close(context.Background())
	require.Error(t, ds.db.Ping())
}

// A first boot whose source is down leaves the YAML following aliases pending.
// The next coordinated four-hour cycle that succeeds must seed them, so the
// configured aliases are not postponed to another boot (design phase 2, G2).
func TestPendingFollowingSeedRetriedByScheduledCycleRoot(t *testing.T) {
	for _, fault := range []string{"none", "seed_write", "seed_ack", "existing_alias", "shutdown"} {
		t.Run(fault, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}, down: true}
			var fetchMu sync.Mutex
			var remaining int
			entered, reaped := make(chan struct{}), make(chan struct{})
			fetch := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				fetchMu.Lock()
				block := remaining == 1
				if remaining > 0 {
					remaining--
				}
				fetchMu.Unlock()
				if block {
					close(entered)
					<-r.Context().Done()
					close(reaped)
					return nil, r.Context().Err()
				}
				return leaf.fetch(r)
			})
			ds, ticks, intervals := buildLifecycleRootWithDependencies(t, lifecycleFollowing, leaf, ModelDependencies{FetchTransport: fetch})
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			var profiles int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
			require.Zero(t, profiles, "an outage at boot must not seed")
			original := pendingSeedOriginals(t, ds)
			require.Len(t, original, 2)
			for _, alias := range []string{"worker", "other"} {
				var p provider.Profile
				require.NoError(t, json.Unmarshal([]byte(original[alias]), &p))
				assert.Equal(t, alias, p.Alias)
				assert.Empty(t, p.Model)
				assert.Equal(t, provider.SelectionFollowing, p.SelectionMode)
				assert.Equal(t, &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}, p.FollowTarget)
			}

			// Both prospective seed attempts must finish before restoring the source.
			var reportsBefore int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports`).Scan(&reportsBefore))
			rootTick(t, ticks)
			rootReports(t, ds, reportsBefore+3)
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
			require.Zero(t, profiles)
			require.Equal(t, original, pendingSeedOriginals(t, ds))
			leaf.mu.Lock()
			leaf.down = false
			leaf.mu.Unlock()

			var existing provider.Profile
			switch fault {
			case "seed_write":
				_, err := ds.db.Exec(`CREATE TRIGGER fail_seed BEFORE INSERT ON profiles BEGIN SELECT RAISE(ABORT,'seed write fault'); END`)
				require.NoError(t, err)
			case "seed_ack":
				_, err := ds.db.Exec(`CREATE TRIGGER fail_seed BEFORE DELETE ON profile_seed_pending BEGIN SELECT RAISE(ABORT,'seed ack fault'); END`)
				require.NoError(t, err)
			case "existing_alias":
				var err error
				existing, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "other", Kind: provider.ProfileKindChat, ConnectionRef: "root", Model: rootOld, SelectionMode: provider.SelectionPinned})
				require.NoError(t, err)
			case "shutdown":
				leaf.mu.Lock()
				leaf.wrong = true // No reusable proof: the original seed must bootstrap.
				leaf.mu.Unlock()
				// The first fetch belongs to coordinated Refresh; the second is the
				// original seed's prospective discovery, after admission is released.
				fetchMu.Lock()
				remaining = 2
				fetchMu.Unlock()
			}
			rootTick(t, ticks)
			if fault == "shutdown" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("scheduled seed retry never reached prospective discovery")
				}
				require.Equal(t, original, pendingSeedOriginals(t, ds))
				closed := make(chan struct{})
				go func() { ds.Close(context.Background()); close(closed) }()
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("Close did not join scheduled seed retry")
				}
				select {
				case <-reaped:
				default:
					t.Fatal("seed I/O outlived database closure")
				}
				select {
				case <-leaf.tickerStopped:
				default:
					t.Fatal("scheduled ticker not stopped")
				}
				require.Error(t, ds.db.Ping())
				reopened, err := adaptsqlite.Open(ds.cfg.Database.Path)
				require.NoError(t, err)
				defer reopened.Close()
				pending, err := adaptsqlite.NewProfileRepo(reopened).PendingProfileSeed(ctx, nil)
				require.NoError(t, err)
				require.Len(t, pending, 2)
				for _, p := range pending {
					var want provider.Profile
					require.NoError(t, json.Unmarshal([]byte(original[p.Alias]), &want))
					assert.Equal(t, want, p)
				}
				require.NoError(t, reopened.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
				assert.Zero(t, profiles)
				return
			}
			if fault == "seed_write" || fault == "seed_ack" {
				require.Eventually(t, func() bool {
					raw, err := os.ReadFile(filepath.Join(filepath.Dir(ds.cfg.Database.Path), "arbitron.log"))
					return err == nil && strings.Contains(string(raw), "retrying pending config profile seeds") && strings.Contains(string(raw), strings.ReplaceAll(fault, "_", " ")+" fault")
				}, 5*time.Second, time.Millisecond, "the scheduled seed attempt must actually encounter the injected storage fault")
				require.Equal(t, original, pendingSeedOriginals(t, ds), "storage faults retain the original seed, not today's config")
				_, err := ds.db.Exec(`DROP TRIGGER fail_seed`)
				require.NoError(t, err)
				rootTick(t, ticks)
			}
			require.Eventually(t, func() bool {
				var n int
				return ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&n) == nil && n == 0
			}, 5*time.Second, 20*time.Millisecond, "the successful tick must seed the original pending aliases")
			for _, alias := range []string{"worker", "other"} {
				p, err := ds.profileSvc.Get(ctx, alias)
				require.NoError(t, err)
				assert.Equal(t, rootOld, p.Model)
				assert.Equal(t, int64(1), p.Revision)
				if alias == "other" && fault == "existing_alias" {
					assert.Equal(t, existing, p, "retry must not overwrite an admin-created alias")
				}
			}
			lifecycleExecute(t, ds, ctx, rootOld)
			leaf.mu.Lock()
			assert.Equal(t, []string{rootOld, rootOld}, leaf.executed, "published seeded aliases must reach actual execution")
			leaf.mu.Unlock()
			rootTick(t, ticks)
			rootTick(t, ticks) // Acceptance by the same loop joins the previous retry.
			require.Empty(t, pendingSeedOriginals(t, ds))
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
			assert.Equal(t, 2, profiles, "repeated ticks must not duplicate seeds")
			for _, alias := range []string{"worker", "other"} {
				p, err := ds.profileSvc.Get(ctx, alias)
				require.NoError(t, err)
				assert.Equal(t, int64(1), p.Revision)
			}
		})
	}
}

func pendingSeedOriginals(t *testing.T, ds *domainStack) map[string]string {
	t.Helper()
	rows, err := ds.db.Query(`SELECT alias, profile_json FROM profile_seed_pending ORDER BY position`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var alias, raw string
		require.NoError(t, rows.Scan(&alias, &raw))
		out[alias] = raw
	}
	require.NoError(t, rows.Err())
	return out
}

// A failure acknowledging an unrelated refresh report must not starve the
// original YAML seeds: each following seed still needs its own durable proof.
func TestPendingFollowingSeedRetriesDespitePublicationFailureRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}, down: true}
	ds, ticks, intervals := buildLifecycleRoot(t, lifecycleFollowing, leaf)
	ctx := lifecycleConnection(t, ds)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	var pending int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&pending))
	require.Equal(t, 2, pending)

	// Retain a real report from boot and fail only its acknowledgment. Fresh
	// prospective discovery can still commit and acknowledge its own reports.
	var reportID string
	require.NoError(t, ds.db.QueryRow(`SELECT id FROM model_refresh_reports ORDER BY rowid LIMIT 1`).Scan(&reportID))
	_, err := ds.db.Exec(`UPDATE model_refresh_reports SET publication_pending=1 WHERE rowid=1`)
	require.NoError(t, err)
	_, err = ds.db.Exec(`CREATE TRIGGER fail_boot_report_ack BEFORE UPDATE OF publication_pending ON model_refresh_reports
		WHEN OLD.rowid=1 BEGIN SELECT RAISE(ABORT, 'publication ack unavailable'); END`)
	require.NoError(t, err)
	leaf.mu.Lock()
	leaf.down = false
	leaf.mu.Unlock()

	rootTick(t, ticks)
	assert.Eventually(t, func() bool {
		var n int
		return ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&n) == nil && n == 0
	}, 5*time.Second, 20*time.Millisecond, "a publication failure must not starve independently provable seeds")
	lifecycleExecute(t, ds, ctx, rootOld)
	for _, alias := range []string{"worker", "other"} {
		p, err := ds.profileSvc.Get(ctx, alias)
		require.NoError(t, err)
		require.Equal(t, rootOld, p.Model)
		require.Equal(t, int64(1), p.Revision)
	}
	var publicationPending bool
	require.NoError(t, ds.db.QueryRow(`SELECT publication_pending FROM model_refresh_reports WHERE id=?`, reportID).Scan(&publicationPending))
	require.True(t, publicationPending, "seed success must not acknowledge the failed publication")
	require.Empty(t, pendingSeedOriginals(t, ds))
	frozen := catalogFrozenPayloads(t, ds)
	require.NotEmpty(t, frozen, "seed proofs must use the durable discovery pipeline")

	_, err = ds.db.Exec(`DROP TRIGGER fail_boot_report_ack`)
	require.NoError(t, err)
	rootTick(t, ticks)
	require.Eventually(t, func() bool {
		var publicationPending bool
		err := ds.db.QueryRow(`SELECT publication_pending FROM model_refresh_reports WHERE id=?`, reportID).Scan(&publicationPending)
		return err == nil && !publicationPending
	}, 5*time.Second, 20*time.Millisecond, "the next tick must recover the original publication")
	for _, alias := range []string{"worker", "other"} {
		p, err := ds.profileSvc.Get(ctx, alias)
		require.NoError(t, err)
		require.Equal(t, int64(1), p.Revision, "completed seeds must not be replayed")
	}
	assert.Equal(t, frozen, catalogFrozenPayloads(t, ds), "publication repair cannot rewrite original seed discoveries")
	lifecycleExecute(t, ds, ctx, rootOld)
	leaf.mu.Lock()
	assert.Equal(t, []string{rootOld, rootOld, rootOld, rootOld}, leaf.executed)
	leaf.mu.Unlock()
}
