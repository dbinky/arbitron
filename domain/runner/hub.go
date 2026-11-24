// domain/runner/hub.go
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// HubDeps are the hub's collaborators. All but Clock are required.
type HubDeps struct {
	Inbox    RemoteInbox
	Registry Registry
	Ledger   Ledger
	Events   *EventRecorder
	Health   HubHealth
	Notifier *PoolNotifier
	Clock    func() time.Time
	Config   HubConfig
}

// Hub owns every remote runner session in this server process.
type Hub struct {
	inbox    RemoteInbox
	reg      Registry
	ledger   Ledger
	events   *EventRecorder
	health   HubHealth
	notifier *PoolNotifier
	now      func() time.Time
	cfg      HubConfig

	// admitMu serialises admission with loss/revocation so a revoked or lost
	// runner can never be re-admitted halfway through being torn down.
	admitMu sync.Mutex

	mu        sync.Mutex
	sessions  map[RunnerID]*Session
	contact   map[RunnerID]time.Time
	revoked   map[RunnerID]bool
	telemetry map[RunnerID][]Telemetry
	jobs      map[uuid.UUID]*remoteJob
	serial    uint64
	probes    map[uuid.UUID]*pendingProbe
	observed  map[SourceID]observationMark
	closed    bool
	loop      sync.WaitGroup
}

// remoteJob is a waiter for one offered generation; all fields except emission
// are guarded by Hub.mu.
type remoteJob struct {
	ctx        context.Context
	a          Assignment
	dispatch   DispatchPayload
	stream     chan engine.ChatChunk
	offeredAt  time.Time
	accepted   bool
	lastSeq    uint64
	finished   bool
	result     *RemoteResult
	err        error
	acceptedCh chan struct{}
	done       chan struct{}
	emitMu     sync.Mutex
}

