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
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/observability"
	adaptoidc "github.com/arbitron/arbitron/adapter/oidc"
	"github.com/arbitron/arbitron/adapter/process"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/config"
	domaindashboard "github.com/arbitron/arbitron/domain/dashboard"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/schedule"
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

// domainStack holds all shared domain dependencies initialised by initDomain.
// Both the HTTP serve command and the standalone MCP command reuse this struct
// so that initialisation logic is not duplicated.
type domainStack struct {
	cfg                 *config.Config
	settings            *config.SettingsStore
	logger              *slog.Logger
	logCloser           io.Closer
	db                  *sql.DB
	eng                 *engine.Engine
	workflowRepo        workflow.WorkflowRepository
	workflowSvc         *workflow.WorkflowService
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
	scheduleEvalCancel  context.CancelFunc
	scheduleSvc         *schedule.Service
	webhookRegistry     *adapthttp.WebhookRegistry
	notifRepo           notification.Repository
	dismissalRepo       instance.DismissalRepository
	conversationHandler engine.ConversationHandler
	profileChecker      workflow.ProfileChecker
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
	tracingShutdown func(ctx context.Context) error
	// Auth (Phase 6). sessionRepo is always built; authMW is always built;
	// authHandler and sweepCancel are nil in local mode.
	sessionRepo  identity.SessionRepository
	tokenRepo    identity.TokenRepository
	tokenSvc     *identity.TokenService
	deviceRepo   identity.DeviceCodeRepository
	deviceSvc    *identity.DeviceCodeService
	provisioner  *instance.Provisioner
	userRepo     instance.UserRepository
	userSvc      *instance.UserService
	dashboardSvc *domaindashboard.Service
	authMW       *adapthttp.AuthMiddleware
	authHandler  *adapthttp.AuthHandler
	sweepCancel  context.CancelFunc
	// backendHealth is the shared, TTL-cached backend-health component (codex
	// install/login readiness). Constructed ONCE here and injected into BOTH the
	// HTTP SystemHandler and the MCP server so the probe forks at most once per
	// TTL across both surfaces (and the unauthenticated local GET /system/config
	// route cannot fork a codex probe pair per request).
	backendHealth *backendhealth.Checker
}

// Close tears down the domain stack in the correct order: engine drains first,
// then the scheduler stops, then tracing flushes, then the database closes,
// and finally the log sink is closed.
func (ds *domainStack) Close(ctx context.Context) {
	// Stop the schedule evaluator FIRST (org-readiness Phase 3): no new scheduled
	// triggers must be created once shutdown begins, so it stops before the engine
	// drains in-flight instances.
	if ds.scheduleEvalCancel != nil {
		ds.scheduleEvalCancel()
	}
	if ds.scheduleEval != nil {
		ds.scheduleEval.Stop()
	}
	ds.eng.Shutdown()
	if ds.schedulerCancel != nil {
		ds.schedulerCancel()
	}
	if ds.scheduler != nil {
		ds.scheduler.Stop()
	}
	if ds.tracingShutdown != nil {
		if err := ds.tracingShutdown(ctx); err != nil {
			ds.logger.Error("tracing shutdown error", "error", err)
		}
	}
	// Stop the org-mode session sweep before closing the DB it writes to.
	if ds.sweepCancel != nil {
		ds.sweepCancel()
	}
	// Stop the bearer last-used flusher's owned goroutine, draining any pending
	// touches synchronously before the DB closes.
	if ds.authMW != nil {
		ds.authMW.Stop()
	}
	if ds.db != nil {
		ds.db.Close()
	}
	if ds.logCloser != nil {
		ds.logCloser.Close()
	}
}

