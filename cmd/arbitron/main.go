// Package main is the entry point for the Arbitron CLI.
package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/arbitron/arbitron/adapter/backendhealth"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/adapter/dashboard"
	adaptedge "github.com/arbitron/arbitron/adapter/edge"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/adapter/filesystem"
	gitarea "github.com/arbitron/arbitron/adapter/git"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/observability"
	adaptoidc "github.com/arbitron/arbitron/adapter/oidc"
	"github.com/arbitron/arbitron/adapter/outbound"
	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/adapter/process"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	builtinrunner "github.com/arbitron/arbitron/adapter/runner/builtin"
	runnerinventory "github.com/arbitron/arbitron/adapter/runner/inventory"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/config"
	domaindashboard "github.com/arbitron/arbitron/domain/dashboard"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/schedule"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Compile-time assertion that *engine.Engine satisfies the HTTP adapter's EngineService.
var _ adapthttp.EngineService = (*engine.Engine)(nil)

const (
	// deviceCodeTTL is how long a device-authorization code remains valid before
	// it expires unapproved/unconsumed.
	deviceCodeTTL = 10 * time.Minute
	// deviceCodePollInterval is the RFC 8628 poll interval (seconds) returned to
	// the client.
	deviceCodePollInterval = 5
	// deviceCodeSweepInterval is how often expired device codes are purged.
	deviceCodeSweepInterval = 5 * time.Minute
)

// spawnSweepInterval is how often the two spawn reconcilers re-run after the
// startup pass: parents stuck waiting on an already-finished child, and runs
// stranded active under a cancelled parent. Both holes open in a HEALTHY
// process (a lost wake, a partially-failed cascade), so neither is a
// startup-only concern. Both sweeps are indexed single queries that usually
// match nothing, so a minute is cheap; the cost of a longer interval is a
// user staring at a run that looks hung. Variable so composition tests can
// shorten the ticker.
var spawnSweepInterval = 1 * time.Minute

// groupSweepInterval is variable so composition tests can shorten the ticker.
// Production retries missed task callbacks and partially applied group actions
// every minute, in addition to the explicit startup reconciliation.
var groupSweepInterval = time.Minute

// recoveryShutdownTimeout bounds how long Close waits for the spawn and group
// recovery workers (a rollback in flight runs under WithoutCancel) before it
// closes their store anyway. Variable so composition tests can shorten it.
var recoveryShutdownTimeout = 30 * time.Second

// waitRecoveryWorkers waits for the recovery workers in wg and for the gather
// reconciler's watch (watchDone, nil when not started) to return, up to
// timeout. It reports whether they all returned; on timeout it logs.
func waitRecoveryWorkers(wg *sync.WaitGroup, watchDone <-chan struct{}, timeout time.Duration, log *slog.Logger) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if wg != nil {
			wg.Wait()
		}
		if watchDone != nil {
			<-watchDone
		}
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		log.Error("recovery workers still running at shutdown; closing the database anyway", "timeout", timeout)
		return false
	}
}

// sweepSpawnReconcilers re-runs the two spawn reconcilers on a ticker until ctx
// is cancelled. Errors are logged, not fatal — a transient DB error on one tick
// must not kill the loop. Mirrors sweepExpiredDeviceCodes.
func sweepSpawnReconcilers(
	ctx context.Context,
	waits *engine.SpawnWaitReconciler,
	orphans *engine.OrphanedChildReconciler,
	interval time.Duration,
	log *slog.Logger,
) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := waits.Reconcile(ctx); err != nil {
				log.Error("spawn-wait reconciliation incomplete", "error", err)
			}
			if err := orphans.Reconcile(ctx); err != nil {
				log.Error("orphaned-child reconciliation incomplete", "error", err)
			}
		}
	}
}

// groupReconciler is the operation shared by startup and periodic recovery.
type groupReconciler interface {
	Reconcile(ctx context.Context) error
}

func sweepGroupReconciler(ctx context.Context, groups groupReconciler, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := groups.Reconcile(ctx); err != nil {
				log.Error("parallel-group reconciliation incomplete", "error", err)
			}
		}
	}
}

// sweepExpiredDeviceCodes purges expired device-authorization rows on a ticker
// until ctx is cancelled. Errors are logged, not fatal — a transient DB error on
// one tick must not kill the loop.
func sweepExpiredDeviceCodes(ctx context.Context, svc *identity.DeviceCodeService, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if n, err := svc.DeleteExpired(ctx, now.UTC()); err != nil {
				log.Error("device-code sweep failed", "error", err)
			} else if n > 0 {
				log.Debug("device-code sweep removed expired codes", "count", n)
			}
		}
	}
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds the root command with its subcommands and the persistent
// --config flag. Factored out of main() so tests can inspect it.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "arbitron",
		Short:   "Arbitron workflow orchestration engine",
		Long:    "Arbitron is a Go-based workflow orchestration engine with hexagonal architecture.",
		Version: version,
	}

	// Make `--version`/`-v` print the same rich line as the `version` subcommand
	// (version + commit + build date) rather than cobra's bare default.
	rootCmd.SetVersionTemplate(versionString() + "\n")

	rootCmd.PersistentFlags().String("config", "",
		"path to config.yaml (overrides ARBITRON_CONFIG and the default ~/.arbitron/config.yaml)")

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())
	rootCmd.AddCommand(newConnectionCmd())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Arbitron server",
		Long:  "Start the Arbitron HTTP server (with MCP at /mcp), open the SQLite database, and run migrations.",
		RunE:  runServe,
	}
	cmd.Flags().Bool("mcp-stdio", false, "Also start the MCP stdio server alongside the HTTP server (HTTP MCP is always mounted at /mcp)")
	return cmd
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP stdio server only",
		Long:  "Start the Arbitron MCP server over stdin/stdout without the HTTP server.",
		RunE:  runMCP,
	}
}

// domainStack holds the shared dependencies constructed by initDomain.
// HTTP and standalone MCP both call Start before serving and Close on shutdown.
type domainStack struct {
	lifecycleMu     sync.Mutex
	lifecycleCancel context.CancelFunc
	startDone       chan struct{}
	startErr        error
	closed          bool
	closeOnce       sync.Once
	background      sync.WaitGroup
	startWork       func(context.Context) error
	// noAutomaticDiscovery is the stdio MCP entrypoint policy
	// (disableAutomaticDiscovery): no refresh loop is registered.
	noAutomaticDiscovery bool
	// awaitInitialDiscovery makes Start also wait for the background loop's
	// first cycle. Test seam only (defaultAwaitInitialDiscovery); never set in
	// production.
	awaitInitialDiscovery bool
	modelStateCrypto      provider.Crypto
	// modelFingerprint is the keyed (HMAC) fingerprint closure the execution
	// path inventory consumes (B4); the raw key never leaves model_state.go.
	modelFingerprint func([]byte) string
	// executionGate serializes execution-affecting mutations against model
	// advancement and run-snapshot creation. It is shared by every service
	// that edits connections, profiles, workflows or the identity policy.
	executionGate *provider.ExecutionGate
	// catalogStore shares the profile DB and acknowledges confirmed publication
	// of the original advancement journal rows, including rows without reports.
	catalogStore *adaptsqlite.ModelCatalogRepo
	// pathInventory is the gate's revision source: every execution path an alias
	// runs on, with the persisted revisions an advancement commit validates.
	pathInventory *providersadapter.ExecutionPathInventory
	// modelRefresh is the assembled catalog refresh (fetch, classify, discover,
	// advance). It is built here and started only by serve/MCP.
	modelRefresh   *modelRefresh
	refreshDeps    modelRefreshDeps
	SystemSettings *settingsdomain.Service
	// IdentityPolicy is the persisted, admin-editable D2 identity policy. Its
	// mutation gate is injected with the shared ExecutionGate (B4/B12); until
	// then updates fail closed.
	IdentityPolicy      *modelidentity.PolicyService
	layout              *layoutStack
	cfg                 *config.Config
	settings            *config.SettingsStore
	logger              *slog.Logger
	logCloser           io.Closer
	db                  *sql.DB
	eng                 *engine.Engine
	workflowRepo        workflow.Repository
	workflowSvc         *workflow.Service
	auditSvc            *audit.Service
	publishedRepo       *adaptsqlite.PublishedWorkflowRepo
	instanceRepo        instance.InstanceRepository
	nodeExecRepo        instance.NodeExecutionRepository
	snapshotRepo        instance.VariableSnapshotRepository
	fileReader          engine.FileReader
	bus                 engine.EventBus
	queueMgr            *queue.Manager
	queueRepo           instance.QueueRepository
	scheduler           *queue.Scheduler
	schedulerCancel     context.CancelFunc
	scheduleEval        *schedule.Evaluator
	scheduleSvc         *schedule.Service
	webhookRegistry     *adapthttp.WebhookRegistry
	notifRepo           notification.Repository
	dismissalRepo       instance.DismissalRepository
	conversationHandler engine.ConversationHandler
	pinLookup           *providersadapter.PinLookup // run-pin cache shared by the pinning resolver
	profileChecker      workflow.ProfileChecker
	profileResolver     provider.ProfileResolver // the live cache-backed resolver the executors use
	connChecker         workflow.ConnectionChecker
	connStore           provider.ConnectionStore
	// profile-editing Phase 4: admin services exposed through the MCP profile_*
	// and connection_* tools. profileSvc carries the capability ruleset +
	// store-bound cache (its Reload recaches after a write); connSvc is the admin
	// gate over connStore. capabilityRules is retained for the read-only
	// provider_capabilities tool.
	profileSvc      *provider.ProfileService
	connSvc         *provider.ConnectionService
	capabilityRules provider.CapabilityRules
	modelDiscovery  *provider.ModelDiscoveryService
	tracingShutdown func(ctx context.Context) error
	// Auth (Phase 6). sessionRepo is always built; authMW is always built;
	// authHandler is nil in local mode; Start owns the org-only sweeps.
	sessionRepo  identity.SessionRepository
	tokenRepo    identity.TokenRepository
	tokenSvc     *identity.TokenService
	deviceRepo   identity.DeviceCodeRepository
	deviceSvc    *identity.DeviceCodeService
	provisioner  *instance.Provisioner
	userRepo     instance.UserRepository
	groupCfg     identity.GroupConfig
	userSvc      *instance.UserService
	dashboardSvc *domaindashboard.Service
	authMW       *adapthttp.AuthMiddleware
	authHandler  *adapthttp.AuthHandler
	// Parallel-group recovery (#76). groupCoord and gatherReconciler are run by
	// Start after crash recovery and then swept on recoveryWG; groupWatchDone is
	// the gather reconciler's event watch. Close joins both, bounded by
	// recoveryShutdownTimeout, before closing the database, so a recovery pass in
	// flight (a rollback runs under WithoutCancel) never outlives its store.
	groupRunRepo     instance.GroupRunRepository
	groupCoord       *engine.GroupCoordinator
	gatherReconciler *engine.GroupGatherReconciler
	recoveryWG       *sync.WaitGroup
	groupWatchDone   <-chan struct{}
	// backendHealth is the shared, TTL-cached backend-health component (codex
	// install/login readiness). Constructed ONCE here and injected into BOTH the
	// HTTP SystemHandler and the MCP server so the probe forks at most once per
	// TTL across both surfaces (and the unauthenticated local GET /system/config
	// route cannot fork a codex probe pair per request).
	backendHealth *backendhealth.Checker
	// Authorized aggregate run state, shared by the production HTTP router.
	graphState *engine.GraphStateReader
	// subtreeEvents routes authorized descendant events for live run views.
	subtreeEvents   *engine.SubtreeEventBridge
	invocations     engine.InvocationStore
	groupResolution *engine.GroupResolutionBridge
	runners         *runnerStack
	mobility        *workspaceMobility
	remote          *remoteRunnerStack
}