// NewHub validates deps and config and builds the hub.
func NewHub(d HubDeps) *Hub {
	if d.Inbox == nil || d.Registry == nil || d.Ledger == nil || d.Events == nil || d.Health == nil || d.Notifier == nil {
		panic("runner: NewHub requires Inbox, Registry, Ledger, Events, Health and Notifier")
	}
	c := d.Config
	if c.Heartbeat <= 0 || c.SuspectAfter <= 0 || c.LostAfter <= c.SuspectAfter || c.OfferTTL <= 0 || c.TelemetryWindow < 1 {
		panic(fmt.Sprintf("runner: invalid hub config %+v", c))
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &Hub{
		inbox: d.Inbox, reg: d.Registry, ledger: d.Ledger, events: d.Events, health: d.Health, notifier: d.Notifier, now: d.Clock, cfg: c,
		sessions: map[RunnerID]*Session{}, contact: map[RunnerID]time.Time{}, revoked: map[RunnerID]bool{},
		telemetry: map[RunnerID][]Telemetry{}, jobs: map[uuid.UUID]*remoteJob{},
		probes: map[uuid.UUID]*pendingProbe{}, observed: map[SourceID]observationMark{},
	}
}

func (h *Hub) record(ctx context.Context, kind string, id RunnerID, src SourceID, cause Cause, detail map[string]any) {
	h.events.Record(ctx, PoolEvent{ID: uuid.New(), At: h.now(), Kind: kind, RunnerID: id, SourceID: src, Cause: cause, Detail: detail})
}

func (h *Hub) isRevoked(id RunnerID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.revoked[id]
}

func (h *Hub) current(s *Session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return s != nil && h.sessions[s.RunnerID] == s
}

func (h *Hub) session(id RunnerID) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

// send delivers best-effort: a failed write surfaces as a dropped socket (the
// endpoint's read loop ends and calls Disconnect); liveness decides loss.
func (h *Hub) send(ctx context.Context, s *Session, m Outbound) {
	if s == nil {
		return
	}
	_ = s.conn.Send(ctx, m)
}

func remoteKind(k CredentialKind) bool {
	return k == CredAnthropicSubscription || k == CredOpenAISubscription || k == CredLunaRoute
}

// reportedSources converts reports to registry sources: remote kinds only,
// de-duplicated by source id, with neutral health (applyReportedHealth then
// records the reported state through the proper transitions).
func reportedSources(id RunnerID, reps []SourceReport) []Source {
	out := make([]Source, 0, len(reps))
	seen := map[SourceID]bool{}
	for _, r := range reps {
		if !remoteKind(r.Kind) {
			continue
		}
		sid := MakeSourceID(id, r.Kind, r.CredentialRef)
		if seen[sid] {
			continue
		}
		seen[sid] = true
		out = append(out, Source{ID: sid, RunnerID: id, Kind: r.Kind, CredentialRef: r.CredentialRef,
			Families: r.Families, Backends: r.Backends, Models: r.Models,
			UnavailableReason: r.UnavailableReason, Capabilities: r.Capabilities,
			Health: SourceHealth{Auth: AuthUnknown, Capacity: CapacityAvailable}})
	}
	return out
}

func sourceSignature(s Source) string {
	fam := make([]string, 0, len(s.Families))
	for _, f := range s.Families {
		fam = append(fam, string(f))
	}
	sort.Strings(fam)
	be := append([]string(nil), s.Backends...)
	sort.Strings(be)
	md := append([]string(nil), s.Models...)
	sort.Strings(md)
	evidence, _ := json.Marshal(s.Capabilities)
	return string(s.ID) + "|" + strings.Join(fam, ",") + "|" + strings.Join(be, ",") + "|" + strings.Join(md, ",") + "|" + s.UnavailableReason + "|" + string(evidence)
}

func sameSourceSet(a, b []Source) bool {
	if len(a) != len(b) {
		return false
	}
	sa := make([]string, 0, len(a))
	for _, s := range a {
		sa = append(sa, sourceSignature(s))
	}
	sb := make([]string, 0, len(b))
	for _, s := range b {
		sb = append(sb, sourceSignature(s))
	}
	sort.Strings(sa)
	sort.Strings(sb)
	return reflect.DeepEqual(sa, sb)
}

// applyReportedHealth maps runner-reported health onto the registry. New
// logged_out / exhausted states go through the HealthTransitioner (which owns
// alerts and events); other observations are merged directly. Recovery from
// exhaustion passes through probation; clearing logged_out emits source.recovered.
func (h *Hub) applyReportedHealth(ctx context.Context, id RunnerID, reps []SourceReport, now time.Time) error {
	for _, rep := range reps {
		if !remoteKind(rep.Kind) {
			continue
		}
		sid := MakeSourceID(id, rep.Kind, rep.CredentialRef)
		cur, err := h.reg.GetSource(ctx, sid)
		if err != nil {
			return fmt.Errorf("hub: read source %s: %w", sid, err)
		}
		rh := rep.Health
		switch {
		case rh.Auth == AuthLoggedOut && cur.Health.Auth != AuthLoggedOut:
			if err := h.health.ApplyOutcome(ctx, sid, SourceOutcome{Class: OutcomeLoggedOut}); err != nil {
				return err
			}
		case rh.Capacity == CapacityExhausted && cur.Health.Capacity != CapacityExhausted:
			if err := h.health.ApplyOutcome(ctx, sid, SourceOutcome{Class: OutcomeExhausted, ResetAt: rh.ResetAt, Windows: rh.Windows, HeadroomHint: rh.Headroom}); err != nil {
				return err
			}
		default:
			next := cur.Health
			recovered := false
			if rh.Auth == AuthOK && cur.Health.Auth != AuthOK {
				recovered = cur.Health.Auth == AuthLoggedOut
				next.Auth = AuthOK
			}
			if rh.Capacity == CapacityAvailable && cur.Health.Capacity == CapacityExhausted {
				next.Capacity = CapacityProbation
				next.ResetAt = nil
			}
			if rh.Headroom != nil {
				next.Headroom = rh.Headroom
			}
			if rh.Windows != nil {
				next.Windows = rh.Windows
			}
			probed := now
			if rh.LastProbeAt != nil {
				probed = *rh.LastProbeAt
			}
			changed := !reflect.DeepEqual(next, cur.Health)
			next.LastProbeAt = &probed
			if changed {
				if err := h.reg.RecordHealth(ctx, sid, next); err != nil {
					return err
				}
				h.notifier.Notify()
			}
			if recovered {
				h.record(ctx, evtSourceRecovered, id, sid, Cause{Kind: "outcome", Actor: string(id)}, map[string]any{"kind": string(rep.Kind)})
			}
		}
	}
	return nil
}

// Admit admits an authenticated, machine-proven runner (the endpoint has
// already verified key and machine binding). On success the runner is
// online, its sources are recorded, and MsgAdmitted has been sent first.
func (h *Hub) Admit(ctx context.Context, key RunnerKey, conn RunnerConn, hello HelloInfo) (*Session, error) {
	if err := validateSourceReports(hello.Sources); err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("runner: Admit requires a connection")
	}
	id := key.RunnerID
	h.admitMu.Lock()
	defer h.admitMu.Unlock()
	if key.RevokedAt != nil || h.isRevoked(id) {
		return nil, ErrRunnerRevoked
	}
	r, _, err := h.reg.GetRunner(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("admit %s: %w", id, err)
	}
	if r.BuiltIn {
		return nil, fmt.Errorf("admit %s: the built-in runner cannot connect remotely", id)
	}
	now := h.now()
	prevLiveness := r.Liveness
	r.OS, r.RunnerVersion, r.HarnessVersions, r.WorkspaceRoot = hello.OS, hello.RunnerVersion, hello.HarnessVersions, hello.WorkspaceRoot
	if err := h.reg.UpsertRunner(ctx, r); err != nil {
		return nil, fmt.Errorf("admit %s: %w", id, err)
	}
	compat := h.cfg.Compat.Check(hello, h.cfg.ServerWorkspaceRoot)
	if err := h.reg.SetCompat(ctx, id, compat); err != nil {
		return nil, fmt.Errorf("admit %s: %w", id, err)
	}
	if compat.State == CompatIncompatible {
		if err := h.reg.SetLiveness(ctx, id, LivenessOffline, now); err != nil {
			return nil, fmt.Errorf("admit %s: %w", id, err)
		}
		h.record(ctx, evtRunnerIncompatible, id, "", Cause{Kind: "system", Actor: string(id)}, map[string]any{
			"reason": compat.Reason, "os": hello.OS, "protocolVersion": hello.ProtocolVersion, "runnerVersion": hello.RunnerVersion})
		h.notifier.Notify()
		return nil, &IncompatibleError{Compat: compat}
	}
	if err := h.reg.ReplaceSources(ctx, id, reportedSources(id, hello.Sources)); err != nil {
		return nil, fmt.Errorf("admit %s: %w", id, err)
	}
	if err := h.applyReportedHealth(ctx, id, hello.Sources, now); err != nil {
		return nil, fmt.Errorf("admit %s: %w", id, err)
	}
	if err := h.reg.SetLiveness(ctx, id, LivenessOnline, now); err != nil {
		return nil, fmt.Errorf("admit %s: %w", id, err)
	}

	h.mu.Lock()
	old := h.sessions[id]
	_, seen := h.contact[id]
	h.serial++
	s := &Session{RunnerID: id, KeyID: key.ID, conn: conn, serial: h.serial}
	h.sessions[id] = s
	h.failProbesLocked(func(p *pendingProbe) bool { return p.runner == id && p.serial != s.serial }, fmt.Errorf("%w: session replaced", ErrRemoteProbeUnavailable))
	h.contact[id] = now
	h.mu.Unlock()
	if old != nil {
		_ = old.conn.Close(CloseSuperseded, "superseded by a newer connection from this runner")
	}
	if err := conn.Send(ctx, Outbound{Kind: MsgAdmitted}); err != nil {
		h.Disconnect(s)
		return nil, fmt.Errorf("admit %s: send admitted: %w", id, err)
	}
	claimed := h.reconcileHeld(ctx, s, hello.Held, now)
	h.resendOffers(ctx, s, claimed)
	kind := evtRunnerConnected
	if seen || prevLiveness == LivenessSuspect || prevLiveness == LivenessLost {
		kind = evtRunnerReconnected
	}
	h.record(ctx, kind, id, "", Cause{Kind: "liveness", Actor: string(id)}, map[string]any{
		"runnerVersion": hello.RunnerVersion, "compat": string(compat.State)})
	h.notifier.Notify()
	return s, nil
}

