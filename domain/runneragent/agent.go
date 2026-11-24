package runneragent

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// Config describes this runner's installation and reconnect cadence.
type Config struct {
	OS, Arch                                          string
	ProtocolVersion                                   int
	RunnerVersion                                     string
	HarnessVersions                                   map[string]string
	WorkspaceRoot, ScratchRoot                        string
	Capabilities                                      []string
	Heartbeat, ProbeInterval, BackoffBase, BackoffCap time.Duration
}

// Deps contains the agent's transport-neutral production ports.
type Deps struct {
	Conn      Connection
	Exec      JobExecutor
	Sources   LocalSources
	Telemetry runner.MachineTelemetry
	Spool     Spool
	Identity  IdentityStore
	Clock     Clock
	Rand      func() float64
	Logger    *slog.Logger
}

// Agent maintains admission and liveness across connections.
type Agent struct {
	cfg         Config
	deps        Deps
	mu          sync.Mutex
	jobs        map[JobRef]*localJob
	fenced      map[string]int64
	wake        chan struct{}
	sessionConn Session
	sessionCtx  context.Context
	runCtx      context.Context
	probeMu     sync.Mutex
	probing     map[string]bool
	probeWG     sync.WaitGroup
}

func New(cfg Config, deps Deps) (*Agent, error) {
	var missing []string
	for _, p := range []struct {
		name  string
		value any
	}{{"Conn", deps.Conn}, {"Exec", deps.Exec}, {"Sources", deps.Sources}, {"Telemetry", deps.Telemetry}, {"Spool", deps.Spool}, {"Identity", deps.Identity}} {
		if p.value == nil || reflect.ValueOf(p.value).Kind() == reflect.Pointer && reflect.ValueOf(p.value).IsNil() {
			missing = append(missing, p.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runneragent: missing %s", strings.Join(missing, ", "))
	}
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = 5 * time.Second
	}
	if cfg.ProbeInterval == 0 {
		cfg.ProbeInterval = time.Minute
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 500 * time.Millisecond
	}
	if cfg.BackoffCap == 0 {
		cfg.BackoffCap = 30 * time.Second
	}
	if cfg.OS == "" || cfg.Arch == "" || cfg.ProtocolVersion <= 0 || cfg.RunnerVersion == "" || cfg.ScratchRoot == "" || cfg.Heartbeat <= 0 || cfg.ProbeInterval <= 0 || cfg.BackoffBase <= 0 || cfg.BackoffCap < cfg.BackoffBase {
		return nil, fmt.Errorf("runneragent: invalid config")
	}
	if deps.Clock == nil {
		deps.Clock = SystemClock{}
	}
	if deps.Rand == nil {
		deps.Rand = rand.Float64
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Agent{cfg: cfg, deps: deps, jobs: make(map[JobRef]*localJob), fenced: make(map[string]int64), wake: make(chan struct{}), probing: make(map[string]bool)}, nil
}

// Run reconnects until the caller cancels. Successful admission resets backoff.
func (a *Agent) Run(ctx context.Context) error {
	a.runCtx = ctx
	if err := a.restore(ctx); err != nil {
		return err
	}
	defer a.stopJobs()
	attempt := 0
	for ctx.Err() == nil {
		s, err := a.deps.Conn.Dial(ctx)
		admitted := false
		if err == nil {
			admitted, err = a.session(ctx, s)
			_ = s.Close()
		}
		if ctx.Err() != nil {
			break
		}
		if admitted {
			attempt = 0
		}
		a.deps.Logger.Debug("runner connection ended", "error", err)
		delay := Backoff(attempt, a.cfg.BackoffBase, a.cfg.BackoffCap, a.deps.Rand)
		attempt++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.deps.Clock.After(delay):
		}
	}
	return ctx.Err()
}

type received struct {
	in  Inbound
	err error
}

func (a *Agent) session(parent context.Context, s Session) (bool, error) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	recv := make(chan received)
	go func() {
		defer close(done)
		for {
			in, err := s.Recv(ctx)
			select {
			case recv <- received{in, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = s.Close()
		<-done
		a.probeWG.Wait()
		a.mu.Lock()
		a.sessionConn = nil
		a.mu.Unlock()
	}()
	key, err := a.deps.Identity.Key(ctx)
	if err != nil {
		return false, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return false, fmt.Errorf("runneragent: invalid identity key")
	}
	admitted := false
	var heartbeat, probe <-chan time.Time
	interval := a.cfg.Heartbeat
	for {
		select {
		case <-ctx.Done():
			return admitted, ctx.Err()
		case r := <-recv:
			if r.err != nil {
				return admitted, r.err
			}
			in := r.in
			switch in.Kind {
			case KindChallenge:
				if admitted || in.Challenge == nil {
					continue
				}
				c := in.Challenge
				response := &ChallengeResponse{PublicKey: key.Public().(ed25519.PublicKey), Signature: ed25519.Sign(key, ChallengeMessage(c.KeyID, c.Nonce))}
				if err := s.Send(ctx, Outbound{Kind: KindChallengeResponse, ChallengeResponse: response}); err != nil {
					return admitted, err
				}
				sources := a.deps.Sources.Probe(ctx)
				hello := &Hello{OS: a.cfg.OS, Arch: a.cfg.Arch, ProtocolVersion: a.cfg.ProtocolVersion, RunnerVersion: a.cfg.RunnerVersion, HarnessVersions: a.cfg.HarnessVersions, WorkspaceRoot: a.cfg.WorkspaceRoot, ScratchRoot: a.cfg.ScratchRoot, Capabilities: a.cfg.Capabilities, Sources: sources, Held: a.held()}
				if err := s.Send(ctx, Outbound{Kind: KindHello, Hello: hello}); err != nil {
					return admitted, err
				}
			case KindRefused:
				if in.Refused != nil {
					return admitted, &RefusedError{Code: in.Refused.Code, Reason: in.Refused.Reason}
				}
			case KindAdmitted:
				if in.Admitted == nil || admitted {
					continue
				}
				admitted = true
				a.mu.Lock()
				a.sessionConn = s
				a.sessionCtx = ctx
				for _, j := range a.jobs {
					j.sent = 0
					j.resultSent = false
					if err := a.flush(ctx, j); err != nil {
						a.mu.Unlock()
						return admitted, err
					}
				}
				a.mu.Unlock()
				if in.Admitted.HeartbeatSeconds > 0 {
					interval = time.Duration(in.Admitted.HeartbeatSeconds) * time.Second
				}
				heartbeat = a.deps.Clock.After(interval)
				probe = a.deps.Clock.After(a.cfg.ProbeInterval)
			case KindFenced, KindCancel:
				if admitted {
					if err := a.fence(ctx, in); err != nil {
						return admitted, err
					}
				}
			case KindAck:
				if admitted {
					if err := a.ack(ctx, in); err != nil {
						return admitted, err
					}
				}
			case KindDispatch:
				if admitted && in.Dispatch != nil && in.JobID != "" && in.Generation > 0 {
					if err := a.onDispatch(ctx, s, in); err != nil {
						return admitted, err
					}
				}
			case KindProbe:
				if !admitted || in.Probe == nil {
					continue
				}
				a.startProbe(ctx, s, *in.Probe)
			}
		case <-heartbeat:
			sample, err := a.deps.Telemetry.Sample(ctx)
			now := a.deps.Clock.Now()
			age := int64(0)
			if err != nil || sample.SampledAt.IsZero() {
				sample = runner.Telemetry{SampledAt: now}
			} else {
				age = max(0, now.Sub(sample.SampledAt).Milliseconds())
			}
			hb := &Heartbeat{Telemetry: sample, TelemetryAgeMs: age, Sources: a.deps.Sources.Sources(ctx), Jobs: a.held()}
			if err := s.Send(ctx, Outbound{Kind: KindHeartbeat, Heartbeat: hb}); err != nil {
				return admitted, err
			}
			heartbeat = a.deps.Clock.After(interval)
		case <-probe:
			a.deps.Sources.Probe(ctx)
			probe = a.deps.Clock.After(a.cfg.ProbeInterval)
		}
	}
}