// Close cancels and joins startup, refresh and sweeps before draining execution,
// stopping schedulers and finally closing renderer, tracing, storage and the
// log sink. It also handles partially constructed stacks.
func (ds *domainStack) Close(ctx context.Context) {
	if ds == nil {
		return
	}
	ds.closeOnce.Do(func() { ds.close(ctx) })
}

func (ds *domainStack) close(ctx context.Context) {
	ds.lifecycleMu.Lock()
	ds.closed = true
	cancel, done := ds.lifecycleCancel, ds.startDone
	ds.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ds.modelRefresh != nil {
		ds.modelRefresh.stop()
	}
	if done != nil {
		<-done
	}
	// Start may have installed the loop while Close was cancelling startup.
	if ds.modelRefresh != nil {
		ds.modelRefresh.stop()
	}
	ds.background.Wait()
	// Stop the schedule evaluator FIRST (org-readiness Phase 3): no new scheduled
	// triggers must be created once shutdown begins, so it stops before the engine
	// drains in-flight instances.
	if ds.scheduleEval != nil {
		ds.scheduleEval.Stop()
	}
	if ds.schedulerCancel != nil {
		ds.schedulerCancel()
	}
	// Stop blocks until the scheduler loop exits, so only a scheduler Start
	// actually ran (schedulerCancel is set there) may be stopped.
	if ds.schedulerCancel != nil {
		ds.scheduler.Stop()
	}
	logger := ds.logger
	if logger == nil {
		logger = slog.Default()
	}
	// The lifecycle context (cancelled above) ends the spawn and group sweeps
	// and the gather watch; join them, bounded, before closing the database.
	waitRecoveryWorkers(ds.recoveryWG, ds.groupWatchDone, recoveryShutdownTimeout, logger)
	// Stop the bearer last-used flusher's owned goroutine, draining any pending
	// touches synchronously before the DB closes.
	if ds.authMW != nil {
		ds.authMW.Stop()
	}
	if ds.queueMgr != nil {
		ds.queueMgr.Stop()
	}
	if ds.eng != nil {
		ds.eng.Shutdown()
	}
	// The layout worker and renderer stop after execution drains, before the
	// database they read and write closes.
	ds.layout.stop()
	if ds.tracingShutdown != nil {
		if err := ds.tracingShutdown(ctx); err != nil {
			logger.Error("tracing shutdown error", "error", err)
		}
	}
	// Source alerts deliver external notifications in the background; join
	// them before their notification store closes.
	if ds.runners != nil {
		ds.runners.stopWake()
		ds.runners.dispatcher.Close()
		ds.runners.monitor.Wait()
		ds.runners.alerts.Wait()
		if ds.remote != nil {
			ds.remote.Hub.Wait()
		}
	}
	if ds.db != nil {
		ds.db.Close()
	}
	if ds.logCloser != nil {
		ds.logCloser.Close()
	}
}

// mcpDeps builds the MCP adapter Deps from the shared domain stack.
// Assign Layouts only for a non-nil query, avoiding a typed-nil interface.
func (ds *domainStack) mcpDeps() *adaptmcp.Deps {
	var modelCatalog adaptmcp.ModelCatalogAdmin
	if ds.modelRefresh != nil {
		modelCatalog = ds.modelRefresh.admin
	}
	deps := &adaptmcp.Deps{
		Engine:              ds.eng,
		Workflows:           ds.workflowRepo,
		WorkflowSvc:         ds.workflowSvc,
		AuditSvc:            ds.auditSvc,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		Settings:            ds.settings,
		SystemSettings:      ds.SystemSettings,
		IdentityPolicy:      ds.IdentityPolicy,
		ModelCatalog:        modelCatalog,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		Notifications:       ds.notifRepo,
		ConversationHandler: ds.conversationHandler,
		ProfileChecker:      ds.profileChecker,
		ConnChecker:         ds.connChecker,
		Connections:         ds.connStore,
		ProfileAdmin:        ds.profileSvc,
		ConnectionAdmin:     ds.connSvc,
		ModelDiscovery:      ds.modelDiscovery,
		Capabilities:        ds.capabilityRules,
		Tokens:              ds.tokenSvc,
		Users:               ds.userSvc,
		ScheduleService:     ds.scheduleSvc,
		Logger:              ds.logger,
		BackendHealth:       ds.backendHealth,
	}
	if ds.remote != nil {
		deps.RunnerKeys = ds.remote.Keys
	}
	if q := ds.layoutQuery(); q != nil {
		deps.Layouts = q
	}
	// Assign Runners only for a built pool, avoiding a typed-nil interface.
	if ds.runners != nil {
		deps.Runners = ds.runners.service
	}
	return deps
}

// httpDeps is the production HTTP projection, shared by server assembly and wiring tests.
func (ds *domainStack) httpDeps() adapthttp.RouterDeps {
	notifHandler := adapthttp.NewNotificationHandler(ds.notifRepo)
	notifHandler.SetEventBus(ds.bus)
	deps := adapthttp.RouterDeps{
		Service:             ds.eng,
		Workflows:           ds.workflowRepo,
		WorkflowSvc:         ds.workflowSvc,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		Dismissals:          ds.dismissalRepo,
		DismissalUsers:      ds.userRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		Settings:            ds.settings,
		SystemSettings:      ds.SystemSettings,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		NotifHandler:        notifHandler,
		ConversationHandler: ds.conversationHandler,
		WebhookHandler:      adapthttp.NewWebhookHandler(ds.webhookRegistry, ds.instanceRepo, ds.nodeExecRepo, ds.scheduler, ds.queueRepo, ds.queueMgr),
		ProfileChecker:      ds.profileChecker,
		ConnChecker:         ds.connChecker,
		Auth:                ds.authMW,
		AuthHandler:         ds.authHandler,
		Tokens:              ds.tokenSvc,
		DeviceCodes:         ds.deviceSvc,
		PublicBaseURL:       ds.cfg.Server.PublicBaseURL,
		Users:               ds.userSvc,
		DashboardStats:      ds.dashboardSvc,
		ScheduleHandler:     adapthttp.NewScheduleHandler(ds.scheduleSvc),
		BackendHealth:       ds.backendHealth,
	}
	// Assign Layouts only for a non-nil query, avoiding a typed-nil interface.
	if q := ds.layoutQuery(); q != nil {
		deps.Layouts = q
	}
	// Avoid registering a route with a typed-nil service interface.
	if ds.graphState != nil {
		deps.GraphState = ds.graphState
	}
	if ds.subtreeEvents != nil {
		deps.SubtreeEvents = ds.subtreeEvents
	}
	if ds.remote != nil {
		deps.RunnerKeys = ds.remote.Keys
		deps.RunnerConnect = ds.remote.Connect
	}
	if ds.runners != nil {
		deps.Runners = ds.runners.service
	}
	if ds.mobility != nil {
		deps.RunnerTransfers = ds.mobility.Transfers
	}
	return deps
}

// newOutboundHTTPClient builds the shared outbound HTTP client threaded into
// every credential-bearing LLM chat adapter (openrouter/azurefoundry/
// anthropicapi/bedrock). timeout is an absolute per-call backstop independent of
// the context deadline. SECURITY (defense-in-depth): it clones the stdlib
// default transport (preserving connection pooling/proxy/keep-alive behavior)
// and pins an explicit MinVersion: tls.VersionTLS12. The Go default is already
// TLS 1.2+, so this only makes the floor explicit and regression-proof.
func newOutboundHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// canonicalWorkspacePath creates the root when missing and resolves it once so
// workspaces, Git staging paths and provider WorkDir pins use the same spelling.
func canonicalWorkspacePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("workspace path is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %q: %w", p, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create workspace root %q: %w", abs, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root %q: %w", abs, err)
	}
	return resolved, nil
}

// initDomain constructs configuration, storage, adapters and services without
// seeding, recovery or background execution. The caller must Start before
// serving and Close even when Start fails.
func initDomain(ctx context.Context, configPath string) (*domainStack, error) {
	return initDomainWithModelDependencies(ctx, configPath, ModelDependencies{})
}

func initDomainWithModelStateSecret(ctx context.Context, configPath string, injectedSecret []byte) (*domainStack, error) {
	return initDomainWithModelDependencies(ctx, configPath, ModelDependencies{ModelStateSecret: injectedSecret})
}

