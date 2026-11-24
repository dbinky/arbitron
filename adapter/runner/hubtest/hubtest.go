// adapter/runner/hubtest/hubtest.go

// Package hubtest provides real-SQLite pool fixtures, a fake clock, a recording
// RunnerConn, a recording HealthTransitioner and an in-memory runner for hub,
// endpoint and remote-runner tests. Import it only from _test.go files.
package hubtest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// ServerRoot is the server workspace root every fixture hub uses.
const ServerRoot = "/srv/arbitron/workspaces"

// Epoch is the fake clock's start.
var Epoch = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// Clock is a manually advanced clock.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock(t time.Time) *Clock { return &Clock{t: t} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// HealthCall is one ApplyOutcome call.
type HealthCall struct {
	Source  runner.SourceID
	Outcome runner.SourceOutcome
}

// FakeHealth records ApplyOutcome calls and applies their state effect to the
// registry exactly as the real HealthMonitor would for these two classes, so
// subsequent heartbeats see the transitioned health.
type FakeHealth struct {
	reg      runner.Registry
	mu       sync.Mutex
	calls    []HealthCall
	observed []ObserveCall
}

func (f *FakeHealth) ApplyOutcome(ctx context.Context, src runner.SourceID, o runner.SourceOutcome) error {
	f.mu.Lock()
	f.calls = append(f.calls, HealthCall{Source: src, Outcome: o})
	f.mu.Unlock()
	s, err := f.reg.GetSource(ctx, src)
	if err != nil {
		return err
	}
	h := s.Health
	switch o.Class {
	case runner.OutcomeLoggedOut:
		h.Auth = runner.AuthLoggedOut
	case runner.OutcomeExhausted:
		h.Capacity = runner.CapacityExhausted
		h.ResetAt = o.ResetAt
	}
	h.LastOutcome = o.Class
	return f.reg.RecordHealth(ctx, src, h)
}

func (f *FakeHealth) Calls() []HealthCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]HealthCall(nil), f.calls...)
}

// Pool is a real SQLite-backed runner pool.
type Pool struct {
	DB       *sql.DB
	Registry *adaptsqlite.RunnerRegistryRepo
	Ledger   *adaptsqlite.RunnerLedgerRepo
	Log      *adaptsqlite.PoolEventRepo
	Bus      *eventbus.InProcessEventBus
	Events   *runner.EventRecorder
	Notifier *runner.PoolNotifier
	Health   *FakeHealth
	Clock    *Clock
}

// NewPool opens a migrated file-backed database (concurrency-safe across goroutines).
func NewPool(t testing.TB) *Pool {
	t.Helper()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	clock := NewClock(Epoch)
	reg := adaptsqlite.NewRunnerRegistryRepo(db)
	log := adaptsqlite.NewPoolEventRepo(db)
	bus := eventbus.New()
	return &Pool{
		DB: db, Registry: reg, Ledger: adaptsqlite.NewRunnerLedgerRepo(db), Log: log, Bus: bus,
		Events:   runner.NewEventRecorder(log, bus, 1000, clock.Now),
		Notifier: runner.NewPoolNotifier(), Health: &FakeHealth{reg: reg}, Clock: clock,
	}
}

// HubConfig is the design-default timing (5 s / 30 s / 120 s / 30 s) with a
// small telemetry window so bounding is observable.
func HubConfig() runner.HubConfig {
	return runner.HubConfig{
		Heartbeat: 5 * time.Second, SuspectAfter: 30 * time.Second, LostAfter: 120 * time.Second, OfferTTL: 30 * time.Second,
		TelemetryWindow: 4, ServerWorkspaceRoot: ServerRoot,
		Compat: runner.CompatTable{ProtocolMin: 1, ProtocolMax: 1, RunnerMin: "0.2.0", RunnerMaxExclusive: "0.3.0", Harness: map[string]string{"claude": "2.1.0"}},
	}
}

func (p *Pool) NewHub(cfg runner.HubConfig) *runner.Hub {
	return runner.NewHub(runner.HubDeps{Inbox: adaptsqlite.NewRemoteInboxRepo(p.DB), Registry: p.Registry, Ledger: p.Ledger, Events: p.Events, Health: p.Health,
		Notifier: p.Notifier, Clock: p.Clock.Now, Config: cfg})
}

