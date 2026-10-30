package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/adapter/renderreview"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

type layoutE2EOptions struct {
	maxAttempts string
	seedSample  string // synthetic sample seeded at boot
	realSample  string // bundled samples/<name> copied and seeded at boot
}
type layoutE2EEnv struct{ root, workspaceDir, dbPath, logPath, configPath, samplesDir string }

var layoutE2EPositions = map[string]workflow.NodePosition{"t1": {X: 20, Y: 30}, "t2": {X: 240, Y: 30}, "t3": {X: 460, Y: 30}}

func newLayoutE2EEnv(t *testing.T, opts layoutE2EOptions) *layoutE2EEnv {
	t.Helper()
	pinLayoutE2EEnv(t)
	e := &layoutE2EEnv{root: t.TempDir()}
	e.workspaceDir = filepath.Join(e.root, "workspace")
	e.dbPath = filepath.Join(e.root, "arbitron.db")
	e.logPath = filepath.Join(e.root, "arbitron.log")
	e.configPath = filepath.Join(e.root, "config.yaml")
	e.samplesDir = filepath.Join(e.root, "samples")
	require.NoError(t, os.MkdirAll(e.workspaceDir, 0700))
	if opts.maxAttempts == "" {
		opts.maxAttempts = "3"
	}
	if opts.seedSample != "" {
		dir := filepath.Join(e.samplesDir, opts.seedSample)
		require.NoError(t, os.MkdirAll(dir, 0700))
		b, err := json.Marshal(layoutE2EWorkflow(opts.seedSample, "Seeded transform"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), b, 0600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "support.txt"), []byte("real sample supporting content"), 0600))
	}
	if opts.realSample != "" {
		src := filepath.Join("..", "..", "samples", opts.realSample)
		require.NoError(t, os.CopyFS(filepath.Join(e.samplesDir, opts.realSample), os.DirFS(src)), "copying bundled sample %s", opts.realSample)
	}
	cfg := fmt.Sprintf("server:\n  host: 127.0.0.1\n  port: 0\nauth:\n  mode: local\ndatabase:\n  path: %q\nworkspace:\n  path: %q\nlogging:\n  level: info\n  file: %q\nsamples:\n  seed: %t\n  sourcedir: %q\nlayout:\n  maxattempts: %s\n", e.dbPath, e.workspaceDir, e.logPath, opts.seedSample != "" || opts.realSample != "", e.samplesDir, opts.maxAttempts)
	// Legacy provenance tests exercise browser-independent failure; acceptance opts in
	// through the production loader's browser environment key after constructing e.
	cfg += fmt.Sprintf("  browserpath: %q\n", filepath.Join(e.root, "no-such-chromium"))
	// The shipped defaults follow a tier and seed only once the catalog answers;
	// these stacks boot offline, so pin the shipped aliases the way a healthy
	// first boot leaves them.
	cfg += layoutE2EPinnedProfiles
	require.NoError(t, os.WriteFile(e.configPath, []byte(cfg), 0600))
	return e
}

const layoutE2EPinnedProfiles = `profiles:
  - {alias: "default-chat", kind: "chat", model: "claude-sonnet-5-5"}
  - {alias: "default-agent", kind: "agent", model: "claude-sonnet-5-5"}
  - {alias: "assessor", kind: "chat", model: "claude-sonnet-5-5"}
  - {alias: "interviewer", kind: "agent", model: "claude-sonnet-5-5"}
  - {alias: "reviewer", kind: "agent", model: "claude-sonnet-5-5"}
  - {alias: "coder", kind: "agent", backend: "claude-code", model: "claude-opus-5-5"}
  - {alias: "planner", kind: "agent", backend: "claude-code", model: "claude-opus-5-5"}
`

