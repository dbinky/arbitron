package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	adaptmodelcatalog "github.com/arbitron/arbitron/adapter/modelcatalog"
	"github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/arbitron/arbitron/adapter/probe"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
)

// modelRefreshDeps are the live collaborators of the catalog refresh. Every
// field is required; none is substituted with a default here.
type modelRefreshDeps struct {
	Settings  *settingsdomain.Service
	Policy    *modelidentity.PolicyService
	Profiles  *provider.ProfileService
	Inventory *providersadapter.ExecutionPathInventory
	Chats     engine.ChatProviderSelector
	Agents    engine.AgentProviderSelector
	// Streams is the selector interactive turns stream through; the interactive
	// chat and agent lanes probe the candidate through it, so the proof is the
	// live path's.
	Streams   engine.InteractiveStreamSelector
	Store     *adaptsqlite.ModelCatalogRepo
	Gate      *provider.ExecutionGate
	Publisher provider.AdvancementPublisher
	Logger    *slog.Logger
	Clock     func() time.Time
	NewTicker func(time.Duration) (<-chan time.Time, func())
	// ProbeDir is the scratch directory Claude Code subscription and claude-code
	// agent checks run in; it must lie under the claude adapters' workspace root.
	ProbeDir string
	// ProbeTimeout is the configured provider-call budget. The router preserves
	// shorter values but independently caps every probe at 30 seconds.
	ProbeTimeout time.Duration
	// Fetcher defaults to the guarded production fetcher; a test supplies a
	// fixture because the guard refuses loopback sources.
	Fetcher modelcatalog.CatalogFetcher
	// FetchTransport replaces only HTTP I/O in root tests. Nil retains guarded
	// production dialing; request/redirect guards always remain in the fetcher.
	FetchTransport http.RoundTripper
	// DB backs the durable announcement queue the refresh's discovery events
	// land in.
	DB *sql.DB
	// AnnouncementClient defaults to the SSRF-guarded client; a test supplies a
	// receiver transport because the guard refuses loopback destinations. It
	// must never be the unguarded environment-proxy client.
	AnnouncementClient *http.Client
}

// announcementTimeout bounds one webhook attempt end to end; the sender clamps
// the shared client to it.
const announcementTimeout = 15 * time.Second

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// modelRefresh is the assembled refresh orchestration: the service, the
// single-flight coordinator in front of it, and the loop that drives it.
type modelRefresh struct {
	probe       modelcatalog.UsabilityProbe
	service     *modelcatalog.RefreshService
	coordinator *modelcatalog.RefreshCoordinator
	// admin is the authorized list/manual-refresh face behind the MCP
	// model_catalog_* tools; its refresh goes through the same coordinator.
	admin *modelcatalog.AdminService
	// candidates is the confirmed-candidate source of follow-profile create/edit
	// (ProfileService.SetModelSelection). It shares the refresh's probe router,
	// inventory and stored evidence, so a profile is created on the same proof
	// rules a refresh advances it by.
	candidates *modelcatalog.CandidateService
	logger     *slog.Logger
	newTicker  func(time.Duration) (<-chan time.Time, func())
	// seedPending retries the YAML profile seeds still pending after boot. The
	// scheduled loop calls it after each completed coordinated cycle, outside
	// the cycle's admission slot, so an outage at first boot does not postpone
	// configured following aliases to another boot. Nil disables the retry.
	seedPending func(context.Context)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// initial is closed once the loop's immediate first cycle (and its seed
	// retry) has returned; nil until startScheduled runs.
	initial chan struct{}
}