// mcpDeps builds the MCP adapter Deps from the shared domain stack.
func (ds *domainStack) mcpDeps() *adaptmcp.Deps {
	return &adaptmcp.Deps{
		Engine:              ds.eng,
		Workflows:           ds.workflowRepo,
		WorkflowSvc:         ds.workflowSvc,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		Settings:            ds.settings,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		Notifications:       ds.notifRepo,
		ConversationHandler: ds.conversationHandler,
		ProfileChecker:      ds.profileChecker,
		ConnChecker:         ds.connChecker,
		Connections:         ds.connStore,
		ProfileAdmin:        ds.profileSvc,
		ConnectionAdmin:     ds.connSvc,
		Capabilities:        ds.capabilityRules,
		Tokens:              ds.tokenSvc,
		Users:               ds.userSvc,
		ScheduleService:     ds.scheduleSvc,
		Logger:              ds.logger,
		BackendHealth:       ds.backendHealth,
	}
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

// initDomain performs all shared initialisation: configuration, logging,
// database, adapters, engine, queue manager, notifications, tracing, and
// crash recovery. The returned domainStack must be closed by the caller.
func initDomain(_ context.Context, configPath string) (*domainStack, error) {
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

	// 3. Open SQLite database.
	logger.Info("opening database", "path", cfg.Database.Path)
	db, err := adaptsqlite.Open(cfg.Database.Path)
	if err != nil {
		logCloser.Close()
		return nil, fmt.Errorf("opening database: %w", err)
	}
	logger.Info("database connection established")

	// 4. Run migrations.
	logger.Info("running database migrations")
	if err := adaptsqlite.RunMigrations(db); err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	logger.Info("database migrations complete")

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
	if err := identity.ReconcileMappings(context.Background(), groupCfg, deptRenamer, logger); err != nil {
		db.Close()
		logCloser.Close()
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
		oidcProvider, oerr := adaptoidc.New(context.Background(), adaptoidc.Config{
			IssuerURL:    cfg.Auth.OIDC.IssuerURL,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
			GroupClaim:   cfg.Auth.RBAC.GroupClaim,
		})
		if oerr != nil {
			db.Close()
			logCloser.Close()
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
		edgeVerifier, eerr := adaptedge.New(context.Background(), adaptedge.Config{
			TeamDomain: cfg.Auth.Edge.TeamDomain,
			Audiences:  cfg.Auth.Edge.Audiences,
			GroupClaim: cfg.Auth.RBAC.GroupClaim,
		})
		if eerr != nil {
			db.Close()
			logCloser.Close()
			return nil, fmt.Errorf("initialising edge verifier: %w", eerr)
		}
		authMW = adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, edgeVerifier, provisioner, userRepo, groupCfg)
	} else {
		authMW = adapthttp.NewAuthMiddleware(authMode, cfg.Auth.Session.CookieName, sessionRepo, tokenRepo, userRepo, groupCfg)
	}

	// 6. Create filesystem-based repos.
	workflowRepo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)
	fileReader := filesystem.NewFileReader(cfg.Workspace.Path)

	// One-time, marker-guarded migration of any pre-existing flat workflow store
	// into the default user's private workspace (users/<defaultUser>/). Runs
	// after the DB migrations seed the default user and before serving begins.
	flatMigrateUser, err := userRepo.GetDefaultUser(context.Background())
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("resolving default user for flat-store migration: %w", err)
	}
	if err := migrateFlatStore(cfg.Workspace.Path, flatMigrateUser); err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("flat-store migration: %w", err)
	}
	logger.Info("flat-store migration complete", "defaultUser", flatMigrateUser)
	// PublishedWorkflowRepo is the authority for the shared workspace's
	// unique-name / publisher-only-overwrite rules and the PublisherLookup the
	// WorkflowService uses to tag shared list entries with their publisher.
	publishedRepo := adaptsqlite.NewPublishedWorkflowRepo(db)
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
		if err := seedSampleWorkflows(context.Background(), cfg.Samples.SourceDir, cfg.Workspace.Path, workflowRepo, publishedRepo, flatMigrateUser, logger); err != nil {
			logger.Error("sample workflow seeding failed; continuing boot without samples", "err", err)
		} else {
			logger.Info("sample workflow seeding complete")
		}
	case cfg.Samples.Seed:
		// seed=true but no source dir resolved: the stock default. Make the
		// silent no-op observable so an operator expecting samples knows why
		// none appeared and how to fix it (set samples.sourcedir).
		logger.Info("sample seeding enabled but no source directory configured; nothing will be seeded (set samples.sourcedir to the install's samples/ path)")
	}
	// WorkflowService resolves storage scope/authorization from the request
	// identity.
	workflowSvc := workflow.NewWorkflowService(workflowRepo, publishedRepo)
	// The workflow-stats read path is gated by the service's visibility predicate
	// (CanSeePublication / ownership); wire the instance repo behind that boundary
	// so GET /workflows/{name}/stats cannot read across user/department scopes.
	workflowSvc.SetStatsReader(instanceRepo)
	// Best-effort audit emitter for publish/visibility/admin-override events. The
	// engine is wired to this same auditSvc below (Phase 3 task 5) via
	// eng.SetAuditRecorder(auditSvc), once eng exists — a single engine seam.
	auditRepo := adaptsqlite.NewAuditRepo(db)
	auditSvc := audit.NewService(auditRepo)
	auditSvc.SetLogger(logger)
	workflowSvc.SetAudit(auditSvc)

	// 7. Create the chat + agent providers based on config.
	//
	// SECURITY: WorkspaceRoot is the absolute workspace path; both adapters use
	// it to pin every per-request WorkDir under this subtree. The chat adapter
	// is tool-suppressed by construction (no AllowedTools / SkipPermissions); the
	// agent adapter carries the full tool scope.
	wsRoot, err := filepath.Abs(cfg.Workspace.Path)
	if err != nil {
		db.Close()
		logCloser.Close()
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
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("loading capability rules: %w", err)
	}

	chatCfg := claudecli.Config{
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

	// 8. Create in-process event bus.
	bus := eventbus.New()

	// 8a. Bootstrap connection secrecy: load the master key (env/keyring), build
	// the AES-GCM crypto only when a key is present, and construct the connection
	// store. The master key is held in memory only and is never logged or
	// persisted; LoadMasterKey logs the source only.
	masterKey, err := adaptcrypto.LoadMasterKey(logger)
	if err != nil && !errors.Is(err, provider.ErrMasterKeyMissing) {
		// An invalid key (bad base64 / wrong length) is a hard error.
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("loading master key: %w", err)
	}
	haveKey := masterKey != nil

	var connCrypto provider.Crypto
	if haveKey {
		connCrypto, err = adaptcrypto.NewAESGCM(masterKey)
		if err != nil {
			db.Close()
			logCloser.Close()
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
	connCount, err := connStore.Count(context.Background())
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("counting connections: %w", err)
	}
	if err := adaptcrypto.RequireMasterKeyIfConnections(haveKey, connCount); err != nil {
		db.Close()
		logCloser.Close()
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
	// seed the table is non-empty and config profile entries are ignored — the DB
	// wins.
	domainProfiles := configadapter.ProfilesFromConfig(cfg.Profiles)

	profileRepo := adaptsqlite.NewProfileRepo(db)
	seeded, skipped, err := SeedProfiles(context.Background(), profileRepo, capabilityRules, domainProfiles)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("seeding profiles: %w", err)
	}
	if seeded > 0 || skipped > 0 {
		logger.Info("seeded profiles from config", "seeded", seeded, "skipped", skipped)
	}

	// profileCache is bound to the repo at construction so it can be reloaded
	// after a write (the Phase-4 ProfileService recache hook); keep the reference
	// for that future wiring. Load it once before serving.
	profileCache := providersadapter.NewProfileCache(profileRepo)
	if err := profileCache.Load(context.Background()); err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("loading profile cache: %w", err)
	}
	profileResolver := providersadapter.NewDynamicProfileResolver(profileCache, connStore)
	profileChecker := providersadapter.NewResolverChecker(profileResolver)

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
	connSvc := provider.NewConnectionService(connStore, profileRepo)

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

	providerFactory := providersadapter.NewFactory(providersadapter.Deps{
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
		HTTPTimeout:       time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		// Runner nil -> opencode/codex use the shared production os/exec runner.
		OpencodeTimeout: time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		// Codex (subscription-only `codex exec`): binary auto-resolved from PATH
		// (CodexBinaryPath left empty, mirroring opencode); the per-mission timeout
		// reuses the shared LLM timeout floor. No allow-all knob: codex runs under
		// the fixed v1 bypass posture (see adapter/codex args.go).
		CodexTimeout: time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
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
	})

	// 9. Create all node executors and build the executor map.
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     engine.NewLLMCallExecutor(fileReader, providerFactory, profileResolver),
		workflow.NodeTypeAgentCall:   engine.NewAgentCallExecutor(fileReader, providerFactory, profileResolver, cfg.LLM.DefaultAgentBackend),
		workflow.NodeTypeScript:      process.NewScriptExecutor(fileReader, cfg.Script.EnvPassthrough),
		workflow.NodeTypeToolCall:    process.NewToolCallExecutor(cfg.Script.EnvPassthrough),
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
	)

	// 11a. Wire the per-instance workspace manager.
	wsMgr, err := filesystem.NewWorkspaceManager(cfg.Workspace.Path)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("workspace manager: %w", err)
	}
	eng.SetWorkspaceManager(wsMgr)

	// 12. Create the queue manager and wire it to the engine.
	queueMgr := queue.NewManager(queueRepo, instanceRepo, cfg, eng)
	eng.SetQueueManager(queueMgr)

	// 12-sched. Create the timer scheduler, wire it to the queue manager and engine.
	scheduler := queue.NewScheduler(queueMgr)
	queueMgr.SetScheduler(scheduler)
	eng.SetScheduler(scheduler)

	// 12-wh. Create the webhook registry, wire it to the queue manager and engine.
	webhookRegistry := adapthttp.NewWebhookRegistry()
	queueMgr.SetWebhookRegistry(webhookRegistry)
	eng.SetWebhookRegistry(webhookRegistry)

	// 12-exec. Wire timer_delay and webhook_wait executors now that all deps are
	// available. These executors are added after engine construction because they
	// depend on the scheduler and webhook registry, which in turn depend on the
	// queue manager, creating a circular dependency. This works because the
	// engine stores a reference to the executors map (not a copy), so mutations
	// here are visible to the engine at execution time.
	executors[workflow.NodeTypeTimerDelay] = engine.NewTimerDelayExecutor(
		instanceRepo, nodeExecRepo, scheduler, queueMgr,
	)
	executors[workflow.NodeTypeWebhookWait] = engine.NewWebhookWaitExecutor(
		instanceRepo, nodeExecRepo, webhookRegistry, scheduler, queueMgr,
	)

	// 12a. Create notification repo and wire it to the engine.
	notifRepo := adaptsqlite.NewNotificationRepo(db)
	eng.SetNotifications(notifRepo)

	// 12a-bis. Create the intervention audit repo and wire it to the engine so
	// admin actions on other users' instances are attributed.
	interventionRepo := adaptsqlite.NewInterventionRepo(db)
	eng.SetInterventions(interventionRepo)
	// 12a-bis-2. Wire the Phase-2 audit service so a lead/admin intervention on a
	// run they do not own emits a domain audit event (instance.lead_intervention /
	// admin.override). *audit.Service.Record has exactly the void
	// engine.AuditRecorder signature, so it satisfies the port with no adapter.
	eng.SetAuditRecorder(auditSvc)
	// 12a-1b. Gate the engine trigger read path by workflow visibility. Local
	// mode's fixed identity is admin, for which CanSeePublication returns true,
	// so local-mode openness is preserved.
	eng.SetPublisherLookup(publishedRepo)
	// 12a-2. Create the per-user dismissal repo and wire it to the engine so
	// Retry resurfaces dismissed items on their new outcome.
	dismissalRepo := adaptsqlite.NewDismissalRepo(db)
	eng.SetDismissals(dismissalRepo)

	// 12b. Create the synchronized SettingsStore that owns the runtime-mutable
	// Teams settings, shared across the HTTP adapter, the MCP adapter, and the
	// Teams notifier so all three route reads/writes through one mutex.
	settings := config.NewSettingsStore(&cfg.Notifications.Teams)

	// 12c. Create the fan-out external notifier (log + best-effort Teams) and
	// wire it to the engine. The Teams child reads the live webhook config
	// through the store, so an admin toggle via the dashboard / MCP takes effect
	// on the next notification.
	externalNotifier := adaptnotif.NewDefaultExternalNotifier(settings, cfg.Server.PublicBaseURL, logger)
	eng.SetExternalNotifier(externalNotifier)

	// 12c-sched. Recurring schedules (org-readiness Phase 3): the persistence
	// repo, the in-process evaluator (fires due schedules as their creator via
	// the engine), and the management service (shared by REST + MCP). The
	// evaluator's collision check is the concrete instance repo's HasActiveRun;
	// the can-trigger gate reuses the engine's publication/visibility predicate.
	scheduleRepo := adaptsqlite.NewScheduleRepo(db)
	scheduleAlerter := schedule.NewScheduleAlerter(notifRepo, externalNotifier, logger)
	scheduleEval := schedule.NewEvaluator(
		scheduleRepo,
		eng,
		instanceRepo, // *adaptsqlite.InstanceRepo satisfies ActiveRunChecker
		scheduleIdentityResolver{users: userRepo, groups: groupCfg},
		scheduleAlerter,
		schedule.WithLogger(logger),
	)
	// Build the shared run-now fire core from the SAME collaborators the
	// evaluator uses (engine Trigger, instance-repo ActiveRunChecker, identity
	// resolver), and wire it into the management service so REST + MCP expose
	// run-now. The Runner never advances LastFiredAt (run-now is out of cadence).
	scheduleRunner := schedule.NewRunner(
		eng,
		scheduleIdentityResolver{users: userRepo, groups: groupCfg},
		instanceRepo,
	)
	scheduleSvc := schedule.NewService(
		scheduleRepo,
		scheduleCanTrigger{publishers: publishedRepo},
		schedule.WithFirer(scheduleRunner),
	)

	// 12c. Build the profile-aware interactive stream selector: chat profiles
	// route to the tool-suppressed chat stream (via the provider factory), agent
	// profiles route by BACKEND to the matching tool-enabled streaming adapter —
	// claude-code → the claude-cli stream adapter, codex → the codex stream
	// adapter (`codex exec --json`, subscription-only). An empty agent backend is
	// floored to the SAME deployment default the batch AgentCallExecutor uses
	// (cfg.LLM.DefaultAgentBackend); opencode and unknown backends fail closed.
	claudeStreamAgent := claudecli.NewStreamAgentAdapter(agentCfg)
	codexStreamAgent := codex.NewStreamProvider(codex.Config{
		// Codex (subscription-only `codex exec`): binary auto-resolved from PATH,
		// the per-mission timeout reuses the shared LLM timeout floor, and the
		// response cap mirrors the factory's codex arm. No allow-all knob: codex
		// runs under the fixed v1 bypass posture (see adapter/codex args.go).
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		// profile-editing Phase 3: the binding's resolved effort rides argv as
		// `-c model_reasoning_effort=<v>` via these rules on the interactive_agent
		// streaming path too.
		CapabilityRules: capabilityRules,
	})
	interactiveStreamSel := providersadapter.NewInteractiveStreamSelector(
		providerFactory,
		map[string]engine.ChatProvider{
			workflow.BackendClaudeCode: claudeStreamAgent,
			workflow.BackendCodex:      codexStreamAgent,
		},
		cfg.LLM.DefaultAgentBackend,
	)

	// Register one interactive executor instance under BOTH interactive node
	// types. Notification creation is handled by the engine when it processes the
	// IsWait result.
	interactiveExec := engine.NewInteractiveExecutor(
		instanceRepo, nodeExecRepo, interactiveStreamSel, profileResolver, fileReader, bus, queueMgr,
	)
	executors[workflow.NodeTypeInteractiveLLM] = interactiveExec
	executors[workflow.NodeTypeInteractiveAgent] = interactiveExec

	// 12d. Create the conversation handler for interactive sessions.
	conversationHandler := engine.NewConversationHandler(
		instanceRepo, nodeExecRepo, interactiveStreamSel, profileResolver, bus, queueMgr,
	)
	// Wire the intervention repo so admin participation in another user's
	// interactive session is attributed. The setter lives on the concrete type.
	if s, ok := conversationHandler.(interface {
		SetInterventions(instance.InterventionRepository)
	}); ok {
		s.SetInterventions(interventionRepo)
	}
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

	// 13. Set up OpenTelemetry tracing before crash recovery so that any spans
	// emitted during recovery are captured by the real provider.
	tracingShutdown, err := observability.SetupTracing(cfg.Tracing)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("setting up tracing: %w", err)
	}

	// 13a. Start the timer scheduler so recovery can register timers.
	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	scheduler.Start(schedulerCtx)

	// 14. Recover any instances that were running when the process last crashed.
	logger.Info("recovering crashed instances")
	if err := eng.RecoverCrashedInstances(); err != nil {
		schedulerCancel()
		scheduler.Stop()
		if tracingShutdown != nil {
			_ = tracingShutdown(context.Background())
		}
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering crashed instances: %w", err)
	}

	// 14a. Recover pending timers and webhook endpoints from the database.
	logger.Info("recovering pending timers")
	if err := queueMgr.RecoverTimers(context.Background()); err != nil {
		schedulerCancel()
		scheduler.Stop()
		if tracingShutdown != nil {
			_ = tracingShutdown(context.Background())
		}
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering timers: %w", err)
	}
	logger.Info("recovering webhook endpoints")
	if err := queueMgr.RecoverWebhooks(context.Background()); err != nil {
		schedulerCancel()
		scheduler.Stop()
		if tracingShutdown != nil {
			_ = tracingShutdown(context.Background())
		}
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("recovering webhooks: %w", err)
	}

	// 14c. Apply the no-backfill misfire policy ONCE (org-readiness Phase 3),
	// AFTER timer/webhook recovery and BEFORE starting the evaluator loop, then
	// start the loop. Recover advances schedules whose occurrences elapsed during
	// downtime (raising a schedule_missed alert) without backfilling them.
	if err := scheduleEval.Recover(context.Background()); err != nil {
		logger.Error("recovering schedules", "error", err)
	}
	scheduleEvalCtx, scheduleEvalCancel := context.WithCancel(context.Background())
	scheduleEval.Start(scheduleEvalCtx)

	// 14b. Start the org-mode session-expiry sweep (no-op in local mode), plus a
	// device-code expiry sweep (device login is org-mode only).
	var sweepCancel context.CancelFunc
	if authMode == identity.ModeOrg {
		var sweepCtx context.Context
		sweepCtx, sweepCancel = context.WithCancel(context.Background())
		go adaptsqlite.SweepExpiredSessions(sweepCtx, sessionRepo,
			time.Duration(cfg.Auth.Session.TTLSeconds)*time.Second, logger)
		go sweepExpiredDeviceCodes(sweepCtx, deviceSvc, deviceCodeSweepInterval, logger)
	}

	// Shared backend-health component: ONE TTL-cached Checker, injected into both
	// the HTTP SystemHandler and the MCP server below. A nil runner falls back to
	// the production os/exec probe (codex.Readiness); a 0 TTL uses the package
	// default. Constructing it here (not per request) is what gives the cache its
	// request-spanning lifetime.
	backendHealthChecker := backendhealth.NewChecker(nil, 0, nil)

	return &domainStack{
		cfg:                 cfg,
		settings:            settings,
		logger:              logger,
		logCloser:           logCloser,
		db:                  db,
		eng:                 eng,
		workflowRepo:        workflowRepo,
		workflowSvc:         workflowSvc,
		publishedRepo:       publishedRepo,
		instanceRepo:        instanceRepo,
		nodeExecRepo:        nodeExecRepo,
		snapshotRepo:        snapshotRepo,
		fileReader:          fileReader,
		bus:                 bus,
		queueMgr:            queueMgr,
		queueRepo:           queueRepo,
		scheduler:           scheduler,
		schedulerCancel:     schedulerCancel,
		scheduleEval:        scheduleEval,
		scheduleEvalCancel:  scheduleEvalCancel,
		scheduleSvc:         scheduleSvc,
		webhookRegistry:     webhookRegistry,
		notifRepo:           notifRepo,
		dismissalRepo:       dismissalRepo,
		conversationHandler: conversationHandler,
		profileChecker:      profileChecker,
		connChecker:         connChecker,
		connStore:           connStore,
		profileSvc:          profileSvc,
		connSvc:             connSvc,
		capabilityRules:     capabilityRules,
		tracingShutdown:     tracingShutdown,
		sessionRepo:         sessionRepo,
		tokenRepo:           tokenRepo,
		tokenSvc:            tokenSvc,
		deviceRepo:          deviceRepo,
		deviceSvc:           deviceSvc,
		provisioner:         provisioner,
		userRepo:            userRepo,
		userSvc:             userSvc,
		dashboardSvc:        dashboardSvc,
		authMW:              authMW,
		authHandler:         authHandler,
		sweepCancel:         sweepCancel,
		backendHealth:       backendHealthChecker,
	}, nil
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

	// 15. Create the MCP server (mounted at /mcp on the HTTP router) and
	// the full API router with embedded dashboard.
	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	dashHandler := dashboard.Handler()
	notifHandler := adapthttp.NewNotificationHandler(ds.notifRepo)
	notifHandler.SetEventBus(ds.bus)
	webhookHandler := adapthttp.NewWebhookHandler(
		ds.webhookRegistry, ds.instanceRepo, ds.nodeExecRepo, ds.scheduler, ds.queueRepo, ds.queueMgr,
	)
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
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
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		DashboardHandler:    dashHandler,
		NotifHandler:        notifHandler,
		ConversationHandler: ds.conversationHandler,
		WebhookHandler:      webhookHandler,
		MCPHandler:          mcpSrv.HTTPHandler(),
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
	})

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

	// Optionally start MCP stdio server alongside the HTTP server.
	// HTTP MCP is always available at /mcp; --mcp-stdio adds the stdio transport.
	mcpCtx, mcpCancel := context.WithCancel(ctx)
	defer mcpCancel()

	mcpStdio, _ := cmd.Flags().GetBool("mcp-stdio")
	if mcpStdio {
		go func() {
			ds.logger.Info("MCP stdio server starting (alongside HTTP)")
			if err := mcpSrv.ServeStdio(mcpCtx); err != nil && mcpCtx.Err() == nil {
				ds.logger.Error("MCP stdio server error", "error", err)
			}
		}()
	}

	// Translate the OS signal into a stop channel for the serve loop, and cancel
	// the MCP stdio context when shutdown begins so it stops accepting requests.
	stop := make(chan struct{})
	go func() {
		select {
		case sig := <-shutdownCh:
			ds.logger.Info("shutdown signal received", "signal", sig)
		case <-ctx.Done():
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
	ds, err := initDomain(ctx, configPath)
	if err != nil {
		return err
	}
	defer ds.Close(context.Background())

	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)

	// NOTE (Phase 6 scope): the stdio MCP transport is NOT behind the HTTP auth
	// middleware. Phase 1 auth covers the HTTP surfaces (REST, /mcp HTTP, WS);
	// the stdio transport is operator-local (a process the operator launched),
	// so stdio-MCP identity is deferred.
	ds.logger.Info("MCP stdio server starting")
	return mcpSrv.ServeStdio(ctx)
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

// scheduleIdentityResolver builds the creator's identity for context injection
// at fire time, so resolveStamp's InDepartment(override) check passes for a
// department-stamped scheduled run. It constructs the Identity via the SAME
// shared domain builder as AuthMiddleware.enrich (fed by the same UserRepository
// + GroupConfig ports), so a scheduled fire and the same user's interactive
// request resolve to identical identities — LedDepartments included.
type scheduleIdentityResolver struct {
	users  instance.UserRepository
	groups identity.GroupConfig
}

func (r scheduleIdentityResolver) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
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