func (e *layoutE2EEnv) boot(t *testing.T) (*domainStack, func()) {
	t.Helper()
	ds, err := initDomainWithModelDependencies(context.Background(), e.configPath, offlineModelDependencies())
	require.NoError(t, err)
	var once sync.Once
	close := func() { once.Do(func() { ds.Close(context.Background()) }) }
	t.Cleanup(close)
	// Seeding, recovery and the layout worker run in Start, not initDomain.
	require.NoError(t, ds.Start(context.Background()))
	return ds, close
}
func (e *layoutE2EEnv) privatePath(name string) string {
	return filepath.Join(e.workspaceDir, "users", localDefaultUser.String(), name, "workflow.json")
}
func (e *layoutE2EEnv) sharedPath(name string) string {
	return filepath.Join(e.workspaceDir, "shared", name, "workflow.json")
}
func layoutE2EWorkflow(name, label string) *workflow.Workflow {
	wf := &workflow.Workflow{Name: name, Description: "Layout root provenance probe"}
	for _, id := range []string{"t1", "t2", "t3"} {
		p := layoutE2EPositions[id]
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeTransform, Label: label + " " + id, Entrypoint: id == "t1", Position: &p, Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: id, Operation: "set", Value: true}}}})
	}
	wf.Edges = []workflow.Edge{{Source: "t1", Target: "t2"}, {Source: "t2", Target: "t3"}}
	return wf
}
func layoutE2EDiscardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func layoutE2EOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "layout.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	return db
}
func layoutE2EConfig(workspace string, maxAttempts int) *config.Config {
	cfg := &config.Config{}
	cfg.Workspace.Path = workspace
	cfg.Layout.MaxAttempts = maxAttempts
	cfg.Database.Path = filepath.Join(workspace, "layout.db")
	cfg.Layout.BrowserPath = filepath.Join(workspace, "no-such-chromium")
	return cfg
}
func layoutE2EManifest(t *testing.T, path string) *workflow.Workflow {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(b, &wf))
	return &wf
}
func layoutE2ESavedRevision(t *testing.T, path string) string {
	t.Helper()
	rev := layoutE2EManifest(t, path).SavedRevision()
	require.NotEmpty(t, rev)
	return rev
}
func layoutE2EWaitTerminal(t *testing.T, ds *domainStack, revision string) layout.Record {
	t.Helper()
	var rec layout.Record
	require.Eventually(t, func() bool {
		var err error
		rec, err = ds.layout.store.Get(context.Background(), revision)
		return err == nil && rec.State.Terminal()
	}, 5*time.Second, 10*time.Millisecond, "revision %s must reach the durable terminal sink", revision)
	if rec.State == layout.StatePassed {
		result, err := ds.layout.store.Result(context.Background(), revision, rec.ResultGeneration)
		require.NoError(t, err)
		var wf workflow.Workflow
		require.NoError(t, json.Unmarshal(rec.Manifest, &wf))
		graph, err := layout.BuildGraph(&wf)
		require.NoError(t, err)
		require.True(t, layout.Compatible(result.Geometry, graph))
		require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	}
	return rec
}
func layoutE2EAssertOutcome(t *testing.T, rec layout.Record) {
	t.Helper()
	require.True(t, rec.State.Terminal())
	require.Positive(t, rec.AttemptLimit)
	require.Equal(t, 1, rec.AttemptsUsed)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(rec.Manifest, &wf))
	require.Equal(t, rec.Revision, wf.SavedRevision())
	require.Len(t, wf.Nodes, 3)
	require.Len(t, wf.Edges, 2)
	require.NotEmpty(t, wf.Nodes[0].Label)
	require.Equal(t, layoutE2EPositions, rec.Retained.Positions)
	if rec.State == layout.StateFailed {
		require.Equal(t, layout.StageRendering, rec.FailureStage)
		require.Contains(t, rec.FailureReason, layout.ErrRendererUnavailable.Error())
		require.NotContains(t, rec.FailureReason, "none of [", "browser candidate paths are logged, never persisted")
		require.NotContains(t, rec.FailureReason, "layout.browserpath")
	} else if rec.State == layout.StatePassed {
		require.Positive(t, rec.ResultGeneration)
		require.False(t, rec.Unreviewed)
	}
}
func layoutE2ECountRows(t *testing.T, db *sql.DB, workflowName string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM layout_revisions WHERE workflow_name = ?", workflowName).Scan(&count))
	return count
}

