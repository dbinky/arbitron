// Package sources discovers and probes the runner's own capacity sources.
// Probes are read-only: they spend no tokens.
package sources

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// Spec pairs an advertised source with its read-only prober.
type Spec struct {
	Source runneragent.Source
	Prober runner.SourceProber
}

// Inventory implements runneragent.LocalSources.
type Inventory struct {
	mu      sync.Mutex
	seq     uint64
	samples []*sample
	specs   []Spec
	last    []runneragent.Source
	harness map[string]string
	now     func() time.Time
}

var _ runneragent.LocalSources = (*Inventory)(nil)

// New builds an inventory; every source starts with unknown auth.
func New(specs []Spec, harness map[string]string, now func() time.Time) *Inventory {
	last := make([]runneragent.Source, len(specs))
	for i, sp := range specs {
		last[i] = sp.Source
		last[i].Health = runner.SourceHealth{Auth: "unknown"}
	}
	h := map[string]string{}
	for k, v := range harness {
		h[k] = v
	}
	return &Inventory{specs: specs, last: last, harness: h, now: now, samples: make([]*sample, len(specs))}
}

// HarnessVersions are the detected local harness versions.
func (i *Inventory) HarnessVersions() map[string]string {
	out := make(map[string]string, len(i.harness))
	for k, v := range i.harness {
		out[k] = v
	}
	return out
}

// Sources returns the last probed view.
func (i *Inventory) Sources(ctx context.Context) []runneragent.Source {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]runneragent.Source, len(i.last))
	for idx, src := range i.last {
		out[idx] = i.withObservation(idx, src)
	}
	return out
}

// CapabilitiesFor returns independent copies of the current source evidence.
func (i *Inventory) CapabilitiesFor(kind runner.CredentialKind, credentialRef string) []runner.ExecutionCapability {
	for _, s := range i.Sources(context.Background()) {
		if s.Kind == kind && s.CredentialRef == credentialRef {
			return cloneCapabilities(s.Capabilities)
		}
	}
	return nil
}

func cloneCapabilities(in []runner.ExecutionCapability) []runner.ExecutionCapability {
	out := slices.Clone(in)
	for idx := range out {
		c := &out[idx]
		c.Families = slices.Clone(c.Families)
		c.Models = slices.Clone(c.Models)
		c.ContextWindows = slices.Clone(c.ContextWindows)
		c.Efforts = slices.Clone(c.Efforts)
		c.Tools = slices.Clone(c.Tools)
	}
	return out
}

// Probe refreshes every source with its own prober.
func (i *Inventory) Probe(ctx context.Context) []runneragent.Source {
	i.mu.Lock()
	prev := append([]runneragent.Source(nil), i.last...)
	i.mu.Unlock()
	next := make([]runneragent.Source, len(i.specs))
	for idx, sp := range i.specs {
		src := sp.Source
		old := prev[idx].Health
		at := i.now().UTC()
		h := runner.SourceHealth{Auth: old.Auth, Capacity: old.Capacity, ResetAt: old.ResetAt, Windows: old.Windows, Headroom: old.Headroom, LastProbeAt: &at}
		if h.Auth == "" {
			h.Auth = "unknown"
		}
		res, err := sp.Prober.Probe(ctx, runner.Source{Kind: src.Kind, CredentialRef: src.CredentialRef, Families: src.Families, Backends: src.Backends, Models: src.Models})
		if err != nil {
			h.Detail = "probe failed"
		} else {
			i.mu.Lock()
			i.record(idx, res)
			i.mu.Unlock()
			h.Auth = res.Auth
			if h.Auth == "" {
				h.Auth = "unknown"
			}
			if res.Capacity != nil {
				h.Capacity, h.ResetAt, h.Windows, h.Headroom = res.Capacity.State, res.Capacity.ResetAt, res.Capacity.Windows, res.Capacity.Headroom
			}
		}
		src.Health = h
		next[idx] = src
	}
	i.mu.Lock()
	i.last = next
	i.mu.Unlock()
	return i.Sources(ctx)
}

type sample struct {
	obs runner.ProbeResult
	seq uint64
	at  time.Time
}

func cloneProbe(r runner.ProbeResult) runner.ProbeResult {
	if r.Capacity != nil {
		c := *r.Capacity
		if c.OrdinaryUsageAllowed != nil {
			v := *c.OrdinaryUsageAllowed
			c.OrdinaryUsageAllowed = &v
		}
		r.Capacity = &c
	}
	return r
}

// record and withObservation require i.mu.
func (i *Inventory) record(idx int, res runner.ProbeResult) {
	i.seq++
	i.samples[idx] = &sample{obs: cloneProbe(res), seq: i.seq, at: i.now()}
}

func (i *Inventory) withObservation(idx int, s runneragent.Source) runneragent.Source {
	if sm := i.samples[idx]; sm != nil {
		o := cloneProbe(sm.obs)
		s.Observation, s.ObservationSeq = &o, sm.seq
		s.SampleAgeMs = i.now().Sub(sm.at).Milliseconds()
		if s.SampleAgeMs < 0 {
			s.SampleAgeMs = 0
		}
	}
	return s
}

var _ runneragent.FreshSourceProber = (*Inventory)(nil)