func initDomainWithModelDependencies(ctx context.Context, configPath string, modelDeps ModelDependencies) (*domainStack, error) {
	// 1. Load configuration from the resolved path.
	slog.Info("loading configuration", "path", configPath)
	cfg, err := configadapter.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// 2. Set up structured logging.
	logger, logCloser, err := observability.SetupLogging(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("setting up logging: %w", err)
	}
	// Every startup failure below releases what this function has opened so
	// far; only a fully assembled stack takes ownership (domainStack.Close).
	assembled := false
	defer func() {
		if !assembled {
			logCloser.Close()
		}
	}()

	for _, warning := range cfg.LoadWarnings {
		logger.Warn(warning)
	}

	// 3. Open SQLite database.
	logger.Info("opening database", "path", cfg.Database.Path)
	db, err := adaptsqlite.Open(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	defer func() {
		if !assembled {
			db.Close()
		}
	}()
	logger.Info("database connection established")

	// 4. Run migrations.
	logger.Info("running database migrations")
	if err := adaptsqlite.RunMigrations(db); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	logger.Info("database migrations complete")

	modelStateCrypto, modelFingerprint, err := initModelState(ctx, db, cfg.Database.Path, modelDeps.ModelStateSecret)
	if err != nil {
		return nil, fmt.Errorf("initializing model-state secret: %w", err)
	}

	// 5. Initialize adapters.
	instanceRepo := adaptsqlite.NewInstanceRepo(db)
	dashboardRepo := adaptsqlite.NewDashboardRepo(db)
	dashboardSvc := domaindashboard.NewService(dashboardRepo)
	nodeExecRepo := adaptsqlite.NewNodeExecutionRepo(db)
	snapshotRepo := adaptsqlite.NewVariableSnapshotRepo(db)
	userRepo := adaptsqlite.NewUserRepo(db)

	// 5a. Auth (Phase 6). The session store is always constructed (migration 003
	// created its table); the middleware and OIDC provider differ by mode. In org
	// mode an unreachable/invalid issuer is a fatal boot error (fail-fast,
	// consistent with the other init gates).
	sessionRepo := adaptsqlite.NewSessionRepo(db)
	// Personal API token store + service (Phase 6, Phase 2). The service scopes
	// every operation to the request identity; it powers the REST /tokens routes
	// and the token_* MCP tools, and is consumed by the bearer middleware branch.
	tokenRepo := adaptsqlite.NewTokenRepo(db)
	tokenSvc := identity.NewTokenService(tokenRepo)
	// Device authorization grant (Phase 6, Phase 2). Browserless clients log in
	// via /auth/device/* and receive a minted arb_ token. 10m TTL, 5s poll.
	deviceRepo := adaptsqlite.NewDeviceCodeRepo(db)
	deviceSvc := identity.NewDeviceCodeService(deviceRepo, tokenSvc, deviceCodeTTL, deviceCodePollInterval)
	// JIT provisioner (Phase 6, Phase 2). Resolves verified OIDC claims to a
	// persisted user, creating bootstrap identities as admins. Shared by the OIDC
	// callback and Task 6's role-management demotion floor (one bootstrap rule).
	groupCfg, err := configadapter.ToGroupConfig(cfg.Auth.RBAC)
	if err != nil {
		// A chained department mapping yields non-deterministic, boot-dependent
		// RBAC scoping; fail the boot loudly so the operator flattens the config.
		return nil, fmt.Errorf("invalid RBAC group mapping: %w", err)
	}

	// Remap reconcile (final-org-gaps Phase 2). Adding/changing a department
	// mapping must rewrite historical department strings so the friendly name is
	// seen everywhere (memberships, runs, schedules, department-visibility
	// publications). This runs ONCE at boot, after migrations + config load and
	// before serving; it is idempotent, so a clean boot with no new mappings is a
	// cheap no-op. A DB error here fails the boot loudly (fail-fast) rather than
	// serving a mis-scoped/duplicated department.
	deptRenamer := adaptsqlite.NewDepartmentRenamer(db)
	if err := identity.ReconcileMappings(ctx, groupCfg, deptRenamer, logger); err != nil {
		return nil, fmt.Errorf("reconciling department mappings: %w", err)
	}
	logger.Info("department mapping reconcile complete")

	provisioner := instance.NewProvisioner(userRepo, cfg.Auth.BootstrapAdmins, groupCfg)
	// Admin-only role management (Phase 6, Phase 2). The demotion floor shares the
	// provisioner's bootstrap rule so a bootstrap (incl. by-email) admin is
	// protected using only the persisted User.
	userSvc := instance.NewUserService(userRepo, provisioner)
	authMode := identity.ModeFromString(cfg.Auth.Mode)
	var authHandler *adapthttp.AuthHandler
	if authMode == identity.ModeOrg {
		oidcProvider, oerr := adaptoidc.New(ctx, adaptoidc.Config{
			IssuerURL:    cfg.Auth.OIDC.IssuerURL,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
			GroupClaim:   cfg.Auth.RBAC.GroupClaim,
		})
		if oerr != nil {
			return nil, fmt.Errorf("initialising OIDC provider: %w", oerr)
		}
		authHandler = adapthttp.NewAuthHandler(oidcProvider, sessionRepo, userRepo, provisioner, cfg.Auth.Session)
	}
	// Edge mode (org-readiness Phase 1): construct the Cloudflare-Access JWT
	// verifier from the public team domain + audiences (no secret). New makes the
	// boot-time discovery call and fails fast on an unreachable/non-compliant team
	// domain. The middleware is stateless (no session/token repos consulted) and
	// no Arbitron login routes are mounted — sign-in/out happen at the edge.
	var authMW *adapthttp.AuthMiddleware
	if authMode == identity.ModeEdge {
		edgeVerifier, eerr := adaptedge.New(ctx, adaptedge.Config{
			TeamDomain: cfg.Auth.Edge.TeamDomain,
			Audiences:  cfg.Auth.Edge.Audiences,
			GroupClaim: cfg.Auth.RBAC.GroupClaim,
		})
		if eerr != nil {
			return nil, fmt.Errorf("initialising edge verifier: %w", eerr)
		}
		authMW = adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, edgeVerifier, provisioner, userRepo, groupCfg)
	} else {
		authMW = adapthttp.NewAuthMiddleware(authMode, cfg.Auth.Session.CookieName, sessionRepo, tokenRepo, userRepo, groupCfg)
	}

	defer func() {
		if !assembled {
			authMW.Stop()
		}
	}()

	// Canonicalize before constructing filesystem adapters and provider pins.
	// The deferred cleanup closes the database and log on failure.
	canonicalWS, err := canonicalWorkspacePath(cfg.Workspace.Path)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	cfg.Workspace.Path = canonicalWS

	// 6. Create filesystem-based repos.
	workflowRepo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)
	fileReader := filesystem.NewFileReader(cfg.Workspace.Path)

	// One-time, marker-guarded migration of any pre-existing flat workflow store
	// into the default user's private workspace (users/<defaultUser>/). Runs
	// after the DB migrations seed the default user and before serving begins.
	flatMigrateUser, err := userRepo.GetDefaultUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving default user for flat-store migration: %w", err)
	}
	if err := migrateFlatStore(cfg.Workspace.Path, flatMigrateUser); err != nil {
		return nil, fmt.Errorf("flat-store migration: %w", err)
	}
	logger.Info("flat-store migration complete", "defaultUser", flatMigrateUser)
	// PublishedWorkflowRepo is the authority for the shared workspace's
	// unique-name / publisher-only-overwrite rules and the PublisherLookup the
	// Service uses to tag shared list entries with their publisher.
	publishedRepo := adaptsqlite.NewPublishedWorkflowRepo(db)
	// One identity authority supplies Service, Seeder and durable commit proof.
	identityRepo := adaptsqlite.NewWorkflowIdentityRepo(db)
	ls, err := newLayoutStack(cfg, db, workflowRepo, publishedRepo, identityRepo, logger)
	if err != nil {
		return nil, fmt.Errorf("constructing layout stack: %w", err)
	}
	defer func() {
		if !assembled {
			ls.stop()
		}
	}()
	// Raw reads of workflow.json/layout.json settle through the repository's read settler.
	fileReader.SetSettledRepository(workflowRepo)
	// Decide old mutations before the first seed, backfill or external graph read.
	// Only systemic journal failures abort boot; see settleBeforeBoot.
	if settlementErr := settleBeforeBoot(ls.commits, layoutSettlementTimeout, logger); settlementErr != nil {
		return nil, fmt.Errorf("settling workflow mutations before boot: %w", settlementErr)
	}
	// Service resolves storage scope/authorization from the request
	// identity.
	workflowSvc := workflow.NewWorkflowService(workflowRepo, publishedRepo)
	workflowSvc.SetRegistry(identityRepo)
	workflowSvc.SetRevisionStamper(ls.stamper)
	workflowSvc.SetMutationCoordinator(ls.mutations)
	// The workflow-stats read path is gated by the service's visibility predicate
	// (CanSeePublication / ownership); wire the instance repo behind that boundary
	// so GET /workflows/{name}/stats cannot read across user/department scopes.
	workflowSvc.SetStatsReader(instanceRepo)
	// Admin Delete guard: refuse deletion while non-terminal instances carry the
	// workflow id. The instance repo counts them by stable id (migration 017).
	workflowSvc.SetActiveInstanceCounter(instanceRepo)
	// Best-effort audit emitter for publish/visibility/admin-override events. The
	// engine is wired to this same auditSvc below (Phase 3 task 5) via
	// eng.SetAuditRecorder(auditSvc), once eng exists — a single engine seam.
	auditRepo := adaptsqlite.NewAuditRepo(db)
	auditSvc := audit.NewService(auditRepo)
	auditSvc.SetLogger(logger)
	workflowSvc.SetAudit(auditSvc)
	// @lib file store behind the service's library methods.
	libStore := wireLibrary(workflowSvc, cfg.Workspace.Path)

	// 7. Create the chat + agent providers based on config.
	//
	// SECURITY: WorkspaceRoot is the absolute workspace path; both adapters use
	// it to pin every per-request WorkDir under this subtree. The chat adapter
	// is tool-suppressed by construction (no AllowedTools / SkipPermissions); the
	// agent adapter carries the full tool scope.
	wsRoot, err := filepath.Abs(cfg.Workspace.Path)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace path: %w", err)
	}

	// Resolve the capability-ruleset override path: empty defaults to
	// ~/.arbitron/capabilities.yaml (resolved like the config path). A missing
	// file is not an error — LoadCapabilityRules falls back to the embedded
	// default ruleset. Loaded here (before the adapter Configs) so the rules can
	// be threaded into the claudecli + codex adapters for runtime context-window +
	// effort emission (profile-editing Phase 3); the SAME value is reused below for
	// the one-time profile seed validation.
	capabilitiesPath := cfg.CapabilitiesPath
	if capabilitiesPath == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			capabilitiesPath = filepath.Join(home, ".arbitron", "capabilities.yaml")
		}
	}
	capabilityRules, err := configadapter.LoadCapabilityRules(capabilitiesPath)
	if err != nil {
		return nil, fmt.Errorf("loading capability rules: %w", err)
	}

	chatCfg := claudecli.Config{
		Runner:           modelDeps.Runner,
		BinaryPath:       cfg.LLM.BinaryPath,
		DefaultModel:     cfg.LLM.Model,
		WorkspaceRoot:    wsRoot,
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		// Capability rules drive the per-call context-window + effort env emission
		// (profile-editing Phase 3). The factory's claude-cli chat arm uses this
		// config to build per-binding ChatAdapters so a chat-kind profile's
		// ContextWindow + Effort axes reach the subprocess env.
		CapabilityRules: capabilityRules,
		// chat is tool-suppressed by construction: no AllowedTools, no SkipPermissions.
	}

	// The agent backend for the empty-connection path is the claude-cli agent
	// adapter: every Claude/subscription agent profile resolves to it (design
	// §4). Non-Claude agent profiles route to opencode via the factory below.
	agentCfg := chatCfg
	agentCfg.SkipPermissions = cfg.LLM.SkipPermissions
	agentCfg.AllowedTools = cfg.LLM.AllowedTools // full agent scope default
	claudeChatAdapter := claudecli.NewChatAdapter(chatCfg)
	claudeAgentAdapter := claudecli.NewAgentAdapter(agentCfg)

	// Workflow launches share one supervisor; discovery and native probes keep
	// the unsupervised adapters because they run outside ledger assignments.
	builtinSup := builtinrunner.NewProcessSupervisor(adaptsqlite.NewBuiltInProcessRepo(db), builtinrunner.SupervisorOptions{})
	builtinExec := modelDeps.Runner
	if builtinExec == nil {
		builtinExec = claudecli.NewSupervisedExecRunner(builtinSup)
	}
	builtinChatCfg, builtinAgentCfg := chatCfg, agentCfg
	builtinChatCfg.Runner, builtinAgentCfg.Runner = builtinExec, builtinExec

	// 8. Create in-process event bus.
	bus := eventbus.New()

	// 8a. Bootstrap connection secrecy: load the master key (env/keyring), build
	// the AES-GCM crypto only when a key is present, and construct the connection
	// store. The master key is held in memory only and is never logged or
	// persisted; LoadMasterKey logs the source only.
	masterKey, err := adaptcrypto.LoadMasterKey(logger)
	if err != nil && !errors.Is(err, provider.ErrMasterKeyMissing) {
		// An invalid key (bad base64 / wrong length) is a hard error.
		return nil, fmt.Errorf("loading master key: %w", err)
	}
	haveKey := masterKey != nil

	var connCrypto provider.Crypto
	if haveKey {
		connCrypto, err = adaptcrypto.NewAESGCM(masterKey)
		if err != nil {
			return nil, fmt.Errorf("building crypto: %w", err)
		}
	}

	// 8b. Construct the connection store over the SQLite DB + crypto. A nil
	// crypto (Claude-only, no master key) yields a store that can still count
	// rows; the fail-fast gate below ensures no connections exist in that case.
	connStore := adaptsqlite.NewConnectionRepo(db, connCrypto)

	// 8c. Fail-fast gate: refuse to start when connections exist but no master
	// key is available, because their ciphertext could never be decrypted. A
	// Claude-only deployment (zero connections) boots with no master key.
	connCount, err := connStore.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting connections: %w", err)
	}
	if err := adaptcrypto.RequireMasterKeyIfConnections(haveKey, connCount); err != nil {
		return nil, err
	}

	// 8d. Build the DB-backed profile resolver (profile-editing Phase 2). Profiles
	// now live in SQLite (the source of truth) behind an in-memory ProfileCache; a
	// DynamicProfileResolver reads the cache so the executors resolve a node's
	// alias to a concrete model+axes through it, and workflow validation checks
	// alias/kind through the ResolverChecker wrapper. The resolver carries the
	// connection store so a profile with a non-empty ConnectionRef resolves to its
	// endpoint + lazy key reveal; an empty ConnectionRef stays on the claude-cli
	// no-key subscription path.
	//
	// One-time seed: the FIRST boot with an empty profiles table seeds it from the
	// config-file profiles (∪ the built-in defaults the loader already merged in).
	// Each candidate is validated against the capability ruleset; an invalid one
	// is logged and skipped so operator config never blocks boot. After the first
	// seed the DB owns the snapshot: unfinished entries retry on later boots,
	// while completed rows and later config edits are left alone. The seed itself
	// runs below, after the ProfileService has its candidate
	// source (see SetModelSelection).
	domainProfiles := configadapter.ProfilesFromConfig(cfg.Profiles)

	profileRepo := adaptsqlite.NewProfileRepo(db)

	// profileCache is bound to the repo at construction so it can be reloaded
	// after a write (the Phase-4 ProfileService recache hook); keep the reference
	// for that future wiring. Load it once before serving.
	profileCache := providersadapter.NewProfileCache(profileRepo)
	if err := profileCache.Load(ctx); err != nil {
		return nil, fmt.Errorf("loading profile cache: %w", err)
	}
	profileResolver := providersadapter.NewDynamicProfileResolver(profileCache, connStore)
	profileChecker := providersadapter.NewResolverChecker(profileResolver)
	// Execution consumers resolve through the pinning decorator so a
	// post-migration run keeps the models pinned at trigger; a legacy (NULL-pin)
	// run and out-of-run calls resolve live. Validation (profileChecker) and the
	// snapshotter keep the live resolver.
	pinLookup := providersadapter.NewPinLookup(instanceRepo)
	pinningResolver := provider.NewPinningResolver(profileResolver, pinLookup, engine.InstanceIDFromContext)

	// Parallel-group writes require the same profile and owner-aware file
	// checkers used by validation. Wire them here, once both are available.
	workflowSvc.SetGroupCheckers(profileChecker, fileReader)

	workflowSvc.SetReferenceBackfiller(adaptsqlite.NewReferenceBackfiller(db))
	// seedWorkflows seeds the bundled samples and reconciles the workflow
	// registry; every step is idempotent. It runs here, so a built stack serves
	// the workflows already on disk, and again in Start AFTER the config profiles
	// are seeded: group authoring validates resolver profiles against the live
	// checker, so a group whose profiles come from config is skipped as invalid on
	// the first pass and registered on the second.
	seedWorkflows := func(ctx context.Context) {
		// Import writers need the live profile checker before touching any store.
		// One-time, marker-guarded seeding of bundled sample workflows into the
		// shared scope (public, owned by the default user). Runs after the published
		// repo exists so the publication records can be written.
		//
		// Seeding is a non-essential convenience subsystem, so a seeding error is
		// logged and tolerated rather than fatal — it must never wedge server boot.
		// Combined with the seeder's re-entrancy, a crash-interrupted seed self-heals
		// on the next boot instead of permanently failing with ErrAlreadyExists.
		switch {
		case cfg.Samples.Seed && cfg.Samples.SourceDir != "":
			if err := seedSampleWorkflows(ctx, cfg.Samples.SourceDir, cfg.Workspace.Path, workflowRepo, publishedRepo, ls.stamper, ls.mutations, identityRepo, libStore, fileReader, flatMigrateUser, logger, profileChecker); err != nil {
				logger.Error("sample workflow seeding failed; continuing boot without samples", "err", err)
			} else {
				logger.Info("sample workflow seeding complete",
					"profiles_note", "the feature-pipeline stage samples need the DB-only admin profiles listed in each stage's samples/<stage>/PROFILES.md (legacy-feature-pipeline: samples/legacy-feature-pipeline/PROFILES.md); create them before running it")
			}
		case cfg.Samples.Seed:
			// seed=true but no source dir resolved: the stock default. Make the
			// silent no-op observable so an operator expecting samples knows why
			// none appeared and how to fix it (set samples.sourcedir).
			logger.Info("sample seeding enabled but no source directory configured; nothing will be seeded (set samples.sourcedir to the install's samples/ path)")
		}

		// One-time startup reconciliation: register any unregistered (legacy id-less)
		// workflow under the default user's private scope and the shared scope. Run
		// under an admin system identity so the scan can persist backfilled ids. It is
		// idempotent and best-effort: a failure is logged, not fatal. Invalid groups
		// remain untouched; Load may assign an id in memory but never persists it.
		backfillCtx := identity.WithIdentity(ctx, identity.Identity{
			UserID: flatMigrateUser,
			Role:   identity.RoleAdmin,
		})
		if err := workflowSvc.BackfillRegistry(backfillCtx); err != nil {
			logger.Warn("workflow registry backfill incomplete", "error", err)
		}
		// One-time reference backfill: stamp each reference row's resolved workflow id
		// onto instances/schedules/published_workflows (Phase 2 task 1). It depends on
		// the registry rows the BackfillRegistry above just reconciled, so it runs
		// AFTER it. Additive and idempotent: names that no longer resolve stay NULL and
		// are Warn-logged, not fatal.
		if err := workflowSvc.BackfillReferences(backfillCtx); err != nil {
			logger.Warn("workflow reference backfill incomplete", "error", err)
		}
		// One-time startup orphan self-heal (PR #44 Consider #2): reconcile any
		// dangling workflow_id reference (a non-NULL id whose identity was deleted) back
		// to NULL, reusing the reference-backfill machinery. Idempotent and best-effort:
		// a failure is logged, not fatal. Runs AFTER BackfillReferences so freshly
		// stamped ids are considered.
		if err := workflowSvc.SweepOrphans(backfillCtx); err != nil {
			logger.Warn("workflow orphan sweep incomplete", "error", err)
		}
	}
	seedWorkflows(ctx)

	// profile-editing Phase 4: the admin-facing editing services. ProfileService
	// owns authz + capability validation + recache: the store-bound profileCache
	// satisfies provider.Reloader directly (its Reload(ctx) re-reads the DB), so it
	// is passed verbatim and a write reloads the SAME cache the resolver reads — a
	// live edit takes effect with no restart. ConnectionService is the admin gate
	// over the raw connStore (Reveal stays off the management surface). Both are
	// injected into the MCP Deps below.
	// Wrap the store-bound profileCache (the recache hook) in a loggingReloader so
	// a post-write recache failure is WARN-logged rather than silently swallowed:
	// without it the engine keeps resolving the STALE profile after a {status:
	// updated} response until the next restart (a credential-binding split-brain).
	// The decorator lives in the wiring layer, keeping the domain logger-free.
	profileReloader := newLoggingReloader(profileCache, logger)
	profileSvc := provider.NewProfileService(profileRepo, capabilityRules, profileReloader)
	// profileRepo (the live source of truth) backs ConnectionService's delete-time
	// in-use check: deleting a connection a profile still references is rejected
	// fail-closed, so an orphaned reference surfaces at delete time rather than at
	// a far-removed workflow run/validate failure.
	// Remote runners (Phase 1): register the built-in runner and its sources
	// (harness presence + connections) before anything can execute, and
	// re-sync after every admin connection write, so a new connection is a
	// placeable source immediately. The sync runs inside the connection
	// service's exclusive execution lease.
	runnerRegistry := adaptsqlite.NewRunnerRegistryRepo(db)
	claudeRunner := modelDeps.Runner
	if claudeRunner == nil {
		claudeRunner = claudecli.NewExecRunner()
	}
	codexCfg := codex.Config{
		Runner:           modelDeps.Runner,
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
	}
	sourceSync := newBuiltinSourceSync(builtinSourceSyncDeps{
		Registry: runnerRegistry,
		Inventory: runnerinventory.Config{
			ClaudeBinary: cfg.LLM.BinaryPath, Codex: codexCfg, Runner: claudeRunner, Connections: connStore,
		},
		StepLimit:     cfg.Runners.DefaultStepLimit,
		RunnerVersion: version,
		WorkspaceRoot: wsRoot,
		Logger:        logger,
	})
	if err := sourceSync.Sync(ctx); err != nil {
		return nil, fmt.Errorf("registering the built-in runner: %w", err)
	}
	connSvc := provider.NewConnectionService(newSyncingConnStore(connStore, sourceSync.Sync), profileRepo)

	// 8e. Build the connection-existence checker for workflow validation over the
	// loaded profiles + the connection store. It confirms a profile's non-empty
	// ConnectionRef names an existing connection at workflow_validate time.
	connChecker := providersadapter.NewConnChecker(domainProfiles, connStore)

	// 8f. Build the provider-selection factory: it maps a resolved binding's
	// Provider to a concrete backend per node (empty/claude-cli -> the claude-cli
	// adapters; openrouter/litellm -> adapter/openrouter; azure-* ->
	// adapter/azurefoundry; non-Claude agent -> adapter/opencode). The executors
	// hold this factory as the domain selector port and never import an adapter.
	//
	// Capability validation stays load-bearing with NO new code: an agent_call
	// whose profile resolves to a chat-only adapter is impossible by construction
	// — the profile is kind: chat and already fails workflow_validate (Phase 1).
	// SECURITY (hang-forever DoS backstop): the HTTP adapters rely on the per-call
	// withTimeout(ctx) derived from cfg.LLM.TimeoutSeconds (now floored > 0 at
	// config load). Give the shared client its own Timeout as an absolute backstop
	// independent of ctx so a wedged endpoint can never block a node forever even
	// if the per-call deadline plumbing regresses. A dedicated client (not the
	// global http.DefaultClient) is used so this Timeout does not leak to other
	// callers; the transport still pools connections and pins an explicit TLS 1.2
	// floor (see newOutboundHTTPClient).
	httpClient := newOutboundHTTPClient(time.Duration(cfg.LLM.TimeoutSeconds) * time.Second)
	if modelDeps.ProviderTransport != nil {
		httpClient.Transport = modelDeps.ProviderTransport
	}

	// Share the outbound transport, timeout and response cap with discovery and
	// LunaRoute agent diagnosis. The CLI validate command owns its own factory.
	probeFactory := adaptprobe.NewProbeFactory(adaptprobe.Deps{
		HTTPClient:       httpClient,
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		ClaudeBinaryPath: cfg.LLM.BinaryPath,
	})
	factoryDeps := providersadapter.Deps{
		ClaudeChat:  claudeChatAdapter,
		ClaudeAgent: claudeAgentAdapter,
		// profile-editing Phase 3: chat Config so the claude-cli chat arm builds
		// a PER-BINDING adapter that applies ContextWindow + Effort env vars from
		// a chat-kind profile to the chat subprocess. An empty/default binding
		// degrades to the subscription env with no axis additions.
		ClaudeChatConfig: chatCfg,
		// org-readiness Phase 2: the claude agent Config so the claude-code arm
		// builds a PER-BINDING adapter that injects ANTHROPIC_BASE_URL + key from
		// the binding (the org gateway / a configured provider). An empty binding
		// degrades to the local subscription env.
		ClaudeAgentConfig: agentCfg,
		HTTPClient:        httpClient,
		Probes:            probeFactory,
		HTTPTimeout:       time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		Runner:            modelDeps.Runner,
		OpencodeTimeout:   time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		// Codex (keyed LunaRoute or subscription): binary auto-resolved from PATH
		// (CodexBinaryPath left empty, mirroring opencode); the per-mission timeout
		// reuses the shared LLM timeout floor. No allow-all knob: codex runs under
		// the fixed v1 bypass posture (see adapter/codex args.go).
		CodexTimeout: time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		// pi (PiBinaryPath empty: PATH plus the standard install dirs) shares the
		// same per-mission floor, so a node without its own timeout is bounded.
		PiTimeout: time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		// SECURITY (OOM DoS): cap the per-request response/stdout read across all
		// the credential-bearing backends (openrouter, azurefoundry, anthropicapi,
		// bedrock, opencode). Dropping this lets an attacker-pointed base_url stream
		// an unbounded body into io.ReadAll and OOM-kill the engine. 256MiB default.
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		// SECURITY: agent permission posture (knob (a)) defaults CLOSED. Allow-all
		// is an explicit operator opt-in via llm.agentallowalltools=true; on a
		// credential-bearing opencode subprocess fed model-influenced prompts an
		// allow-all posture is RCE-equivalent (the lethal trifecta).
		OpencodeAllowAllTools: cfg.LLM.AgentAllowAllTools,
		// profile-editing Phase 3: thread the loaded capability rules into the
		// codex arm so a profile's resolved effort reaches `codex exec`. The
		// claude-code arm receives its rules through ClaudeAgentConfig (= agentCfg,
		// which carries CapabilityRules).
		CapabilityRules: capabilityRules,
	}
	providerFactory := providersadapter.NewFactory(factoryDeps)
	builtinFactoryDeps := factoryDeps
	builtinFactoryDeps.ClaudeChat = claudecli.NewChatAdapter(builtinChatCfg)
	builtinFactoryDeps.ClaudeAgent = claudecli.NewAgentAdapter(builtinAgentCfg)
	builtinFactoryDeps.ClaudeChatConfig, builtinFactoryDeps.ClaudeAgentConfig = builtinChatCfg, builtinAgentCfg
	builtinFactoryDeps.Runner = builtinExec
	builtinFactory := providersadapter.NewFactory(builtinFactoryDeps)
	modelDiscovery := provider.NewModelDiscoveryService(connStore, probeFactory, capabilityRules)

	// 9r. Remote runners (Phase 1). The pool is assembled before the
	// executors: its source alerts need the notification store and external
	// notifier, and its built-in runner executes interactive turns with the
	// undecorated interactive selector, so those are built here.

	// 12b. Create the synchronized SettingsStore that owns the runtime-mutable
	// Teams settings, shared across the HTTP adapter, the MCP adapter, and the
	// Teams notifier so all three route reads/writes through one mutex.
	settings := config.NewSettingsStore(&cfg.Notifications.Teams)
	// 12c. Create the fan-out external notifier (log + best-effort Teams). The
	// Teams child reads the live webhook config through the store, so an admin
	// toggle via the dashboard / MCP takes effect on the next notification.
	externalNotifier := adaptnotif.NewDefaultExternalNotifier(settings, cfg.Server.PublicBaseURL, logger)
	if modelDeps.TeamsTransport != nil {
		externalNotifier = adaptnotif.NewMultiNotifier(logger, adaptnotif.NewLogNotifier(logger),
			adaptnotif.NewTeamsNotifier(settings, cfg.Server.PublicBaseURL, outbound.NewGuardedClientWithTransport(10*time.Second, 0, modelDeps.TeamsTransport), logger))
	}
	notifRepo := adaptsqlite.NewNotificationRepo(db)

	// 12c. Build the profile-aware interactive stream selector: chat profiles
	// route to the tool-suppressed chat stream (via the provider factory), agent
	// profiles route by BACKEND to the matching streaming provider. The tool/
	// sandbox POSTURE is backend-defined (each provider's own implementation
	// detail), NOT uniformly tool-enabled: claude-code → the claude-cli stream
	// adapter (tool-enabled / autonomous), codex → the codex INTERACTIVE provider
	// (`codex exec` / `codex exec resume`, subscription-only): a workspace-write
	// sandbox with writable_roots pinned to the run's WorkDir worktree and network
	// egress off (the autonomous batch arm runs the SAME `codex exec` binary under
	// the fully-open bypass posture). An empty agent backend is floored to the SAME deployment default the
	// batch AgentCallExecutor uses (cfg.LLM.DefaultAgentBackend); opencode and
	// unknown backends fail closed.
	codexInteractiveConfig := codex.InteractiveConfig{
		Runner: modelDeps.Runner,
		// Codex (subscription-only): each interactive turn runs `codex exec --json`
		// (fresh) / `codex exec resume --json` (resume); binary auto-resolved from
		// PATH, the per-turn timeout reuses the shared LLM timeout floor (bounding a
		// wedged turn). The interactive posture is codex's workspace-write sandbox with
		// writable_roots pinned to the turn's WorkDir worktree and network egress off
		// (see buildInteractiveExecArgs), so codex's sandbox is the write/egress
		// containment, with no per-mutation approval loop (reads are NOT confined).
		// MaxResponseBytes caps the exec stdout pipe.
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		// profile-editing Phase 3: the binding's resolved effort rides the protocol
		// body via these rules on the interactive_agent streaming path too.
		CapabilityRules: capabilityRules,
	}
	// The codex exec protocol (sandbox posture, event vocabulary, exec-resume) is
	// VERIFIED against codex-cli 0.139. The codex interactive arm stays behind an
	// explicit operator opt-in
	// (ARBITRON_CODEX_INTERACTIVE_VERIFIED): default off → the codex backend is
	// absent from the stream map and the selector fails closed
	// (ErrUnsupportedAgentBackend), so a host without a compatible codex binary
	// cannot accidentally route interactive_agent turns to it.
	agentStreams := map[string]providersadapter.AgentStreamFactory{
		workflow.BackendClaudeCode: func(b provider.ResolvedBinding) engine.ChatProvider {
			return claudecli.NewStreamAgentAdapterForBinding(agentCfg, b)
		},
	}
	if codexInteractiveVerified(os.Getenv(codexInteractiveVerifiedEnv)) {
		agentStreams[workflow.BackendCodex] = func(b provider.ResolvedBinding) engine.ChatProvider {
			cfg := codexInteractiveConfig
			cfg.Binding = b
			return codex.NewInteractiveProvider(cfg)
		}
		logger.Warn("codex interactive provider ENABLED via " + codexInteractiveVerifiedEnv +
			" — codex exec protocol verified against codex-cli 0.139 (workspace-write sandbox, writable_roots pinned to WorkDir, network egress off; reads NOT confined — transcripts may contain host secrets)")
	} else {
		logger.Info("codex interactive provider disabled; set " + codexInteractiveVerifiedEnv +
			"=true to enable the codex interactive_agent backend")
	}
	interactiveStreamSel := providersadapter.NewInteractiveStreamSelector(
		providerFactory,
		agentStreams,
		cfg.LLM.DefaultAgentBackend,
	)

	builtinAgentStreams := map[string]providersadapter.AgentStreamFactory{
		workflow.BackendClaudeCode: func(b provider.ResolvedBinding) engine.ChatProvider {
			return claudecli.NewStreamAgentAdapterForBinding(builtinAgentCfg, b)
		},
	}
	if _, ok := agentStreams[workflow.BackendCodex]; ok {
		builtinAgentStreams[workflow.BackendCodex] = func(b provider.ResolvedBinding) engine.ChatProvider {
			cfg := codexInteractiveConfig
			cfg.Binding, cfg.Runner = b, builtinExec
			return codex.NewInteractiveProvider(cfg)
		}
	}
	builtinStreamSel := providersadapter.NewInteractiveStreamSelector(builtinFactory, builtinAgentStreams, cfg.LLM.DefaultAgentBackend)

	// Resolve the execution root before constructing the runner pool.
	wsMgr, err := filesystem.NewWorkspaceManager(cfg.Workspace.Path)
	if err != nil {
		return nil, fmt.Errorf("workspace manager: %w", err)
	}
	// One process-lifetime boot ID and shared workspace claim repository.
	bootID := uuid.NewString()
	leases := adaptsqlite.NewResourceLeaseRepo(db, bootID)
	homeRepo := adaptsqlite.NewWorkspaceHomeRepo(db)
	sessionInventory, err := runner.NewInteractiveSessionInventory(instanceRepo, adaptsqlite.NewGroupRunRepo(db))
	if err != nil {
		return nil, fmt.Errorf("interactive session inventory: %w", err)
	}
	// The executing machine's process port; only the built-in runner calls it.
	localProcess := process.NewSupervisedLocalProcessRunner(builtinSup)
	runnerPool, err := newRunnerStack(runnerStackDeps{
		Config: cfg.Runners, DB: db, Bus: bus, Registry: runnerRegistry, RefreshInventory: sourceSync.Sync,
		Factory: providerFactory, Streams: interactiveStreamSel, DefaultAgentBackend: cfg.LLM.DefaultAgentBackend,
		BuiltinFactory: builtinFactory, BuiltinStreams: builtinStreamSel, Supervisor: builtinSup,
		Process: localProcess, Homes: homeRepo, Leases: leases,
		Probers: map[runner.CredentialKind]runner.SourceProber{
			runner.CredAnthropicSubscription: runnerprobe.NewClaudeAuthProber(cfg.LLM.BinaryPath, claudeRunner),
			runner.CredOpenAISubscription:    runnerprobe.NewCodexProber(codexCfg),
			runner.CredLunaRoute:             runnerprobe.NewLunaRouteProber(connStore, probeFactory),
		},
		Confirmer:  runnerprobe.NewDispatchConfirmer(profileRepo, filepath.Join(wsRoot, ".runner-confirm"), 2*time.Minute),
		Alerts:     adaptnotif.NewSourceAlerts(notifRepo, externalNotifier, logger, time.Now),
		Audit:      auditSvc,
		Instances:  instanceRepo,
		ServerRoot: wsMgr.BasePath(), Logger: logger,
		RequiredModels: profileClaudeModels{profiles: profileRepo, resolver: profileResolver},
		Clock:          time.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("building runner pool: %w", err)
	}

	// 9. Create all node executors and build the executor map. Model and
	// process steps run through the runner pool's routed ports.
	modelConsumers := newRunModelConsumers(fileReader, runnerPool.chats, runnerPool.agents, pinningResolver, cfg.LLM.DefaultAgentBackend)
	scriptExec := process.NewScriptExecutor(fileReader, cfg.Script.EnvPassthrough, runnerPool.process)
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     modelConsumers.llm,
		workflow.NodeTypeAgentCall:   modelConsumers.agent,
		workflow.NodeTypeScript:      scriptExec,
		workflow.NodeTypeToolCall:    process.NewToolCallExecutor(cfg.Script.EnvPassthrough, runnerPool.process),
		workflow.NodeTypeConditional: engine.NewConditionalExecutor(),
		workflow.NodeTypeTransform:   engine.NewTransformExecutor(),
	}

	// 10. Create the queue repository.
	queueRepo := adaptsqlite.NewQueueRepo(db)

	// 11. Create the workflow engine (queue manager wired below).
	eng := engine.NewEngine(
		workflowRepo,
		instanceRepo,
		nodeExecRepo,
		snapshotRepo,
		userRepo,
		executors,
		bus,
		nil, // queueManager set below after creation
		queueRepo,
		cfg.LLM.DefaultAgentBackend,
	)

	invocationStore := adaptsqlite.NewInvocationRepo(db)
	eng.SetInvocationStore(invocationStore)

	// 11a. Wire the per-instance workspace manager.
	eng.SetWorkspaceManager(wsMgr)

	// Build and feed workspace mobility before any workflow can execute.
	mobility, err := buildWorkspaceMobility(workspaceMobilityInputs{
		DB: db, Runners: cfg.Runners, Homes: homeRepo,
		Hub: runnerPool.remote.Hub, Keys: runnerPool.remote.Keys,
		Registry: runnerPool.registry, Ledger: runnerPool.ledger, Events: runnerPool.recorder,
		Status: runnerPool.projector, Notifier: runnerPool.notifier,
		Workspaces: wsMgr, Capabilities: capabilityRules, Clock: time.Now,
		Supervisor: builtinSup, Sessions: sessionInventory, Audit: auditSvc,
		BootID: bootID, Leases: leases,
	})
	if err != nil {
		return nil, fmt.Errorf("wiring workspace mobility: %w", err)
	}
	runnerPool.dispatcher.SetWorkspace(mobility.Sync)
	runnerPool.dispatcher.SetAdopter(mobility.Adopter)
	runnerPool.dispatcher.SetAgentToolPolicy(providerFactory.EffectiveAgentTools)
	runnerPool.service.SetDrainController(mobility.Drain)
	scriptExec.SetBundleStore(mobility.Bundles)
	eng.SetStepCommitter(mobility.StepCommitter)
	eng.SetStepCompletionReleaser(runnerPool.dispatcher)
	eng.SetWorkspaceBaselineInitializer(mobility.Baseline)
	eng.SetRecoveryNoticeSource(mobility.Notices)
	eng.SetWorkspaceHistorySource(mobility.History)
	eng.SetWorkspaceRemovalHook(mobility.Removal)

	// 11b. Construct the instance-list enrichment step and wire it to the
	// engine so ListEnriched composes the additive queue-list fields (run
	// name, submitter, friendly step, model, backend, retry count). The
	// queue-position provider is wired in Phase 2 (queue.Manager.QueuePositions).
	instanceEnricher := engine.NewInstanceEnricher(instanceRepo, nodeExecRepo, userRepo, engine.NoopQueuePositionProvider{}, cfg.LLM.DefaultAgentBackend)
	eng.SetInstanceEnricher(instanceEnricher)

	// 12. Create the queue manager and wire it to the engine.
	queueMgr := queue.NewManager(queueRepo, instanceRepo, cfg, eng)
	eng.SetQueueManager(queueMgr)

	// 12-pos. Wire the queue manager as the enricher's queue-position provider
	// so queued rows carry their priority-aware position (Phase 2). The manager
	// is the single ordering authority; the enricher only stamps the value.
	instanceEnricher.SetQueuePositionProvider(queueMgr)

	// 12-count. Wire the link-addressed read surface as the enricher's child-count
	// source, so a page of the run list resolves every hierarchy expander in ONE
	// batched, post-authorization query.
	instanceEnricher.SetSpawnLinks(instanceRepo)
	// Live, not pinningResolver: each row overlays the pins InstanceRepo.List
	// carries, so a page needs no per-row pin load (G23).
	instanceEnricher.SetProfileResolver(profileResolver)
	instanceEnricher.SetExecutionStatusProvider(runnerPool.projector)

	// 12-sched. Create the timer scheduler, wire it to the queue manager and engine.
	scheduler := queue.NewScheduler(queueMgr)
	queueMgr.SetScheduler(scheduler)
	eng.SetScheduler(scheduler)

	// 12-wh. Create the webhook registry, wire it to the queue manager and engine.
	webhookRegistry := adapthttp.NewWebhookRegistry()
	queueMgr.SetWebhookRegistry(webhookRegistry)
	eng.SetWebhookRegistry(webhookRegistry)

	// 12-wh-spawn. The webhook-timeout path fails an instance from inside
	// domain/queue, which cannot import domain/engine. Wire the engine's spawn
	// wake as the optional child-terminal hook so a parent awaiting a
	// webhook-timed-out child is re-queued immediately instead of waiting for the
	// next startup reconciliation.
	queueMgr.SetChildTerminalHook(eng.OnChildTerminal)
	// Admission and recovery reads only report runs with corrupt model pins;
	// the engine fails them through its complete terminal path.
	queueMgr.SetCorruptRunHandler(eng.FailCorruptRun)

	// 12-exec. Wire timer_delay and webhook_wait executors now that all deps are
	// available. These executors are registered after engine construction because
	// they depend on the scheduler and webhook registry, which in turn depend on
	// the queue manager, creating a circular dependency. Registration goes
	// through the engine's own mutex (RegisterExecutor) rather than by mutating
	// the shared map header, which was an unsynchronized write to a map the run
	// loop reads from other goroutines.
	eng.RegisterExecutor(workflow.NodeTypeTimerDelay, engine.NewTimerDelayExecutor(
		instanceRepo, nodeExecRepo, scheduler, queueMgr,
	))
	eng.RegisterExecutor(workflow.NodeTypeWebhookWait, engine.NewWebhookWaitExecutor(
		instanceRepo, nodeExecRepo, webhookRegistry, scheduler, queueMgr,
	))

	// 12a. Wire the notification repo built with the runner pool.
	eng.SetNotifications(notifRepo)

	// 12a-bis. Create the intervention audit repo and wire it to the engine so
	// admin actions on other users' instances are attributed.
	interventionRepo := adaptsqlite.NewInterventionRepo(db)
	eng.SetInterventions(interventionRepo)
	// 12a-bis-3. Wire the link-addressed read surface so cancelling a run tears
	// down its spawned children recursively, and so the list enricher can resolve
	// a page's child counts in one batched call.
	eng.SetSpawnLinks(instanceRepo)
	// node.started describes the model the node will run on, so it resolves
	// through the pinning decorator inside the run context. The list enricher
	// below keeps the live resolver (no per-row run context; see gaps G23).
	eng.SetProfileResolver(pinningResolver)
	// 12a-bis-2. Wire the Phase-2 audit service so a lead/admin intervention on a
	// run they do not own emits a domain audit event (instance.lead_intervention /
	// admin.override). *audit.Service.Record has exactly the void
	// engine.AuditRecorder signature, so it satisfies the port with no adapter.
	eng.SetAuditRecorder(auditSvc)
	// 12a-1b. Gate the engine trigger read path by workflow visibility. Local
	// mode's fixed identity is admin, for which CanSeePublication returns true,
	// so local-mode openness is preserved.
	eng.SetPublisherLookup(publishedRepo)
	// 12a-1c. Wire the name<->id resolver so triggered instances carry the stable
	// workflow id (and reruns re-resolve the current name after a rename). The
	// workflow identity repo satisfies the engine's narrow WorkflowResolver.
	eng.SetWorkflowResolver(identityRepo)
	// 12a-2. Create the per-user dismissal repo and wire it to the engine so
	// Retry resurfaces dismissed items on their new outcome.
	dismissalRepo := adaptsqlite.NewDismissalRepo(db)
	eng.SetDismissals(dismissalRepo)
	eng.SetPinCacheRelease(pinLookup.Release)

	// Shared authorized layout reads/retries use manifest and snapshot policies.
	ls.attachQuery(workflowSvc, eng)

	systemSettings := settingsdomain.NewService(adaptsqlite.NewSettingsRepo(db))
	identityPolicy := modelidentity.NewPolicyService(adaptsqlite.NewModelIdentityPolicyRepo(db))
	executionGate := provider.NewExecutionGate()
	identityPolicy.SetMutationGate(executionGate)
	// Connection and profile admin writes take the same exclusive lease, so an
	// edit cannot interleave with an advancement or a run-snapshot capture.
	connSvc.SetMutationGate(executionGate)
	workflowSvc.SetMutationGate(executionGate)
	// Automatic model advancement (B5): the SQLite store commits the CAS and the
	// journal in one transaction, the cache confirms publication, and the shared
	// gate rechecks path evidence first. The automatic path deliberately bypasses
	// the loggingReloader: it must observe a reload failure, not log and continue.
	catalogStore := adaptsqlite.NewModelCatalogRepo(db)
	profileSvc.SetAdvancementDependencies(profileRepo, profileCache, executionGate)
	profileSvc.SetPublicationJournal(catalogStore)
	// A trigger snapshots its model pins under the gate's shared lease, so an
	// admin edit cannot land halfway through one run's alias capture.
	eng.SetModelPinGate(executionGate)
	// 12c. Wire the external notifier built with the runner pool.
	eng.SetExternalNotifier(externalNotifier)

	// 12c-sched. Recurring schedules (org-readiness Phase 3): the persistence
	// repo, the in-process evaluator (fires due schedules as their creator via
	// the engine), and the management service (shared by REST + MCP). The
	// evaluator's collision check is the concrete instance repo's HasActiveRun;
	// the can-trigger gate reuses the engine's publication/visibility predicate.
	// ONE identity resolver serves the schedule evaluator, the schedule run-now
	// runner, AND the spawn executor, so a scheduled fire and a spawned child
	// can never resolve the same user differently.
	identityResolver := userIdentityResolver{users: userRepo, groups: groupCfg}

	// 12c-spawn. Wire the spawn_workflow executor.
	//
	// It is registered AFTER engine construction, exactly as timer_delay and
	// webhook_wait are, because it depends on the engine itself (*Engine
	// satisfies engine.ChildSpawner) — through RegisterExecutor, so the write is
	// synchronized with the run loop's lookup and rejected once the set is
	// frozen.
	//
	// The preparer's call is tool-SUPPRESSED: it routes through the same
	// providerFactory chat arm llm_call uses, and the Phase-1 kind guard fails
	// closed if the node's model_profile resolves to an agent binding.
	eng.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, engine.NewSpawnWorkflowExecutor(
		instanceRepo,
		nodeExecRepo,
		instanceRepo, // *adaptsqlite.InstanceRepo also satisfies instance.SpawnWaitRepository
		modelConsumers.childInputs,
		eng,
		identityResolver,
		fileReader,
		queueMgr,
	))

	// Parallel groups share the production repositories, queue, workspace and
	// owner identity resolver. Build before FreezeExecutors: the engine is the
	// task starter, child-contract reader and authorized canceller.
	groupRunRepo := adaptsqlite.NewGroupRunRepo(db)
	groupWS, err := filesystem.NewGroupWorkspace(cfg.Workspace.Path)
	if err != nil {
		return nil, fmt.Errorf("group workspace: %w", err)
	}
	// File groups use Git snapshots and staging; results-only groups keep the
	// filesystem workspace so they never require a Git process.
	workingArea, err := gitarea.NewWorkingArea(cfg.Workspace.Path)
	if err != nil {
		return nil, fmt.Errorf("group working area: %w", err)
	}
	groupCoord := engine.NewGroupCoordinator(groupRunRepo, instanceRepo, queueRepo, eng, identityResolver, bus, queueMgr)
	groupLauncher := engine.NewGroupLauncher(groupRunRepo, eng, eng, mobility.Workspaces, groupWS, identityResolver, eng, bus)
	groupLauncher.SetWorkspaceResource(mobility.Resource)
	groupLauncher.SetFileWorkingArea(workingArea)
	groupLauncher.SetLaunchByteBudget(cfg.Workspace.EffectiveGroupLaunchMaxBytes(), filesystem.GroupAreaMeter{})
	groupExecutor := engine.NewParallelGroupExecutor(instanceRepo, nodeExecRepo, groupRunRepo, groupLauncher, groupCoord, instanceRepo, queueMgr)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup, groupExecutor)
	// Finalize immutable task refs before terminal persistence/workspace cleanup.
	groupFinalizer := engine.NewGroupFileFinalizer(groupRunRepo, workingArea, wsMgr)
	groupFinalizer.SetWorkspaceResource(mobility.Resource)
	eng.SetGroupTaskFinalizer(groupFinalizer)
	eng.SetGroupTerminalHandler(groupCoord)

	scheduleRepo := adaptsqlite.NewScheduleRepo(db)
	scheduleAlerter := schedule.NewScheduleAlerter(notifRepo, externalNotifier, logger)
	scheduleEval := schedule.NewEvaluator(
		scheduleRepo,
		eng,
		instanceRepo, // *adaptsqlite.InstanceRepo satisfies ActiveRunChecker
		identityResolver,
		scheduleAlerter,
		schedule.WithLogger(logger),
		// Resolve the schedule's stable WorkflowID to the workflow's CURRENT name at
		// fire time so a rename cannot break a running schedule.
		schedule.WithResolver(identityRepo),
	)
	// Build the shared run-now fire core from the SAME collaborators the
	// evaluator uses (engine Trigger, instance-repo ActiveRunChecker, identity
	// resolver), and wire it into the management service so REST + MCP expose
	// run-now. The Runner never advances LastFiredAt (run-now is out of cadence).
	scheduleRunner := schedule.NewRunner(
		eng,
		identityResolver,
		instanceRepo,
	)
	scheduleSvc := schedule.NewService(
		scheduleRepo,
		scheduleCanTrigger{publishers: publishedRepo},
		schedule.WithFirer(scheduleRunner),
		// Stamp the schedule's stable WorkflowID (name->id) at create time so a
		// later rename cannot break it; the evaluator fires by id->current name.
		schedule.WithResolverID(identityRepo),
	)

	// The probes launch from this directory; the inventory binds what Claude
	// reads from it, so both must name the same path.
	probeDir := filepath.Join(wsRoot, ".model-probe")
	// Inventory and execution share the actual factory and stream selector,
	// including the configured deployment floor and interactive opt-in map.
	pathInventory, err := providersadapter.NewExecutionPathInventory(providersadapter.InventoryDeps{
		Profiles:    profileSvc,
		Connections: connStore,
		Workflows:   workflowRepo,
		ActiveRuns:  instanceRepo,
		Policy:      identityPolicy,
		Fingerprint: modelFingerprint,
		Factory:     providerFactory,
		Interactive: interactiveStreamSel,
		Harness: providersadapter.NewHarnessFileFingerprinter(providersadapter.HarnessFilesConfig{
			Binaries:   map[string]string{workflow.BackendClaudeCode: cfg.LLM.BinaryPath},
			ProjectDir: probeDir,
		}),
		DefaultAgentBackend: cfg.LLM.DefaultAgentBackend,
	})
	if err != nil {
		return nil, fmt.Errorf("building execution path inventory: %w", err)
	}
	executionGate.SetCurrentRevision(pathInventory.Revision)
	refreshDeps := modelRefreshDeps{
		Settings: systemSettings, Policy: identityPolicy, Profiles: profileSvc, Inventory: pathInventory,
		Chats: providerFactory, Agents: providerFactory, Streams: interactiveStreamSel, Store: catalogStore, Gate: executionGate, Publisher: profileCache, Logger: logger,
		ProbeDir: probeDir, DB: db,
		ProbeTimeout:   time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		FetchTransport: modelDeps.FetchTransport, Clock: modelDeps.Now, NewTicker: modelDeps.NewTicker,
	}
	if modelDeps.AnnouncementTransport != nil {
		// Replace HTTP I/O only; the URL guard keeps wrapping the transport.
		refreshDeps.AnnouncementClient = outbound.NewGuardedClientWithTransport(announcementTimeout, 0, modelDeps.AnnouncementTransport)
	}
	refresh, err := newModelRefresh(refreshDeps)
	if err != nil {
		return nil, fmt.Errorf("building model refresh: %w", err)
	}
	defer func() {
		if !assembled {
			refresh.stop()
		}
	}()
	// Following-profile create/edit prove their model with the refresh's own
	// candidate source; without it every following write fails closed.
	profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
	// Start seeds through this installed candidate source: cold prospective
	// proof commits before the validated seed batch writes and reloads the cache.
	// Capture one profile generation and validate it on actual deployment/node
	// routes under the engine's shared lease, before persistence or enqueue.
	eng.SetModelSnapshotter(providersadapter.NewModelSnapshotter(profileCache, connStore, modelidentity.NewVersionResolver(), pathInventory, capabilityRules))

	// Register one interactive executor instance under BOTH interactive node
	// types. Notification creation is handled by the engine when it processes the
	// IsWait result.
	// These consumers always execute inside a run. Lost context must refuse a
	// turn rather than silently resolve a live model after a profile edit.
	interactiveResolver := pinningResolver.ForRun()
	interactiveExec := engine.NewInteractiveExecutor(
		instanceRepo, nodeExecRepo, runnerPool.streams, interactiveResolver, fileReader, bus, queueMgr,
	)
	eng.RegisterExecutor(workflow.NodeTypeInteractiveLLM, interactiveExec)
	eng.RegisterExecutor(workflow.NodeTypeInteractiveAgent, interactiveExec)

	// 12d. Create the conversation handler for interactive sessions.
	conversationHandler := engine.NewConversationHandler(
		instanceRepo, nodeExecRepo, runnerPool.streams, interactiveResolver, bus, queueMgr,
	)
	invocationSetter, ok := conversationHandler.(interface{ SetInvocationStore(engine.InvocationStore) })
	if !ok {
		return nil, errors.New("conversation handler does not accept the invocation store")
	}
	invocationSetter.SetInvocationStore(invocationStore)
	// Wire the intervention repo so admin participation in another user's
	// interactive session is attributed. The setter lives on the concrete type.
	if s, ok := conversationHandler.(interface {
		SetInterventions(instance.InterventionRepository)
	}); ok {
		s.SetInterventions(interventionRepo)
	}
	// Release a run's cached model pins when its interactive session completes,
	// through the same function the engine uses for terminal runs.
	pinReleaser, ok := conversationHandler.(interface{ SetPinCacheRelease(func(uuid.UUID)) })
	if !ok {
		return nil, fmt.Errorf("conversation handler does not accept a pin cache release")
	}
	pinReleaser.SetPinCacheRelease(pinLookup.Release)
	// Wire the Phase-2 audit service so a lead/admin steering an interactive
	// session they do not own emits a domain audit event
	// (instance.lead_intervention / admin.override) — the same accountability
	// trail the engine mutation paths produce. Without this, the highest-trust
	// surface in the product left ZERO audit trail (PR #21 Must Fix).
	if s, ok := conversationHandler.(interface {
		SetAuditRecorder(engine.AuditRecorder)
	}); ok {
		s.SetAuditRecorder(auditSvc)
	}

	// 12d-group-files. The bridge uses the same run-scoped pinning resolver,
	// profile selector, file reader, notifier and node-addressed queue as
	// interactive sessions, so its autonomous turn and the human continuation
	// (conversationHandler) both run the parent's trigger-time resolver model.
	// End/abandon actions reach it through the shared HTTP/MCP/WS conversation
	// handler.
	groupResolution := engine.NewGroupResolutionBridge(
		groupRunRepo, instanceRepo, nodeExecRepo, interactiveResolver,
		runnerPool.streams, fileReader, eng, bus, queueRepo, queueMgr,
		time.Duration(cfg.LLM.TimeoutSeconds)*time.Second,
	)
	groupResolution.SetInvocationStore(invocationStore)
	groupSessions, ok := conversationHandler.(interface {
		SetGroupResolution(instance.GroupRunRepository, engine.GroupResolutionCompleter)
	})
	if !ok {
		return nil, errors.New("the conversation handler cannot host parallel-group conflict-resolution sessions")
	}
	groupSessions.SetGroupResolution(groupRunRepo, groupResolution)
	gatherer := engine.NewGroupGatherer(groupRunRepo, workingArea, groupResolution, bus)
	gatherer.SetWorkspaceResource(mobility.Resource)
	groupExecutor.SetGatherer(gatherer)
	groupPruner := engine.NewGroupResourcePruner(
		groupRunRepo, instanceRepo, instanceRepo, mobility.Workspaces, workingArea, workingArea,
	)
	groupPruner.SetResultsResources(groupWS, groupRunRepo)
	// Executor and recovery share the single-writer gather lock and bridge.
	gatherReconciler := engine.NewGroupGatherReconciler(
		groupRunRepo, instanceRepo, nodeExecRepo, gatherer,
		groupResolution, groupResolution, queueRepo, queueMgr,
		groupExecutor, bus, groupPruner,
	)
	eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)

	// 12e. SEAL the executor set.
	//
	// Every executor is registered by this point, and from here on goroutines
	// that can EXECUTE a node start: the timer scheduler, crash recovery, the
	// queue evaluator, the schedule evaluator. Registration after this panics
	// rather than racing the run loop's lookup.
	eng.FreezeExecutors()

	// 13. Set up OpenTelemetry tracing before crash recovery so that any spans
	// emitted during recovery are captured by the real provider.
	tracingShutdown, err := observability.SetupTracing(cfg.Tracing)
	if err != nil {
		return nil, fmt.Errorf("setting up tracing: %w", err)
	}

	// Shared backend-health component: ONE TTL-cached Checker, injected into both
	// the HTTP SystemHandler and the MCP server below. A nil runner falls back to
	// the production os/exec probe (codex.Readiness); a 0 TTL uses the package
	// default. Constructing it here (not per request) is what gives the cache its
	// request-spanning lifetime.
	backendHealthChecker := backendhealth.NewChecker(nil, 0, nil)

	graphState := engine.NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)
	// Share a bounded ancestry cache; authorize every forwarded source anew.
	subtreeEvents := engine.NewSubtreeEventBridge(bus, eng, instanceRepo)
	subtreeEvents.SetGroupRuns(groupRunRepo)

	assembled = true
	ds := &domainStack{
		layout:                ls,
		cfg:                   cfg,
		modelStateCrypto:      modelStateCrypto,
		modelFingerprint:      modelFingerprint,
		executionGate:         executionGate,
		pathInventory:         pathInventory,
		modelRefresh:          refresh,
		refreshDeps:           refreshDeps,
		SystemSettings:        systemSettings,
		IdentityPolicy:        identityPolicy,
		catalogStore:          catalogStore,
		settings:              settings,
		logger:                logger,
		logCloser:             logCloser,
		db:                    db,
		eng:                   eng,
		workflowRepo:          workflowRepo,
		workflowSvc:           workflowSvc,
		auditSvc:              auditSvc,
		publishedRepo:         publishedRepo,
		instanceRepo:          instanceRepo,
		nodeExecRepo:          nodeExecRepo,
		snapshotRepo:          snapshotRepo,
		fileReader:            fileReader,
		bus:                   bus,
		queueMgr:              queueMgr,
		queueRepo:             queueRepo,
		scheduler:             scheduler,
		groupRunRepo:          groupRunRepo,
		groupCoord:            groupCoord,
		gatherReconciler:      gatherReconciler,
		recoveryWG:            &sync.WaitGroup{},
		scheduleEval:          scheduleEval,
		scheduleSvc:           scheduleSvc,
		webhookRegistry:       webhookRegistry,
		notifRepo:             notifRepo,
		dismissalRepo:         dismissalRepo,
		conversationHandler:   conversationHandler,
		pinLookup:             pinLookup,
		profileChecker:        profileChecker,
		profileResolver:       profileResolver,
		connChecker:           connChecker,
		connStore:             connStore,
		profileSvc:            profileSvc,
		connSvc:               connSvc,
		modelDiscovery:        modelDiscovery,
		capabilityRules:       capabilityRules,
		tracingShutdown:       tracingShutdown,
		sessionRepo:           sessionRepo,
		tokenRepo:             tokenRepo,
		tokenSvc:              tokenSvc,
		deviceRepo:            deviceRepo,
		deviceSvc:             deviceSvc,
		provisioner:           provisioner,
		userRepo:              userRepo,
		userSvc:               userSvc,
		dashboardSvc:          dashboardSvc,
		authMW:                authMW,
		authHandler:           authHandler,
		backendHealth:         backendHealthChecker,
		graphState:            graphState,
		awaitInitialDiscovery: defaultAwaitInitialDiscovery,
		subtreeEvents:         subtreeEvents,
		invocations:           invocationStore,
		groupResolution:       groupResolution,
		runners:               runnerPool,
		mobility:              mobility,
		remote:                runnerPool.remote,
	}
	skipSeed := func(alias string, cause error) {
		logger.Warn("skipped config profile", "alias", alias, "error", cause)
	}
	// A seed left pending at boot (discovery down, proof not yet confirmed) is
	// retried from the persisted pending journal after each completed tick. The
	// current config candidates go along so an original that can never be
	// prepared (an empty model frozen by an older release) is repaired by a
	// following default here: startup cannot, since it never discovers.
	refresh.seedPending = func(ctx context.Context) {
		seeded, skipped, err := SeedProfiles(ctx, profileRepo, profileSvc, domainProfiles, skipSeed)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("retrying pending config profile seeds", "error", err)
			}
			return
		}
		if seeded > 0 || skipped > 0 {
			logger.Info("seeded pending profiles from config", "seeded", seeded, "skipped", skipped)
		}
	}
	ds.startWork = func(ctx context.Context) error {
		// Startup seeding never discovers: a following seed's prospective
		// discovery would hold readiness behind catalog fetches and probes. It
		// stays pending for the background loop's seedPending retry (serve).
		seeded, skipped, err := SeedProfiles(provider.WithoutSeedDiscovery(ctx), profileRepo, profileSvc, domainProfiles, skipSeed)
		if err != nil {
			return fmt.Errorf("seeding profiles: %w", err)
		}
		if seeded > 0 || skipped > 0 {
			logger.Info("seeded profiles from config", "seeded", seeded, "skipped", skipped)
		}
		seedWorkflows(ctx)
		reportPinPreflight(ctx, profileSvc, notifRepo, externalNotifier, &ds.background, logger)
		return ds.startRuntime(ctx, authMode,
			engine.NewSpawnWaitReconciler(instanceRepo, instanceRepo, queueRepo, queueMgr),
			engine.NewOrphanedChildReconciler(instanceRepo, instanceRepo, identityResolver, eng))
	}
	return ds, nil
}