// newModelRefresh builds the dependency graph without starting anything, so a
// constructed stack never reaches the network until start is called.
func newModelRefresh(d modelRefreshDeps) (*modelRefresh, error) {
	if d.Settings == nil || d.Policy == nil || d.Profiles == nil || d.Inventory == nil || d.Store == nil {
		return nil, fmt.Errorf("model refresh discovery: missing required dependency")
	}
	if d.Publisher == nil || d.Gate == nil {
		return nil, fmt.Errorf("model refresh publication: %w", provider.ErrAdvancementPortsMissing)
	}
	mapper, err := modelidentity.NewIdentityMapper(d.Policy)
	if err != nil {
		return nil, fmt.Errorf("model refresh identity mapper: %w", err)
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	normalizer, err := adaptmodelcatalog.NewNormalizer(mapper, clock)
	if err != nil {
		return nil, fmt.Errorf("model refresh normalizer: %w", err)
	}
	chatLane, err := probe.NewChatUsabilityLane(probe.ChatLaneDeps{Chats: d.Chats, WithBinding: d.Inventory.WithBinding, WorkDir: d.ProbeDir})
	if err != nil {
		return nil, fmt.Errorf("model refresh chat lane: %w", err)
	}
	agentLane, err := probe.NewAgentUsabilityLane(probe.AgentLaneDeps{Agents: d.Agents, WithBinding: d.Inventory.WithBinding, WorkDir: d.ProbeDir})
	if err != nil {
		return nil, fmt.Errorf("model refresh agent lane: %w", err)
	}
	interactiveChatLane, err := probe.NewInteractiveChatUsabilityLane(probe.InteractiveChatLaneDeps{Streams: d.Streams, WithBinding: d.Inventory.WithBinding, WorkDir: d.ProbeDir})
	if err != nil {
		return nil, fmt.Errorf("model refresh interactive chat lane: %w", err)
	}
	interactiveAgentLane, err := probe.NewInteractiveAgentUsabilityLane(probe.InteractiveAgentLaneDeps{Streams: d.Streams, WithBinding: d.Inventory.WithBinding, WorkDir: d.ProbeDir})
	if err != nil {
		return nil, fmt.Errorf("model refresh interactive agent lane: %w", err)
	}
	router, err := probe.NewUsabilityRouter(probe.UsabilityRouterDeps{Lanes: []probe.UsabilityLane{chatLane, agentLane, interactiveChatLane, interactiveAgentLane}, Timeout: d.ProbeTimeout, Clock: clock})
	if err != nil {
		return nil, fmt.Errorf("model refresh probe router: %w", err)
	}
	assembled := &modelRefresh{probe: router, logger: d.Logger, newTicker: d.NewTicker}
	var fetcher modelcatalog.CatalogFetcher = d.Fetcher
	if fetcher == nil {
		fetcher = adaptmodelcatalog.NewFetcherWithTransport(d.FetchTransport)
	}
	pipeline, err := modelcatalog.NewDiscoveryPipeline(modelcatalog.DiscoveryDeps{
		Settings: d.Settings, Fetcher: fetcher, Normalizer: normalizer, Mapper: mapper,
		Inventory: d.Inventory, Profiles: d.Profiles, Store: d.Store, Probe: assembled.probe, AdvanceValidator: d.Profiles, Clock: clock, Logger: d.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("model refresh pipeline: %w", err)
	}
	if d.DB == nil {
		return nil, fmt.Errorf("model refresh announcements: nil database")
	}
	announceClient := d.AnnouncementClient
	if announceClient == nil {
		announceClient = outbound.NewGuardedClient(announcementTimeout, 0)
	}
	// The drain reads the raw webhook through the internal settings reader and
	// runs after every cycle, including a failed one (RefreshService.Refresh).
	announcer := modelcatalog.NewAnnouncementService(
		adaptsqlite.NewModelAnnouncementRepo(d.DB), d.Settings,
		notification.NewTeamsModelSender(announceClient), clockFunc(clock))
	service := &modelcatalog.RefreshService{
		Pipeline: pipeline, Store: d.Store, Gate: d.Gate, Publisher: d.Publisher, Journal: d.Store, Logger: d.Logger,
	}
	if err := service.SetAnnouncer(announcer); err != nil {
		return nil, fmt.Errorf("model refresh announcer: %w", err)
	}
	coordinator := modelcatalog.NewRefreshCoordinator(service.Refresh)
	admin := &modelcatalog.AdminService{Store: d.Store, Refresh: coordinator.Refresh}
	candidates, err := modelcatalog.NewCandidateService(modelcatalog.CandidateDeps{
		Store: d.Store, Inventory: d.Inventory, Pipeline: pipeline, Coordinator: coordinator, Gate: d.Gate, Mapper: mapper, Clock: clock,
	})
	if err != nil {
		return nil, fmt.Errorf("model refresh candidate source: %w", err)
	}
	assembled.service, assembled.coordinator, assembled.admin, assembled.candidates = service, coordinator, admin, candidates
	return assembled, nil
}

// startupPass is the synchronous startup step: publication/journal recovery
// only (RefreshService.Recover), admitted through the coordinator so it never
// overlaps a manual refresh. It is local SQLite work. A failure fails startup
// before execution recovery, since recovered runs would otherwise resolve
// profiles from an unpublished working set. Catalog fetches, probes and
// announcement delivery never gate readiness; they run in startScheduled.
func (m *modelRefresh) startupPass(ctx context.Context) error {
	if err := m.coordinator.Do(ctx, m.service.Recover); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("model publication recovery: %w", err)
	}
	return ctx.Err()
}

// startScheduled starts the background discovery loop once: an immediate first
// cycle, then every four hours. Each completed cycle retries pending profile
// seeds. Only the serving entrypoint calls it; stdio MCP does not.
func (m *modelRefresh) startScheduled(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel, m.done, m.initial = cancel, make(chan struct{}), make(chan struct{})
	var initialOnce sync.Once
	initial := m.initial
	refresh := func(ctx context.Context) (modelcatalog.RefreshReport, error) {
		defer initialOnce.Do(func() { close(initial) })
		report, err := m.coordinator.Refresh(ctx)
		// Seed preparation has its own durable prospective discovery. A failed
		// publication must not starve it; admission refusals and shutdown must
		// not start another attempt alongside work we did not join.
		if ctx.Err() == nil && !errors.Is(err, modelcatalog.ErrRefreshBusy) && !errors.Is(err, modelcatalog.ErrRefreshClosed) && m.seedPending != nil {
			m.seedPending(ctx)
		}
		return report, err
	}
	loop := &modelcatalog.RefreshLoop{Refresh: refresh, Logger: m.logger, NewTicker: m.newTicker, Immediate: true}
	go func(done chan struct{}) {
		defer close(done)
		loop.RunScheduled(ctx)
	}(m.done)
}

// initialPass returns a channel closed when the immediate first cycle has
// returned, or nil when no loop was started.
func (m *modelRefresh) initialPass() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initial
}

// stop cancels the loop and any in-flight cycle and waits for both, so nothing
// writes to the database after it is closed.
func (m *modelRefresh) stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	m.coordinator.Close()
}