// Disconnect forgets a closed socket. It never changes liveness: only
// heartbeat gaps do (Tick).
func (h *Hub) Disconnect(s *Session) {
	if s == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[s.RunnerID] == s {
		delete(h.sessions, s.RunnerID)
		h.failProbesLocked(func(p *pendingProbe) bool { return p.runner == s.RunnerID && p.serial == s.serial }, ErrRemoteProbeUnavailable)
	}
}

// Handle processes one message from an admitted session. A non-nil error
// tells the endpoint to close the connection.
func (h *Hub) Handle(ctx context.Context, s *Session, in Inbound) error {
	if s == nil {
		return ErrSessionClosed
	}
	if h.isRevoked(s.RunnerID) {
		return ErrRunnerRevoked
	}
	if !h.current(s) {
		return ErrSessionClosed
	}
	now := h.now()
	h.mu.Lock()
	h.contact[s.RunnerID] = now
	h.mu.Unlock()
	switch in.Kind {
	case MsgProbeResult:
		return h.handleProbeResult(ctx, s, in.ProbeResult, now)
	case MsgHeartbeat:
		if in.Heartbeat == nil {
			return fmt.Errorf("%w: heartbeat without payload", ErrMalformedMessage)
		}
		return h.handleHeartbeat(ctx, s, *in.Heartbeat, now)
	case MsgAccept, MsgReject, MsgOutput, MsgResult:
		return h.handleJob(ctx, s, in, now)
	default:
		return fmt.Errorf("%w: unexpected kind %q", ErrMalformedMessage, in.Kind)
	}
}