func runServe(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	configPath, err := configPathFromCmd(cmd)
	if err != nil {
		return err
	}
	ds, err := initDomain(ctx, configPath)
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())
	if err := ds.Start(ctx); err != nil {
		// Shutdown requested while starting (signal or service stop) is a clean
		// exit, the same as one requested while serving.
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	// 15. Create the MCP server (mounted at /mcp on the HTTP router) and
	// the full API router with embedded dashboard.
	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	routerDeps := ds.httpDeps()
	routerDeps.DashboardHandler = dashboard.Handler()
	routerDeps.MCPHandler = mcpSrv.HTTPHandler()
	router := adapthttp.NewRouterFromDeps(routerDeps)

	// 16. Add request logging middleware (wraps the entire router).
	handler := adapthttp.RequestLoggingMiddleware(ds.logger)(router)

	addr := fmt.Sprintf("%s:%d", ds.cfg.Server.Host, ds.cfg.Server.Port)

	// Warn if binding to a non-loopback address in LOCAL mode. Local mode has no
	// authentication (a fixed auto-admin), so exposing it on the network is a
	// security risk. In ORG mode the API IS authenticated, so binding non-loopback
	// is the intended deployment and the warning is suppressed.
	if ds.cfg.Auth.Mode == config.AuthModeLocal &&
		ds.cfg.Server.Host != "127.0.0.1" && ds.cfg.Server.Host != "localhost" && ds.cfg.Server.Host != "::1" {
		ds.logger.Warn("local mode is binding to a non-loopback address -- local mode has no authentication and should not be exposed on the network",
			"host", ds.cfg.Server.Host)
	}

	srv := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 10 * time.Second,
		// WriteTimeout is intentionally 0 (no timeout) because WebSocket
		// connections remain open for streaming and a fixed timeout would
		// kill long-lived connections. Per-request deadlines are handled at
		// the handler level instead.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	tlsConf, challengeSrv, err := buildTLSConfig(ds.cfg.Server.TLS, ds.logger)
	if err != nil {
		return fmt.Errorf("configuring TLS: %w", err)
	}
	srv.TLSConfig = tlsConf

	// 17. Graceful shutdown on SIGINT/SIGTERM.
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdownCh)
	shutdownCtx, shutdownCancel := context.WithCancel(ctx)

	// Optionally start MCP stdio server alongside the HTTP server.
	// HTTP MCP is always available at /mcp; --mcp-stdio adds the stdio transport.
	mcpCtx, mcpCancel := context.WithCancel(ctx)
	defer mcpCancel()

	mcpStdio, _ := cmd.Flags().GetBool("mcp-stdio")
	if mcpStdio {
		mcpDone := make(chan struct{})
		defer func() { mcpCancel(); <-mcpDone }()
		go func() {
			defer close(mcpDone)
			ds.logger.Info("MCP stdio server starting (alongside HTTP)")
			stdioCtx, err := ds.stdioContext(mcpCtx)
			if err != nil {
				ds.logger.Error("MCP stdio identity unavailable", "error", err)
				return
			}
			if err := mcpSrv.ServeStdio(stdioCtx); err != nil && mcpCtx.Err() == nil {
				ds.logger.Error("MCP stdio server error", "error", err)
			}
		}()
	}

	// Translate the OS signal into a stop channel for the serve loop, and cancel
	// the MCP stdio context when shutdown begins so it stops accepting requests.
	stop := make(chan struct{})
	shutdownDone := make(chan struct{})
	defer func() { shutdownCancel(); <-shutdownDone }()
	go func() {
		defer close(shutdownDone)
		select {
		case sig := <-shutdownCh:
			ds.logger.Info("shutdown signal received", "signal", sig)
		case <-shutdownCtx.Done():
		}
		mcpCancel()
		close(stop)
	}()

	// serveFn owns the symmetric two-listener lifecycle: the main server and the
	// optional ACME challenge server are started together, both push serve errors
	// to a shared channel (so a non-root :80 bind failure aborts startup), and
	// both are shut down together. It is a closure so the same body runs whether
	// the process is supervised by the Windows SCM or launched interactively. The
	// passed-in context is the serve context; on Windows under the SCM it is
	// cancelled when a Stop/Shutdown control request arrives.
	serveFn := func(serveCtx context.Context) error {
		return serveHTTP(serveCtx, ds.logger, srv, challengeSrv, stop)
	}

	// On Windows, when started by the Service Control Manager, dispatch through
	// svc.Run so SCM Stop/Shutdown is translated into context cancellation. On
	// every other platform (and on an interactive Windows run) this is a no-op
	// returning (false, nil) and we fall through to the normal serve path.
	if handled, err := runUnderServiceManager(ctx, serveFn); handled {
		return err
	}

	return serveFn(ctx)
}