func TestLayoutE2E_InitDomainAuthoring(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, close := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NotNil(t, ds.layout, "initDomain must construct the durable pipeline")
	assertSaved := func(path, name string, count int) *workflow.Workflow {
		t.Helper()
		wf := layoutE2EManifest(t, path)
		require.NotEqual(t, uuid.Nil, wf.ID)
		require.NotEmpty(t, wf.SavedRevision())
		rec := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
		layoutE2EAssertOutcome(t, rec)
		require.Equal(t, 3, rec.AttemptLimit)
		require.Equal(t, wf.ID, rec.Destination.WorkflowID)
		require.Equal(t, count, layoutE2ECountRows(t, ds.db, name))
		captured, err := json.Marshal(wf)
		require.NoError(t, err)
		require.JSONEq(t, string(captured), string(rec.Manifest))
		return wf
	}
	require.NoError(t, ds.workflowSvc.Create(ctx, "authoring", layoutE2EWorkflow("authoring", "Original")))
	first := assertSaved(e.privatePath("authoring"), "authoring", 1)
	require.NoError(t, ds.workflowSvc.Update(ctx, "authoring", layoutE2EWorkflow("authoring", "Updated")))
	updated := assertSaved(e.privatePath("authoring"), "authoring", 2)
	require.NotEqual(t, first.SavedRevision(), updated.SavedRevision())
	require.Equal(t, first.ID, updated.ID)
	require.NoError(t, ds.workflowSvc.Publish(ctx, "authoring", "published", workflow.VisibilityPublic, ""))
	published := assertSaved(e.sharedPath("published"), "published", 1)
	require.NotEqual(t, updated.SavedRevision(), published.SavedRevision())
	require.NotEqual(t, updated.ID, published.ID)
	require.NoError(t, ds.workflowSvc.Publish(ctx, "authoring", "published", workflow.VisibilityPublic, ""))
	republished := assertSaved(e.sharedPath("published"), "published", 2)
	require.NotEqual(t, published.SavedRevision(), republished.SavedRevision())
	require.Equal(t, published.ID, republished.ID)
	require.NoError(t, ds.workflowSvc.Update(ctx, "published", layoutE2EWorkflow("published", "Admin shared")))
	shared := assertSaved(e.sharedPath("published"), "published", 3)
	proof, ok, err := ds.publishedRepo.PublicationOf(ctx, "published")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, shared.SavedRevision(), proof.Revision)
	require.Equal(t, shared.ID, proof.WorkflowID)
	require.NoError(t, ds.workflowSvc.Copy(ctx, "published", "copied"))
	copied := assertSaved(e.privatePath("copied"), "copied", 1)
	require.NotEqual(t, shared.ID, copied.ID, "an independent Copy must carry its own identity into the saved manifest and layout row")
	require.NoError(t, ds.workflowSvc.Duplicate(ctx, "authoring", "duplicated"))
	duplicate := assertSaved(e.privatePath("duplicated"), "duplicated", 1)
	require.NotEqual(t, updated.ID, duplicate.ID)
	loaded, err := ds.workflowSvc.Load(ctx, "authoring")
	require.NoError(t, err)
	require.Equal(t, updated.SavedRevision(), loaded.SavedRevision())
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "authoring"))
	close()
	ds.Close(context.Background())
	ds, _ = e.boot(t)
	require.Equal(t, updated.SavedRevision(), layoutE2ESavedRevision(t, e.privatePath("authoring")))
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "authoring"))
}

func TestLayoutE2E_RenameThroughCompositionRoot(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "rename-source", layoutE2EWorkflow("rename-source", "Rename input")))
	source := layoutE2EManifest(t, e.privatePath("rename-source"))
	layoutE2EWaitTerminal(t, ds, source.SavedRevision())
	require.NoError(t, ds.workflowSvc.Rename(ctx, "rename-source", "rename-target"))
	require.NoDirExists(t, e.privatePath("rename-source"))
	renamed := layoutE2EManifest(t, e.privatePath("rename-target"))
	require.Equal(t, source.ID, renamed.ID, "rename keeps the workflow identity")
	require.NotEqual(t, source.SavedRevision(), renamed.SavedRevision(), "rename issues a new saved revision")
	require.Equal(t, workflow.SaveReasonRename, renamed.Presentation.Reason)
	layoutE2EWaitTerminal(t, ds, renamed.SavedRevision())
	// History moves with the name: the old revision is relocated, the rename revision is new.
	require.Equal(t, 0, layoutE2ECountRows(t, ds.db, "rename-source"))
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "rename-target"))
	old, err := ds.layout.store.Get(ctx, source.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, "rename-target", old.Destination.Name)
}

func TestLayoutE2E_SharedRenameThroughCompositionRoot(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "shared-src", layoutE2EWorkflow("shared-src", "Shared input")))
	require.NoError(t, ds.workflowSvc.Publish(ctx, "shared-src", "shared-old", workflow.VisibilityPublic, ""))
	published := layoutE2EManifest(t, e.sharedPath("shared-old"))
	layoutE2EWaitTerminal(t, ds, published.SavedRevision())
	require.NoError(t, ds.workflowSvc.Rename(ctx, "shared-old", "shared-new"))
	require.NoDirExists(t, e.sharedPath("shared-old"))
	renamed := layoutE2EManifest(t, e.sharedPath("shared-new"))
	require.Equal(t, published.ID, renamed.ID)
	require.NotEqual(t, published.SavedRevision(), renamed.SavedRevision())
	proof, ok, err := ds.publishedRepo.PublicationOf(context.Background(), "shared-new")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, renamed.SavedRevision(), proof.Revision, "publication proof moves with the rename in one transaction")
	require.Equal(t, published.ID, proof.WorkflowID)
	_, ok, err = ds.publishedRepo.PublicationOf(context.Background(), "shared-old")
	require.NoError(t, err)
	require.False(t, ok)
	layoutE2EWaitTerminal(t, ds, renamed.SavedRevision())
	require.Equal(t, 0, layoutE2ECountRows(t, ds.db, "shared-old"))
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "shared-new"))
}