func (h *Hub) handleHeartbeat(ctx context.Context, s *Session, hb HeartbeatInfo, now time.Time) error {
	if err := validateSourceReports(hb.Sources); err != nil {
		return err
	}
	t := hb.Telemetry
	// Compare in milliseconds before converting to Duration to avoid overflow.
	if hb.TelemetryAgeMs < 0 || hb.TelemetryAgeMs > (3*h.cfg.Heartbeat).Milliseconds() {
		t = Telemetry{SampledAt: now}
	} else {
		t.SampledAt = now.Add(-time.Duration(hb.TelemetryAgeMs) * time.Millisecond)
	}
	h.RecordTelemetry(s.RunnerID, t)
	r, cur, err := h.reg.GetRunner(ctx, s.RunnerID)
	if err != nil {
		return err
	}
	if err := h.reg.SetLiveness(ctx, s.RunnerID, LivenessOnline, now); err != nil {
		return err
	}
	if r.Liveness == LivenessSuspect {
		h.record(ctx, evtRunnerReconnected, s.RunnerID, "", Cause{Kind: "liveness", Actor: string(s.RunnerID)}, map[string]any{"from": string(r.Liveness)})
		h.notifier.Notify()
	}
	if hb.Sources != nil {
		next := reportedSources(s.RunnerID, hb.Sources)
		if !sameSourceSet(cur, next) {
			if err := h.reg.ReplaceSources(ctx, s.RunnerID, next); err != nil {
				return err
			}
			h.notifier.Notify()
		}
		if err := h.applyReportedHealth(ctx, s.RunnerID, hb.Sources, now); err != nil {
			return err
		}
	}
	return h.refreshHeld(ctx, s, hb.Jobs, now)
}

// RecordTelemetry appends t to id's bounded in-memory window (heartbeats and
// the built-in loop of P2 T7).
func (h *Hub) RecordTelemetry(id RunnerID, t Telemetry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w := append(h.telemetry[id], t)
	if len(w) > h.cfg.TelemetryWindow {
		w = append([]Telemetry(nil), w[len(w)-h.cfg.TelemetryWindow:]...)
	}
	h.telemetry[id] = w
}

// Latest returns the newest sample per runner (TelemetrySource).
func (h *Hub) Latest() map[RunnerID]Telemetry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[RunnerID]Telemetry, len(h.telemetry))
	for id, w := range h.telemetry {
		if len(w) > 0 {
			out[id] = w[len(w)-1]
		}
	}
	return out
}

// Window returns a copy of id's recent samples, oldest first.
func (h *Hub) Window(id RunnerID) []Telemetry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Telemetry(nil), h.telemetry[id]...)
}
