package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/adapter/renderreview"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

var (
	_ layout.WorkflowAccess = (*workflow.Service)(nil)
	_ layout.InstanceAccess = (*engine.Engine)(nil)
)

// attachQuery waits for the engine so instance access uses its snapshot policy.
func (ls *layoutStack) attachQuery(workflows layout.WorkflowAccess, instances layout.InstanceAccess) {
	ls.query = layout.NewQueryService(ls.reader, ls.requester, workflows, instances, ls.mutations)
}

// layoutQuery avoids handing adapters a typed-nil query interface.
func (ds *domainStack) layoutQuery() *layout.QueryService {
	if ds == nil || ds.layout == nil {
		return nil
	}
	return ds.layout.query
}

const layoutSettlementTimeout = 30 * time.Second

// settleBeforeBoot decides old mutations before seeding and serving. Boot is
// all-or-nothing only for systemic journal failures. Failures confined to a
// destination (typed at source as *workflow.DestinationError) and the boot
// deadline are logged and tolerated: Settle runs again under each
// destination's lock on every read and save, so an unsettled destination
// keeps returning ErrMutationPending while worker recovery retries it.
func settleBeforeBoot(source workflow.CommittedRevisionSource, timeout time.Duration, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := source.CommittedRequests(ctx)
	deadline := errors.Is(ctx.Err(), context.DeadlineExceeded)
	var systemic []error
	for _, leaf := range settlementLeaves(err) {
		var local *workflow.DestinationError
		switch {
		case errors.As(leaf, &local):
			attrs := []any{"error", local}
			for i, t := range local.Targets {
				attrs = append(attrs, fmt.Sprintf("destination%d", i), workflow.ScopeLabel(t.Scope)+"/"+t.Scope.OwnerID.String()+"/"+t.Name)
			}
			if local.Record != "" {
				attrs = append(attrs, "record", local.Record)
			}
			logger.Error("workflow destination left unsettled at boot; it stays blocked until recovery succeeds", attrs...)
		case deadline && errors.Is(leaf, context.DeadlineExceeded):
			logger.Error("boot settlement deadline reached; unsettled destinations stay blocked until worker recovery finishes them", "timeout", timeout, "error", leaf)
		default:
			systemic = append(systemic, leaf)
		}
	}
	return errors.Join(systemic...)
}

// settlementLeaves flattens joined errors so each failure is classified on
// its own; a confined failure never masks a systemic sibling.
func settlementLeaves(err error) []error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*workflow.DestinationError); ok {
		return []error{err}
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range multi.Unwrap() {
			out = append(out, settlementLeaves(e)...)
		}
		return out
	}
	// A single-wrap chain is one failure unless it hides a join further down.
	for inner := errors.Unwrap(err); inner != nil; inner = errors.Unwrap(inner) {
		if _, ok := inner.(*workflow.DestinationError); ok {
			break
		}
		if _, ok := inner.(interface{ Unwrap() []error }); ok {
			return settlementLeaves(inner)
		}
	}
	return []error{err}
}

// Tests replace the embedded build to verify a missing render surface fails boot.
var layoutRenderAssets = dashboard.Assets

func layoutArtifactDir(cfg *config.Config) string {
	if cfg.Layout.ArtifactDir != "" {
		return cfg.Layout.ArtifactDir
	}
	return filepath.Join(filepath.Dir(cfg.Database.Path), "layout-artifacts")
}

// noSandboxWarning names the residual risk of running the review browser
// without Chromium's sandbox (docs/install/README.md "Browser for workflow
// auto-arrangement").
const noSandboxWarning = "layout review browser runs WITHOUT the Chromium sandbox: a renderer compromise " +
	"would run as the arbitron service user with access to its database, workflow files and mounted credentials; " +
	"the browser only loads embedded loopback assets and gets a scrubbed environment, but prefer the sandbox " +
	"(docker compose ships a Chromium seccomp profile with browsernosandbox=false)"

