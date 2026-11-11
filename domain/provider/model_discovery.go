package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/arbitron/arbitron/domain/modelidentity"
)

// DiscoveryNote is returned with every discovery view.
const DiscoveryNote = "Availability does not imply task or backend compatibility. Fields marked \"unknown\" are not guarantees; known facts cite their evidence. " +
	"Choose the model and backend explicitly: nothing is recommended or auto-selected. LunaRoute agent profiles must name their backend and use pinned selection. " +
	"family is the model's actual vendor (aliases included); keep doer and judge on different vendors, and an \"unresolved\" family is refused."

// FactView is one support fact: supported, unsupported or unknown.
type FactView struct {
	Support  string `json:"support"`
	Evidence string `json:"evidence,omitempty"`
}

// LimitView is a token limit; Known=false means unknown.
type LimitView struct {
	Known    bool   `json:"known"`
	Tokens   int    `json:"tokens,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// EffortView lists non-default efforts known to be supported on one axis.
type EffortView struct {
	Known    bool     `json:"known"`
	Levels   []string `json:"levels"`
	Evidence string   `json:"evidence,omitempty"`
}

// DiscoveredModel is one listed model with its known/unknown facts.
type DiscoveredModel struct {
	ID              string                `json:"id"`
	Family          string                `json:"family"`
	Tasks           map[string]FactView   `json:"tasks"`
	Backends        map[string]FactView   `json:"backends"`
	ContextTokens   LimitView             `json:"context_tokens"`
	OutputTokens    LimitView             `json:"output_tokens"`
	Efforts         map[string]EffortView `json:"efforts"`
	Reasoning       FactView              `json:"reasoning"`
	RequestSettings map[string]FactView   `json:"request_settings"`
}

// DiscoveryView is the per-connection discovery result.
type DiscoveryView struct {
	Connection       string            `json:"connection"`
	Provider         string            `json:"provider"`
	ListingSupported bool              `json:"listing_supported"`
	Note             string            `json:"note"`
	Models           []DiscoveredModel `json:"models"`
}

// ModelDiscoveryService combines a connection's listing with curated facts.
// Like ConnectionService, it is admin-only. It never persists or exposes keys.
type ModelDiscoveryService struct {
	store  ConnectionStore
	probes ProbeSelector
	caps   LunaRouteCapabilities
}

// NewModelDiscoveryService wires the three ports. Nil ports fail discovery
// with ErrSelectionPortsMissing, never an empty successful catalog.
func NewModelDiscoveryService(store ConnectionStore, probes ProbeSelector, caps LunaRouteCapabilities) *ModelDiscoveryService {
	return &ModelDiscoveryService{store: store, probes: probes, caps: caps}
}

func factView(f SupportFact) FactView {
	if f.Support == "" {
		return FactView{Support: string(SupportUnknown)}
	}
	return FactView{Support: string(f.Support), Evidence: f.Evidence}
}

func limitView(l LimitFact) LimitView {
	if !l.Known() {
		return LimitView{}
	}
	return LimitView{Known: true, Tokens: l.Tokens, Evidence: l.Evidence}
}

func effortView(e EffortFact) EffortView {
	levels := make([]string, 0, len(e.Levels))
	if e.Known {
		for _, level := range e.Levels {
			levels = append(levels, string(level))
		}
		return EffortView{Known: true, Levels: levels, Evidence: e.Evidence}
	}
	return EffortView{Levels: levels}
}

// Discover lists the named connection's models, preserving store, guard and
// probe errors. Listing membership never supplies a capability fact.
func (s *ModelDiscoveryService) Discover(ctx context.Context, name string) (DiscoveryView, error) {
	if err := requireAdmin(ctx); err != nil {
		return DiscoveryView{}, err
	}
	if s.store == nil || s.probes == nil || s.caps == nil {
		return DiscoveryView{}, fmt.Errorf("%w: model discovery", ErrSelectionPortsMissing)
	}
	conn, err := s.store.Get(ctx, name)
	if err != nil {
		return DiscoveryView{}, err
	}
	store := s.store
	binding := GuardLunaRouteReveal(ResolvedBinding{
		Provider: conn.Provider, BaseURL: conn.BaseURL, Extras: conn.Extras, ConnectionRef: name,
		RevealKey: func(ctx context.Context) (string, error) { return store.Reveal(ctx, name) },
	})
	view := DiscoveryView{Connection: name, Provider: conn.Provider, ListingSupported: true, Note: DiscoveryNote, Models: []DiscoveredModel{}}
	probe, err := s.probes.ProbeFor(binding)
	if errors.Is(err, ErrNoProbeForProvider) {
		view.ListingSupported = false
		return view, nil
	}
	if err != nil {
		return DiscoveryView{}, err
	}
	if probe == nil {
		return DiscoveryView{}, fmt.Errorf("%w: nil discovery probe", ErrSelectionPortsMissing)
	}
	result, err := probe.Probe(ctx, binding)
	if err != nil {
		return DiscoveryView{}, err
	}
	if !result.Reachable {
		return DiscoveryView{}, ClassifyLunaRoute(LunaRouteOutcome{
			Operation: LunaRouteListModels, Malformed: errors.New("probe returned no reachable result"),
		})
	}
	if result.ModelsUnsupported {
		view.ListingSupported = false
		return view, nil
	}

	route := Canonicalize(conn.Provider)
	curated := IsLunaRoute(conn.Provider)
	seen := make(map[string]bool, len(result.Models))
	ids := make([]string, 0, len(result.Models))
	for _, id := range result.Models {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		var model LunaRouteModel
		if curated {
			model, _ = s.caps.LunaRouteModel(id)
		}
		discovered := DiscoveredModel{
			ID: id, Family: string(modelidentity.ClassifyFamily(route, id)),
			Tasks: map[string]FactView{
				string(ProfileKindChat):  factView(model.Task(ProfileKindChat)),
				string(ProfileKindAgent): factView(model.Task(ProfileKindAgent)),
			},
			Backends:      make(map[string]FactView, len(LunaRouteBackends)),
			ContextTokens: limitView(model.ContextTokens),
			OutputTokens:  limitView(model.OutputTokens),
			Efforts:       map[string]EffortView{LunaRouteChatAxis: effortView(model.Effort(LunaRouteChatAxis))},
			Reasoning:     factView(model.Reasoning),
			RequestSettings: map[string]FactView{
				"temperature":   factView(model.RequestSettings["temperature"]),
				"output_schema": factView(model.RequestSettings["output_schema"]),
			},
		}
		for _, backend := range LunaRouteBackends {
			discovered.Backends[backend] = factView(model.Backend(backend))
			discovered.Efforts[backend] = effortView(model.Effort(backend))
		}
		view.Models = append(view.Models, discovered)
	}
	return view, nil
}
