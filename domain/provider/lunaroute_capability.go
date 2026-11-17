package provider

import (
	"sort"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Support is one independent capability fact.
type Support string

const (
	SupportUnknown     Support = "unknown"
	SupportSupported   Support = "supported"
	SupportUnsupported Support = "unsupported"
)

// SupportFact is a support verdict plus the evidence justifying a known verdict.
// A zero value reads as unknown.
type SupportFact struct {
	Support  Support
	Evidence string
}

// LimitFact is a token limit; Tokens == 0 means unknown.
type LimitFact struct {
	Tokens   int
	Evidence string
}

// Known reports whether the limit is a recorded fact.
func (l LimitFact) Known() bool { return l.Tokens > 0 }

// EffortFact lists non-default effort levels supported on one axis.
// Known=true with no Levels means known: none; Known=false means unknown.
type EffortFact struct {
	Known    bool
	Levels   []Effort
	Evidence string
}

// LunaRouteChatAxis keys the chat-task effort fact.
const LunaRouteChatAxis = "chat"

// LunaRouteBackends is the agent-backend vocabulary the table may describe.
var LunaRouteBackends = []string{workflow.BackendClaudeCode, workflow.BackendOpencode, workflow.BackendCodex, workflow.BackendPi}

// LunaRouteModel records curated facts about one exact executable id.
// Absent facts are unknown; success on one axis does not prove another.
type LunaRouteModel struct {
	ID              string
	Tasks           map[ProfileKind]SupportFact
	Backends        map[string]SupportFact
	ContextTokens   LimitFact
	OutputTokens    LimitFact
	Efforts         map[string]EffortFact
	Reasoning       SupportFact // independent of effort support
	RequestSettings map[string]SupportFact
}

func normalized(f SupportFact, ok bool) SupportFact {
	if !ok || f.Support == "" {
		return SupportFact{Support: SupportUnknown}
	}
	return f
}

// Task returns the task fact, or unknown when absent.
func (m LunaRouteModel) Task(kind ProfileKind) SupportFact {
	f, ok := m.Tasks[kind]
	return normalized(f, ok)
}

// Backend returns the backend fact, or unknown when absent.
func (m LunaRouteModel) Backend(backend string) SupportFact {
	f, ok := m.Backends[backend]
	return normalized(f, ok)
}

// Effort returns the effort fact for chat or an agent backend.
func (m LunaRouteModel) Effort(axis string) EffortFact { return m.Efforts[axis] }

// LunaRouteCapabilities is the domain read port over the curated table.
type LunaRouteCapabilities interface {
	LunaRouteModel(id string) (LunaRouteModel, bool)
}

var _ LunaRouteCapabilities = CapabilityRules{}

// WithLunaRouteModels returns a copy carrying the table. Like NewCapabilityRules,
// it takes the map as-is; callers must not mutate it afterwards.
func (r CapabilityRules) WithLunaRouteModels(models map[string]LunaRouteModel) CapabilityRules {
	r.lunaroute = models
	return r
}

// LunaRouteModel looks up one exact model id. Returned facts are read-only.
func (r CapabilityRules) LunaRouteModel(id string) (LunaRouteModel, bool) {
	m, ok := r.lunaroute[id]
	return m, ok
}

// LunaRouteModels returns a new slice sorted by id. Nested facts are read-only.
func (r CapabilityRules) LunaRouteModels() []LunaRouteModel {
	out := make([]LunaRouteModel, 0, len(r.lunaroute))
	for _, m := range r.lunaroute {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