// SeedRun inserts an instance row (owned by the local default user) and returns its id.
func (p *Pool) SeedRun(t testing.TB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := p.DB.Exec(`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state) VALUES (?, ?, 'wf', '{}', 'running')`,
		id.String(), "00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	return id
}

// SeedRunner registers an offline remote runner (what KeyService.Create does)
// and returns a matching claimed key.
func (p *Pool) SeedRunner(t testing.TB, id runner.RunnerID, limit int) runner.RunnerKey {
	t.Helper()
	require.NoError(t, p.Registry.UpsertRunner(context.Background(), runner.Runner{
		ID: id, Name: string(id), StepLimit: limit, Liveness: runner.LivenessOffline,
		Compat: runner.Compatibility{State: runner.CompatCompatible}, CreatedAt: p.Clock.Now(),
	}))
	claimed := p.Clock.Now()
	return runner.RunnerKey{ID: uuid.New(), Name: string(id), RunnerID: id, Prefix: "ark_" + string(id),
		MachinePublicKey: make([]byte, 32), CreatedAt: claimed, ClaimedAt: &claimed}
}

// SeedBuiltIn registers the online built-in runner with the given sources.
func (p *Pool) SeedBuiltIn(t testing.TB, limit int, sources ...runner.Source) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, p.Registry.UpsertRunner(ctx, runner.Runner{ID: runner.BuiltInRunnerID, Name: "built-in", BuiltIn: true,
		StepLimit: limit, Liveness: runner.LivenessOnline, Compat: runner.Compatibility{State: runner.CompatCompatible},
		WorkspaceRoot: ServerRoot, CreatedAt: p.Clock.Now()}))
	require.NoError(t, p.Registry.SetLiveness(ctx, runner.BuiltInRunnerID, runner.LivenessOnline, p.Clock.Now()))
	require.NoError(t, p.Registry.ReplaceSources(ctx, runner.BuiltInRunnerID, sources))
}

// BuiltInAnthropic is a healthy built-in Anthropic subscription source.
func BuiltInAnthropic() runner.Source {
	return runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, ""),
		RunnerID: runner.BuiltInRunnerID, Kind: runner.CredAnthropicSubscription,
		Families: []modelidentity.Family{"anthropic"},
		Health:   runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}
}

// ReserveJob records an intent and reserves it on (id, src) exactly as the
// dispatcher does before calling a StepRunner (EnsureIntent, Reserve, MarkRunning).
func (p *Pool) ReserveJob(t testing.TB, runID uuid.UUID, id runner.RunnerID, src runner.SourceID, op runner.OpType) runner.Assignment {
	t.Helper()
	c := Capability(op, ChatJob("claude-opus-5-5", op).Binding, "anthropic")
	return p.ReserveJobFor(t, runID, id, src, runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Provider: c.Provider, EndpointFingerprint: c.EndpointFingerprint,
		Model: "claude-opus-5-5", Op: op, Affinity: runner.AffinityNone, RunID: runID})
}

// ReserveJobFor reserves an assignment for the exact supplied requirements.
func (p *Pool) ReserveJobFor(t testing.TB, runID uuid.UUID, id runner.RunnerID, src runner.SourceID, req runner.ExecRequirements) runner.Assignment {
	t.Helper()
	ctx := context.Background()
	key := runner.JobKey{RunID: runID, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	job, err := p.Ledger.EnsureIntent(ctx, key, "n1", req)
	require.NoError(t, err)
	r, _, err := p.Registry.GetRunner(ctx, id)
	require.NoError(t, err)
	job, err = p.Ledger.Reserve(ctx, job.ID, runner.Decision{Placed: true, Runner: id, Source: src, Reason: "test"}, r.StepLimit, p.Clock.Now().Add(2*time.Minute))
	require.NoError(t, err)
	f := runner.Fence{JobID: job.ID, RunnerID: id, Generation: job.Generation}
	require.NoError(t, p.Ledger.MarkRunning(ctx, f))
	return runner.Assignment{Job: job, Fence: f}
}

// EventKinds returns the recorded pool event kinds, oldest first.
func (p *Pool) EventKinds(t testing.TB) []string {
	t.Helper()
	evs, err := p.Log.List(context.Background(), 1000)
	require.NoError(t, err)
	out := make([]string, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i].Kind)
	}
	return out
}

// EventsOf returns events of one kind, oldest first.
func (p *Pool) EventsOf(t testing.TB, kind string) []runner.PoolEvent {
	t.Helper()
	evs, err := p.Log.List(context.Background(), 1000)
	require.NoError(t, err)
	var out []runner.PoolEvent
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == kind {
			out = append(out, evs[i])
		}
	}
	return out
}