func TestLayoutE2E_SeedThroughRootAndRestart(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{seedSample: "seeded"})
	// A late backfill cannot satisfy this publication-time identity sink.
	db, err := adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.RunMigrations(db))
	_, err = db.Exec(`CREATE TRIGGER seed_identity_before_proof BEFORE INSERT ON published_workflows
 WHEN NOT EXISTS (SELECT 1 FROM workflow_identity WHERE id=NEW.workflow_id AND name=NEW.name AND scope='shared')
 BEGIN SELECT RAISE(ABORT, 'seed identity missing before publication'); END`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	ds, close := e.boot(t)
	wf := layoutE2EManifest(t, e.sharedPath("seeded"))
	require.NotEmpty(t, wf.SavedRevision())
	require.NotEqual(t, uuid.Nil, wf.ID)
	rec := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	layoutE2EAssertOutcome(t, rec)
	source := layoutE2EManifest(t, filepath.Join(e.samplesDir, "seeded", "workflow.json"))
	require.Equal(t, source.Nodes, wf.Nodes)
	require.Equal(t, source.Edges, wf.Edges)
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(rec.Manifest, &captured))
	require.Equal(t, *wf, captured, "initDomain must feed the complete final saved sample to SQLite")
	require.Equal(t, wf.Presentation.SavedAt, rec.RequestedAt)
	require.Equal(t, 3, rec.AttemptLimit)
	require.Equal(t, wf.ID, rec.Destination.WorkflowID)
	require.Equal(t, workflow.SaveReasonSeed, rec.Reason)
	proof, ok, err := ds.publishedRepo.PublicationOf(context.Background(), "seeded")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, wf.ID, proof.WorkflowID)
	require.Equal(t, wf.SavedRevision(), proof.Revision)
	var registered string
	require.NoError(t, ds.db.QueryRow("SELECT id FROM workflow_identity WHERE name = ?", "seeded").Scan(&registered))
	require.Equal(t, wf.ID.String(), registered)
	support, err := os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath("seeded")), "support.txt"))
	require.NoError(t, err)
	require.Equal(t, "real sample supporting content", string(support))
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "seeded"))
	close()
	ds, _ = e.boot(t)
	require.Equal(t, *wf, *layoutE2EManifest(t, e.sharedPath("seeded")), "restart must not rewrite the sample or its saved metadata")
	restarted, err := ds.layout.store.Get(context.Background(), wf.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, rec.Manifest, restarted.Manifest)
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "seeded"))
}

func TestLayoutE2E_SeedDoesNotAdoptForeignOrphan(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{seedSample: "foreign"})
	require.NoError(t, os.MkdirAll(filepath.Dir(e.sharedPath("foreign")), 0700))
	b, err := json.Marshal(layoutE2EWorkflow("foreign", "Foreign existing bytes"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(e.sharedPath("foreign"), b, 0600))
	ds, _ := e.boot(t)
	after, err := os.ReadFile(e.sharedPath("foreign"))
	require.NoError(t, err)
	// Backfill may assign an ID; it must not author a new revision or replace content.
	wf := layoutE2EManifest(t, e.sharedPath("foreign"))
	require.Empty(t, wf.SavedRevision())
	require.Contains(t, string(after), "Foreign existing bytes")
	require.Equal(t, 0, layoutE2ECountRows(t, ds.db, "foreign"))
	log, err := os.ReadFile(e.logPath)
	require.NoError(t, err)
	require.Contains(t, string(log), "sample workflow seeding failed; continuing boot without samples")
	_, ok, err := ds.publishedRepo.PublicationOf(context.Background(), "foreign")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestLayoutE2E_TransferRelocatesWithoutNewRevision(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "move", layoutE2EWorkflow("move", "Owned workflow")))
	rev := layoutE2ESavedRevision(t, e.privatePath("move"))
	layoutE2EWaitTerminal(t, ds, rev)
	owner := uuid.New()
	require.NoError(t, ds.workflowSvc.TransferOwnership(ctx, "move", owner))
	path := filepath.Join(e.workspaceDir, "users", owner.String(), "move", "workflow.json")
	require.Equal(t, rev, layoutE2ESavedRevision(t, path))
	require.NoFileExists(t, e.privatePath("move"))
	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	require.Equal(t, owner, row.Destination.Scope.OwnerID)
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "move"))
}