// Browser startup is lazy: missing Chromium fails jobs visibly, not workflow saves.
// A broken embedded build, however, must fail boot rather than disable review.
func newLayoutRenderReviewer(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
	if cfg == nil {
		return nil, nil, nil, errors.New("layout renderer requires a config")
	}
	if logger == nil {
		logger = slog.Default()
	}
	artifactDir := layoutArtifactDir(cfg)
	renderer, err := renderreview.NewBrowserRenderer(renderreview.BrowserConfig{
		Assets: layoutRenderAssets(), BrowserPath: cfg.Layout.BrowserPath,
		NoSandbox: cfg.Layout.BrowserNoSandbox, ArtifactDir: artifactDir, Logger: logger,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building layout browser renderer: %w", err)
	}
	if cfg.Layout.BrowserNoSandbox {
		logger.Warn(noSandboxWarning, "setting", "layout.browsernosandbox / ARBITRON_LAYOUT_BROWSERNOSANDBOX")
	}
	if path, err := renderreview.DetectBrowser(cfg.Layout.BrowserPath); err != nil {
		logger.Warn("no Chrome/Chromium browser found for layout review: every arrangement job will end failed at the rendering stage (retryable) until one is installed or layout.browserpath is set; saved workflows are unaffected", "error", err)
	} else {
		logger.Info("layout review browser", "path", path, "artifactDir", artifactDir, "noSandbox", cfg.Layout.BrowserNoSandbox)
	}
	return renderer, renderreview.NewReadabilityReviewer(logger), renderer.Close, nil
}

var layoutRenderReviewerFactory = newLayoutRenderReviewer

type layoutStack struct {
	store     *adaptsqlite.LayoutRepo
	journal   *filesystem.RevisionJournal
	commits   *workflow.CommitSource
	cache     *filesystem.LayoutCache
	requester *layout.Requester
	stamper   *workflow.RevisionStamper
	reader    *layout.Reader
	query     *layout.QueryService
	mutations workflow.MutationCoordinator
	worker    *layout.Worker
	renderer  layout.Renderer
	reviewer  layout.Reviewer
	closeFns  []func()
	cancel    context.CancelFunc
	mu        sync.Mutex
	stopped   bool
}

// layoutNilPort rejects typed nil collaborators as well as absent interfaces.
func layoutNilPort(v any) bool {
	return v == nil || (reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil())
}

func newLayoutStack(cfg *config.Config, db *sql.DB, repo *filesystem.WorkflowRepo, publications workflow.RevisionPublicationStore, registry workflow.Registry, logger *slog.Logger) (_ *layoutStack, err error) {
	if cfg == nil || db == nil || repo == nil {
		return nil, errors.New("layout stack requires config, database and workflow repository")
	}
	// Both absent is the explicit standalone no-registry mode; production passes both.
	if layoutNilPort(publications) != layoutNilPort(registry) || publications != nil && layoutNilPort(publications) || registry != nil && layoutNilPort(registry) {
		return nil, errors.New("layout stack requires publication proof and registry together")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ls := &layoutStack{store: adaptsqlite.NewLayoutRepo(db), journal: filesystem.NewRevisionJournal(cfg.Workspace.Path), cache: filesystem.NewLayoutCache(repo), mutations: repo}
	defer func() {
		if err != nil {
			ls.stop()
		}
	}()
	ls.requester = layout.NewRequester(ls.store, cfg.Layout.MaxAttempts, logger)
	ls.commits, err = workflow.NewCommitSource(workflow.CommitSourceDeps{Journal: ls.journal, Repo: repo, Mutations: repo, Publications: publications, Trees: repo, Registry: registry, Relocator: ls.requester, Logger: logger})
	if err != nil {
		return nil, err
	}
	repo.SetReadSettler(ls.commits)
	ls.stamper, err = workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: ls.journal, Requester: ls.requester, Displayed: ls.store, Settler: ls.commits, Logger: logger})
	if err != nil {
		return nil, err
	}
	ls.reader = layout.NewReader(ls.store, ls.journal, ls.cache)
	var closeFn func()
	ls.renderer, ls.reviewer, closeFn, err = layoutRenderReviewerFactory(cfg, logger)
	if closeFn != nil {
		ls.closeFns = append(ls.closeFns, closeFn)
	}
	if err != nil {
		return nil, fmt.Errorf("layout renderer: %w", err)
	}
	coordinator, err := layout.NewCoordinator(ls.renderer, ls.reviewer)
	if err != nil {
		return nil, err
	}
	ls.worker, err = layout.NewWorker(layout.WorkerConfig{Store: ls.store, Coordinator: coordinator, Journal: ls.journal, Committed: ls.commits, Manifests: layout.NewRepositoryManifests(repo), Cache: ls.cache, Requester: ls.requester, Logger: logger})
	if err != nil {
		return nil, err
	}
	ls.requester.SetNotifier(ls.worker.Notify)
	return ls, nil
}

func (ls *layoutStack) start(logger *slog.Logger) {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.stopped || ls.cancel != nil || ls.worker == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ls.cancel = cancel
	ls.worker.Start(ctx)
}
func (ls *layoutStack) stop() {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.stopped {
		return
	}
	ls.stopped = true
	if ls.cancel != nil {
		ls.cancel()
	}
	if ls.worker != nil {
		ls.worker.Stop()
	}
	for i := len(ls.closeFns) - 1; i >= 0; i-- {
		ls.closeFns[i]()
	}
}