// DarwinHello is a compatible hello at the server root.
func DarwinHello(sources ...runner.SourceReport) runner.HelloInfo {
	return runner.HelloInfo{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "0.2.0",
		HarnessVersions: map[string]string{"claude": "2.1.30"}, WorkspaceRoot: ServerRoot,
		ScratchRoot: "/Users/op/.arbitron-runner/scratch", Sources: sources}
}

// Capability is the exact evidence a remote runner advertises for b on op.
func Capability(op runner.OpType, b provider.ResolvedBinding, families ...modelidentity.Family) runner.ExecutionCapability {
	fp, err := runner.EndpointFingerprint(b)
	if err != nil {
		panic(err)
	}
	return runner.ExecutionCapability{Op: op, Backend: b.Backend, Provider: provider.Canonicalize(b.Provider), EndpointFingerprint: fp,
		Families: families, Models: []string{b.Model}, ExecutedModel: true}
}

// AnthropicSource is a healthy subscription source report.
func AnthropicSource() runner.SourceReport {
	return runner.SourceReport{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"},
		Capabilities: []runner.ExecutionCapability{Capability(runner.OpChat, ChatJob("claude-opus-5-5", runner.OpChat).Binding, "anthropic"), Capability(runner.OpStream, ChatJob("claude-opus-5-5", runner.OpStream).Binding, "anthropic")},
		Observation:  &runner.ProbeResult{Auth: runner.AuthOK}, ObservationSeq: 1,
		Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}
}

// LunaRouteSource is a healthy LunaRoute source report keyed by the server's
// connection ref, serving the given families.
func LunaRouteSource(ref string, families ...modelidentity.Family) runner.SourceReport {
	b := provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute, BaseURL: provider.LunaRouteHostedRoot, Model: "glm-5.3", ConnectionRef: ref}
	return runner.SourceReport{Kind: runner.CredLunaRoute, CredentialRef: ref, Families: families,
		Capabilities: []runner.ExecutionCapability{Capability(runner.OpChat, b, families...), Capability(runner.OpStream, b, families...)},
		Observation:  &runner.ProbeResult{Auth: runner.AuthOK}, ObservationSeq: 1,
		Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}
}

// ChatJob is a chat or stream StepJob on the no-connection subscription path.
func ChatJob(model string, op runner.OpType) runner.StepJob {
	return runner.StepJob{Op: op,
		Binding: provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: model},
		Chat:    &engine.ChatRequest{Model: model, Prompt: "say hi"}}
}

// FakeConn records every Outbound and its close.
type FakeConn struct {
	mu       sync.Mutex
	sent     []runner.Outbound
	consumed []bool
	closed   bool
	code     int
	reason   string
	sendErr  error
	notify   chan struct{}
}

func NewFakeConn() *FakeConn { return &FakeConn{notify: make(chan struct{}, 1)} }

func (c *FakeConn) Send(_ context.Context, m runner.Outbound) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendErr != nil {
		return c.sendErr
	}
	c.sent = append(c.sent, m)
	c.consumed = append(c.consumed, false)
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return nil
}

func (c *FakeConn) Close(code int, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed, c.code, c.reason = true, code, reason
	}
	return nil
}

func (c *FakeConn) FailSends(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendErr = err
}

func (c *FakeConn) Sent() []runner.Outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]runner.Outbound(nil), c.sent...)
}

func (c *FakeConn) Kinds() []string {
	var out []string
	for _, m := range c.Sent() {
		out = append(out, m.Kind)
	}
	return out
}

// Count counts sent messages of kind for jobID (uuid.Nil = any job).
func (c *FakeConn) Count(kind string, jobID uuid.UUID) int {
	n := 0
	for _, m := range c.Sent() {
		if m.Kind == kind && (jobID == uuid.Nil || m.JobID == jobID) {
			n++
		}
	}
	return n
}

func (c *FakeConn) Closed() (int, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code, c.reason, c.closed
}

// Await returns the oldest not-yet-awaited message of kind for jobID (uuid.Nil
// = any job), waiting up to 2 s.
func (c *FakeConn) Await(t testing.TB, kind string, jobID uuid.UUID) runner.Outbound {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		c.mu.Lock()
		for i, m := range c.sent {
			if !c.consumed[i] && m.Kind == kind && (jobID == uuid.Nil || m.JobID == jobID) {
				c.consumed[i] = true
				c.mu.Unlock()
				return m
			}
		}
		c.mu.Unlock()
		select {
		case <-c.notify:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("no %q message for job %s; sent kinds: %s", kind, jobID, fmt.Sprint(c.Kinds()))
			return runner.Outbound{}
		}
	}
}