func TestLayoutE2E_LegacyReadsAndBackfillNeverStamp(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	wf := layoutE2EWorkflow("legacy", "Pre-feature input")
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(e.privatePath("legacy")), 0700))
	require.NoError(t, os.WriteFile(e.privatePath("legacy"), b, 0600))
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	loaded, err := ds.workflowSvc.Load(ctx, "legacy")
	require.NoError(t, err)
	require.Empty(t, loaded.SavedRevision())
	require.NotEqual(t, uuid.Nil, loaded.ID)
	require.NoError(t, ds.workflowSvc.BackfillRegistry(ctx))
	require.Empty(t, layoutE2EManifest(t, e.privatePath("legacy")).SavedRevision())
	require.Equal(t, 0, layoutE2ECountRows(t, ds.db, "legacy"))
}

func TestLayoutE2E_ConfigBudgetAndWarnings(t *testing.T) {
	for _, value := range []string{"3", "0", "-2"} {
		t.Run(value, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: value})
			ds, _ := e.boot(t)
			require.NoError(t, ds.workflowSvc.Create(adminCtxForLocalUser(t, ds), "budget", layoutE2EWorkflow("budget", "Configured budget")))
			rev := layoutE2ESavedRevision(t, e.privatePath("budget"))
			require.NotNil(t, ds.layout)
			rec := layoutE2EWaitTerminal(t, ds, rev)
			want := 3
			if value != "3" {
				want = 1
			}
			require.Equal(t, want, rec.AttemptLimit)
			log, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			if value != "3" {
				require.Contains(t, string(log), fmt.Sprintf("layout.maxattempts %s is below 1; using 1", value))
				require.Contains(t, string(log), "WARN")
			}
		})
	}
}

// The gate consumes the real graph measurement request, not an ignored argument.
type layoutE2EGatedRenderer struct {
	entered  chan layout.MeasureRequest
	inFlight atomic.Int32
}

func (r *layoutE2EGatedRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	r.entered <- req
	<-ctx.Done()
	return layout.Measurements{}, ctx.Err()
}
func (*layoutE2EGatedRenderer) Render(context.Context, layout.RenderRequest) (layout.Rendering, error) {
	return layout.Rendering{}, fmt.Errorf("unexpected Render on gated measurer")
}
func TestLayoutE2E_SaveIsAsyncAndEngineSnapshotRetainsPendingRevision(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	renderer := &layoutE2EGatedRenderer{entered: make(chan layout.MeasureRequest, 1)}
	joined := false
	var stack *domainStack
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return renderer, renderreview.NewReadabilityReviewer(nil), func() {
			require.Zero(t, renderer.inFlight.Load(), "renderer must be joined before cleanup")
			require.NoError(t, stack.db.Ping(), "DB must remain open through renderer cleanup")
			joined = true
		}, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	legacy := layoutE2EWorkflow("legacy-snapshot", "Untouched legacy input")
	require.NoError(t, filesystem.NewWorkflowRepo(e.workspaceDir).Create(context.Background(), workflow.Private(localDefaultUser), legacy.Name, legacy))
	ds, close := e.boot(t)
	stack = ds
	ctx := adminCtxForLocalUser(t, ds)
	legacyID, _, err := ds.eng.Trigger(ctx, legacy.Name, map[string]any{}, "normal")
	require.NoError(t, err)
	legacyInstance, err := ds.instanceRepo.Get(ctx, legacyID)
	require.NoError(t, err)
	legacySnapshot, err := json.Marshal(legacyInstance.WorkflowSnapshot)
	require.NoError(t, err)
	require.NotContains(t, string(legacySnapshot), `"presentation"`, "legacy snapshots must omit saved-revision metadata")
	legacySaved, err := json.Marshal(layoutE2EManifest(t, e.privatePath(legacy.Name)))
	require.NoError(t, err)
	require.JSONEq(t, string(legacySaved), string(legacySnapshot))
	require.Zero(t, layoutE2ECountRows(t, ds.db, legacy.Name))
	done := make(chan error, 1)
	go func() { done <- ds.workflowSvc.Create(ctx, "pending", layoutE2EWorkflow("pending", "Frozen input")) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("save waited for rendering")
	}
	select {
	case req := <-renderer.entered:
		saved := layoutE2EManifest(t, e.privatePath("pending"))
		graph, err := layout.BuildGraph(saved)
		require.NoError(t, err)
		require.Equal(t, layout.MeasureRequestFor(graph, layout.DefaultContract()), req, "the worker must feed the entire saved graph, not an empty or different measurement request")
	case <-time.After(3 * time.Second):
		t.Fatal("renderer not fed by real root")
	}
	wf := layoutE2EManifest(t, e.privatePath("pending"))
	require.NotEmpty(t, wf.SavedRevision())
	id, _, err := ds.eng.Trigger(ctx, "pending", map[string]any{}, "normal")
	require.NoError(t, err)
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	b, err := json.Marshal(inst.WorkflowSnapshot)
	require.NoError(t, err)
	var frozen workflow.Workflow
	require.NoError(t, json.Unmarshal(b, &frozen))
	require.Equal(t, wf, &frozen, "the persisted snapshot must freeze the complete committed graph, positions and presentation")
	require.Equal(t, workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: layoutE2EPositions}, frozen.Presentation.Retained)
	require.Equal(t, ds.layout.reader.View(ctx, wf).Retained, ds.layout.reader.View(ctx, &frozen).Retained)
	rec, err := ds.layout.store.Get(ctx, wf.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, layout.StateInProgress, rec.State)
	captured, err := json.Marshal(wf)
	require.NoError(t, err)
	require.Equal(t, captured, rec.Manifest)
	require.Equal(t, wf.Presentation.Retained, rec.Retained)
	require.Equal(t, wf.Presentation.SavedAt, rec.RequestedAt)
	require.Equal(t, workflow.SaveReasonCreate, rec.Reason)
	require.Equal(t, layout.Destination{Scope: workflow.Private(localDefaultUser), Name: "pending", WorkflowID: wf.ID}, rec.Destination)
	require.Equal(t, 3, rec.AttemptLimit)
	// A later authoring save cannot change the historical instance or row.
	require.NoError(t, ds.workflowSvc.Update(ctx, "pending", layoutE2EWorkflow("pending", "Newer source")))
	require.NotEqual(t, wf.SavedRevision(), layoutE2ESavedRevision(t, e.privatePath("pending")))
	inst, err = ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	stillFrozen, err := json.Marshal(inst.WorkflowSnapshot)
	require.NoError(t, err)
	require.Equal(t, b, stillFrozen)
	historical, err := ds.layout.store.Get(ctx, wf.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, captured, historical.Manifest)
	close()
	require.True(t, joined)
	require.Error(t, ds.db.Ping())
}

