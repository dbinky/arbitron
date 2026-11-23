// Package sources discovers and probes the runner's own capacity sources.
// Probes are read-only: they spend no tokens.
package sources

import (
	"context"
	"fmt"
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
	return &Inventory{specs: specs, last: last, harness: h, now: now}
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
	return append([]runneragent.Source(nil), i.last...)
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
	return append([]runneragent.Source(nil), next...)
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
	return specs, harness
}