func runMCP(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	configPath, err := configPathFromCmd(cmd)
	if err != nil {
		return err
	}
	ds, err := startMCPStack(ctx, configPath, ModelDependencies{})
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())

	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	stdioCtx, err := ds.stdioContext(ctx)
	if err != nil {
		return err
	}
	ds.logger.Info("MCP stdio server starting")
	return mcpSrv.ServeStdio(stdioCtx)
}

// startMCPStack is the stdio MCP initialization: the stack is built, the
// entrypoint's discovery policy applied, then started. MCP clients enforce
// short handshake timeouts and each launches its own process, so no catalog
// fetch or probe may precede ServeStdio and only serve runs the automatic
// refresh loop. On a Start failure the stack is closed.
func startMCPStack(ctx context.Context, configPath string, deps ModelDependencies) (*domainStack, error) {
	ds, err := initDomainWithModelDependencies(ctx, configPath, deps)
	if err != nil {
		return nil, err
	}
	ds.disableAutomaticDiscovery()
	if err := ds.Start(ctx); err != nil {
		ds.Close(context.Background())
		return nil, err
	}
	return ds, nil
}

// scheduleCanTrigger adapts the published-workflow lookup into the
// schedule.CanTrigger port. "Can schedule" == "can trigger": it reuses the
// EXACT publication/visibility predicate the engine's TriggerWithDepartment
// applies (cmd parity with engine.go ll. 246-257). An unpublished or
// private/owned workflow is open (true); a published one is gated by
// workflow.CanSeePublication.
type scheduleCanTrigger struct {
	publishers *adaptsqlite.PublishedWorkflowRepo
}