func TestLayoutE2E_MCPAuthoringThroughRegisteredServer(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	srv := adaptmcp.NewServerWithDeps("layout-e2e", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	manifest, err := json.Marshal(layoutE2EWorkflow("mcp-authored", "Registered tool input"))
	require.NoError(t, err)
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "workflow_create", "arguments": map[string]any{"manifest": string(manifest)}}})
	require.NoError(t, err)
	response := srv.MCPServer().HandleMessage(adminCtxForLocalUser(t, ds), request)
	b, err := json.Marshal(response)
	require.NoError(t, err)
	var rpc struct {
		Result *gomcp.CallToolResult `json:"result"`
		Error  any                   `json:"error"`
	}
	require.NoError(t, json.Unmarshal(b, &rpc))
	require.Nil(t, rpc.Error)
	require.NotNil(t, rpc.Result)
	require.False(t, rpc.Result.IsError, string(b))
	rev := layoutE2ESavedRevision(t, e.privatePath("mcp-authored"))
	rec := layoutE2EWaitTerminal(t, ds, rev)
	layoutE2EAssertOutcome(t, rec)
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "mcp-authored"))
}

func TestLayoutE2E_ActualCommandsJoinOnContextAndEOF(t *testing.T) {
	for _, mode := range []string{"serve", "mcp"} {
		t.Run(mode, func(t *testing.T) {
			commandLikeProduction(t)
			e := newLayoutE2EEnv(t, layoutE2EOptions{seedSample: "command-seed"})
			old := layoutRenderReviewerFactory
			t.Cleanup(func() { layoutRenderReviewerFactory = old })
			var closed atomic.Bool
			layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
				r, v, closeRender, err := newLayoutRenderReviewer(cfg, logger)
				return r, v, func() {
					if closeRender != nil {
						closeRender()
					}
					closed.Store(true)
				}, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := newRootCmd()
			cmd.SetArgs([]string{"--config", e.configPath, mode})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			var stdinWriter *os.File
			if mode == "mcp" {
				r, w, err := os.Pipe()
				require.NoError(t, err)
				stdinWriter = w
				prior := os.Stdin
				os.Stdin = r
				t.Cleanup(func() { os.Stdin = prior; r.Close(); w.Close() })
			}
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() { defer close(finished); done <- cmd.ExecuteContext(ctx) }()
			t.Cleanup(func() {
				cancel()
				if stdinWriter != nil {
					stdinWriter.Close()
				}
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("command cleanup did not join")
				}
			})
			// The command's live worker, not a manually constructed test stack, must consume the seed.
			require.Eventually(t, func() bool {
				if _, err := os.Stat(e.sharedPath("command-seed")); err != nil {
					return false
				}
				db, err := adaptsqlite.Open(e.dbPath)
				if err != nil {
					return false
				}
				defer db.Close()
				var state string
				err = db.QueryRow("SELECT state FROM layout_revisions WHERE workflow_name='command-seed'").Scan(&state)
				return err == nil && state == string(layout.StateFailed)
			}, 5*time.Second, 20*time.Millisecond)
			require.NotEmpty(t, layoutE2ESavedRevision(t, e.sharedPath("command-seed")))
			if mode == "mcp" {
				require.NoError(t, stdinWriter.Close())
			} else {
				cancel()
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("command did not join shutdown")
			}
			require.True(t, closed.Load(), "renderer must close before the command returns")
			before, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			time.Sleep(30 * time.Millisecond)
			after, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			require.True(t, bytes.Equal(before, after), "background work continued after command close")
			require.NotContains(t, string(after), "database is closed")
		})
	}
}