// ProbeSource probes exactly the named source without consulting cached health.
func (i *Inventory) ProbeSource(ctx context.Context, kind runner.CredentialKind, ref string) (runneragent.Source, error) {
	idx := -1
	for n, sp := range i.specs {
		if sp.Source.Kind == kind && sp.Source.CredentialRef == ref {
			idx = n
			break
		}
	}
	if idx < 0 {
		return runneragent.Source{}, runneragent.ErrUnknownSource
	}
	sp := i.specs[idx]
	res, err := sp.Prober.Probe(ctx, runner.Source{Kind: sp.Source.Kind, CredentialRef: sp.Source.CredentialRef, Families: sp.Source.Families, Backends: sp.Source.Backends, Models: sp.Source.Models})
	i.mu.Lock()
	defer i.mu.Unlock()
	src := i.last[idx]
	if err != nil {
		src.Observation = nil
		return src, err
	}
	i.record(idx, res)
	return i.withObservation(idx, src), nil
}

// ClaudeAuth probes the Claude subscription login with `claude auth status`.
// The env is the allowlisted base env, so a stray ANTHROPIC_API_KEY never masks
// the subscription.
type ClaudeAuth struct {
	Runner claudecli.CommandRunner
	Bin    string
}

var _ runner.SourceProber = ClaudeAuth{}

// Probe reports ok / logged_out from the JSON "loggedIn" field; anything else is unknown.
func (p ClaudeAuth) Probe(ctx context.Context, s runner.Source) (runner.ProbeResult, error) {
	if s.Kind != runner.CredAnthropicSubscription {
		return runner.ProbeResult{}, fmt.Errorf("sources: claude auth cannot probe %s", s.Kind)
	}
	return runnerprobe.NewClaudeAuthProber(p.Bin, p.Runner).Probe(ctx, s)
}

// LunaRouteAuth probes LunaRoute's model listing with this runner's own key.
type LunaRouteAuth struct {
	Conn    provider.ConnectionProbe
	Binding provider.ResolvedBinding
}

var _ runner.SourceProber = LunaRouteAuth{}

// Probe maps the listing outcome; congestion is an error, never an auth verdict.
func (p LunaRouteAuth) Probe(ctx context.Context, s runner.Source) (runner.ProbeResult, error) {
	if s.Kind != runner.CredLunaRoute || s.CredentialRef != p.Binding.ConnectionRef {
		return runner.ProbeResult{}, fmt.Errorf("sources: lunaroute prober for %q cannot probe %s/%s", p.Binding.ConnectionRef, s.Kind, s.CredentialRef)
	}
	_, err := p.Conn.Probe(ctx, p.Binding)
	if err == nil {
		return runner.ProbeResult{Auth: "ok"}, nil
	}
	switch o := runner.ClassifyError(err); o.Class {
	case runner.OutcomeLoggedOut:
		return runner.ProbeResult{Auth: "logged_out"}, nil
	case runner.OutcomeExhausted:
		return runner.ProbeResult{Auth: "ok", Capacity: &runner.CapacityObservation{State: "exhausted", ResetAt: o.ResetAt, Windows: o.Windows}}, nil
	default:
		return runner.ProbeResult{}, fmt.Errorf("sources: lunaroute probe: %s", o.Class)
	}
}

// DiscoverInput is what the runner knows about its local harnesses and keys.
type DiscoverInput struct {
	ClaudeBinary   string
	Runner         claudecli.CommandRunner
	LunaRoute      config.LunaRoute
	LunaRouteProbe provider.ConnectionProbe
	Rules          provider.CapabilityRules
	Confirmations  runner.ConfirmationExecutor
}

// Discover lists the sources this runner can serve. openai_subscription is
// never advertised in Phase 2 (no remote codex chat path).
func Discover(ctx context.Context, in DiscoverInput) ([]Spec, map[string]string) {
	var specs []Spec
	harness := map[string]string{}
	if bin, found := claudecli.ResolveBinary(in.ClaudeBinary); found && in.Runner != nil {
		if v, err := claudecli.ProbeVersion(ctx, in.Runner, bin); err == nil {
			harness["claude"] = v
			specs = append(specs, Spec{
				Source: runneragent.Source{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{""}},
				Prober: ClaudeAuth{Runner: in.Runner, Bin: bin},
			})
		}
	}
	if in.LunaRoute.Enabled() && in.LunaRouteProbe != nil {
		key := in.LunaRoute.APIKey
		b := provider.ResolvedBinding{
			Kind: provider.ProfileKindChat, Provider: provider.CanonicalLunaRoute, ConnectionRef: in.LunaRoute.ConnectionRef,
			RevealKey: func(context.Context) (string, error) { return key, nil },
		}
		specs = append(specs, Spec{
			Source: runneragent.Source{Kind: runner.CredLunaRoute, CredentialRef: in.LunaRoute.ConnectionRef,
				Families: append([]modelidentity.Family(nil), in.LunaRoute.Families...), Backends: []string{""}, Models: append([]string(nil), in.LunaRoute.Models...)},
			Prober: LunaRouteAuth{Conn: in.LunaRouteProbe, Binding: b},
		})
	}
	for idx := range specs {
		src := &specs[idx].Source
		src.Capabilities, src.UnavailableReason = BuildCapabilities(src.Kind, harness["claude"], in)
	}
	return specs, harness
}