func (c scheduleCanTrigger) CanTrigger(ctx context.Context, id identity.Identity, workflowName string) (bool, error) {
	if c.publishers == nil {
		return true, nil
	}
	pub, found, err := c.publishers.PublicationOf(ctx, workflowName)
	if err != nil {
		return false, err
	}
	if !found {
		// Unpublished shared folder / private workflow: open, exactly as the
		// engine treats a not-found publication.
		return true, nil
	}
	return workflow.CanSeePublication(id, pub), nil
}

// userIdentityResolver builds a user's full runtime identity from a bare user
// id, for injection into a background context.
//
// It constructs the Identity via the SAME shared domain builder as
// AuthMiddleware.enrich (fed by the same UserRepository + GroupConfig ports),
// so a scheduled fire, a spawned child run, and the same user's interactive
// request all resolve to identical identities — LedDepartments included.
//
// The name is deliberately NOT schedule-specific: ONE value of this type is
// constructed at the composition root and passed to the schedule evaluator, the
// schedule run-now runner, AND the spawn executor. It satisfies both
// schedule.IdentityResolver and engine.IdentityResolver, which are structurally
// identical twins redeclared across a domain edge.
type userIdentityResolver struct {
	users  instance.UserRepository
	groups identity.GroupConfig
}

func (r userIdentityResolver) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
	u, err := r.users.GetByID(ctx, userID)
	if err != nil {
		return identity.Identity{}, err
	}
	depts, err := r.users.ListDepartments(ctx, userID)
	if err != nil {
		return identity.Identity{}, err
	}
	subject := ""
	if u.Subject != nil {
		subject = *u.Subject
	}
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	return identity.BuildIdentity(identity.UserIdentityInput{
		UserID:            u.ID,
		Username:          u.Username,
		Role:              u.Role,
		Subject:           subject,
		Email:             email,
		StoredDepartments: depts,
	}, r.groups), nil
}