// runStdioSession drives the actual `arbitron mcp` command over piped stdio,
// sending each request in turn and validating every stdout line as JSON-RPC.
func runStdioSession(t *testing.T, args []string, requests []map[string]any) []string {
	t.Helper()
	input, writer, err := os.Pipe()
	require.NoError(t, err)
	output, reader, err := os.Pipe()
	require.NoError(t, err)
	priorIn, priorOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, reader
	t.Cleanup(func() {
		os.Stdin, os.Stdout = priorIn, priorOut
		input.Close()
		writer.Close()
		output.Close()
		reader.Close()
	})
	cmd := newRootCmd()
	cmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); done <- cmd.ExecuteContext(ctx) }()
	lines := make(chan string, 16)
	scanErr := make(chan error, 1)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer close(lines)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErr <- scanner.Err()
	}()
	t.Cleanup(func() {
		cancel()
		writer.Close()
		reader.Close()
		<-finished
		<-scanned
	})
	var responses []string
	for _, req := range requests {
		b, err := json.Marshal(req)
		require.NoError(t, err)
		_, err = writer.Write(append(b, '\n'))
		require.NoError(t, err)
		select {
		case line, ok := <-lines:
			require.True(t, ok, "stdio ended without an RPC response")
			frame := decodeLayoutStdioFrame(t, line)
			id, err := json.Marshal(req["id"])
			require.NoError(t, err)
			require.JSONEq(t, string(id), string(frame["id"]))
			responses = append(responses, line)
		case <-time.After(10 * time.Second):
			t.Fatal("no stdio response")
		}
	}
	require.NoError(t, writer.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("stdio failed to exit")
	}
	require.NoError(t, reader.Close())
	for line := range lines {
		frame := decodeLayoutStdioFrame(t, line)
		require.Contains(t, frame, "method", "unexpected response after EOF: %s", line)
	}
	require.NoError(t, <-scanErr)
	return responses
}

// SINK = the layout_revisions row. The renderer stays blocked, so a response
// that arrived at all proves the stdio save returned before arrangement.
func TestLayoutE2E_StdioMCPAuthoringStampsAndRequestsOncePerRevision(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	renderer := &layoutE2EGatedRenderer{entered: make(chan layout.MeasureRequest, 8)}
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return renderer, renderreview.NewReadabilityReviewer(nil), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	manifest := func(label string) string {
		b, err := json.Marshal(layoutE2EWorkflow("stdio-author", label))
		require.NoError(t, err)
		return string(b)
	}
	call := func(id int, tool string, args map[string]any) map[string]any {
		return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
	}
	responses := runStdioSession(t, []string{"--config", e.configPath, "mcp"}, []map[string]any{
		call(1, "workflow_create", map[string]any{"manifest": manifest("First")}),
		call(2, "workflow_update", map[string]any{"name": "stdio-author", "manifest": manifest("Second")}),
	})
	for _, r := range responses {
		var rpc struct {
			Result *gomcp.CallToolResult `json:"result"`
			Error  any                   `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(r), &rpc))
		require.Nil(t, rpc.Error, r)
		require.NotNil(t, rpc.Result, r)
		require.False(t, rpc.Result.IsError, r)
	}
	// The command has exited; reopen the database it wrote.
	db, err := adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.Equal(t, 2, layoutE2ECountRows(t, db, "stdio-author"), "one durable arrangement request per saved revision")
	var distinct int
	require.NoError(t, db.QueryRow("SELECT count(DISTINCT revision) FROM layout_revisions WHERE workflow_name = ?", "stdio-author").Scan(&distinct))
	require.Equal(t, 2, distinct)
	require.FileExists(t, e.privatePath("stdio-author"))
	require.NotEmpty(t, layoutE2ESavedRevision(t, e.privatePath("stdio-author")))
}

func TestLayoutE2E_StackConstructionAndSave(t *testing.T) {
	db := layoutE2EOpenDB(t)
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	ls, err := newLayoutStack(layoutE2EConfig(root, 3), db, repo, adaptsqlite.NewPublishedWorkflowRepo(db), adaptsqlite.NewWorkflowIdentityRepo(db), nil)
	require.NoError(t, err)
	t.Cleanup(ls.stop)
	require.IsType(t, &renderreview.BrowserRenderer{}, ls.renderer)
	require.IsType(t, &renderreview.ReadabilityReviewer{}, ls.reviewer)
	ls.start(nil)
	ls.start(nil)
	svc := workflow.NewWorkflowService(repo, adaptsqlite.NewPublishedWorkflowRepo(db))
	svc.SetRegistry(adaptsqlite.NewWorkflowIdentityRepo(db))
	svc.SetRevisionStamper(ls.stamper)
	svc.SetMutationCoordinator(ls.mutations)
	require.NoError(t, svc.Create(adminCtxForLocalUser(t, nil), "stack-save", layoutE2EWorkflow("stack-save", "Captured meaningful label")))
	path := filepath.Join(root, "users", localDefaultUser.String(), "stack-save", "workflow.json")
	wf := layoutE2EManifest(t, path)
	rec := layoutE2EWaitTerminal(t, &domainStack{layout: ls}, wf.SavedRevision())
	layoutE2EAssertOutcome(t, rec)
	require.Equal(t, 3, rec.AttemptLimit)
	require.Equal(t, wf.ID, rec.Destination.WorkflowID)
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	require.JSONEq(t, string(b), string(rec.Manifest))
	view := ls.reader.View(context.Background(), wf)
	require.Equal(t, layout.DisplayFailed, view.DisplayStatus)
	require.True(t, view.Unreviewed)
	require.Equal(t, layoutE2EPositions, view.Retained.Positions)
	require.Equal(t, 1, layoutE2ECountRows(t, db, "stack-save"))
	ls.stop()
	ls.stop()
}

func TestLayoutE2E_ExplicitStandaloneNoRegistrySave(t *testing.T) {
	db := layoutE2EOpenDB(t)
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	ls, err := newLayoutStack(layoutE2EConfig(root, 3), db, repo, nil, nil, layoutE2EDiscardLogger())
	require.NoError(t, err)
	t.Cleanup(ls.stop)
	ls.start(nil)
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRevisionStamper(ls.stamper)
	svc.SetMutationCoordinator(ls.mutations)
	require.NoError(t, svc.Create(adminCtxForLocalUser(t, nil), "standalone", layoutE2EWorkflow("standalone", "Explicit no-registry input")))
	wf := layoutE2EManifest(t, filepath.Join(root, "users", localDefaultUser.String(), "standalone", "workflow.json"))
	rec := layoutE2EWaitTerminal(t, &domainStack{layout: ls}, wf.SavedRevision())
	layoutE2EAssertOutcome(t, rec)
	require.Equal(t, wf.ID, rec.Destination.WorkflowID)
	require.Equal(t, 1, layoutE2ECountRows(t, db, "standalone"))
}

func TestLayoutE2E_StackRejectsMissingCollaborators(t *testing.T) {
	for _, field := range []string{"config", "db", "repo", "publications", "registry"} {
		t.Run(field, func(t *testing.T) {
			db := layoutE2EOpenDB(t)
			cfg := layoutE2EConfig(t.TempDir(), 3)
			repo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)
			var pub workflow.RevisionPublicationStore = adaptsqlite.NewPublishedWorkflowRepo(db)
			var reg workflow.Registry = adaptsqlite.NewWorkflowIdentityRepo(db)
			switch field {
			case "config":
				cfg = nil
			case "db":
				db = nil
			case "repo":
				repo = nil
			case "publications":
				pub = nil
			case "registry":
				reg = nil
			}
			_, err := newLayoutStack(cfg, db, repo, pub, reg, nil)
			require.Error(t, err)
		})
	}
}

func TestLayoutE2E_RendererFactoryCleanupAndStopBeforeStart(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	db := layoutE2EOpenDB(t)
	cfg := layoutE2EConfig(t.TempDir(), 3)
	repo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)
	closed := 0
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return nil, nil, func() { closed++ }, fmt.Errorf("factory failed")
	}
	_, err := newLayoutStack(cfg, db, repo, adaptsqlite.NewPublishedWorkflowRepo(db), adaptsqlite.NewWorkflowIdentityRepo(db), nil)
	require.ErrorContains(t, err, "factory failed")
	require.Equal(t, 1, closed)
	layoutRenderReviewerFactory = old
	ls, err := newLayoutStack(cfg, db, repo, nil, nil, nil)
	require.NoError(t, err) // explicit no-registry test mode
	ls.stop()
	ls.start(nil)
	ls.stop()
	var absent *layoutStack
	absent.stop()
}

func TestLayoutE2E_ClosePartialStackIsIdempotent(t *testing.T) {
	var absent *domainStack
	absent.Close(context.Background())
	partial := &domainStack{layout: &layoutStack{}}
	partial.Close(context.Background())
	partial.Close(context.Background())
}
