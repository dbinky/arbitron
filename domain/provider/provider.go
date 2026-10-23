// Package provider defines the slim profile registry: the alias indirection
// and capability contract that node kinds resolve a backend through. Phase 2
// adds ConnectionRef; Phase 1 is alias + kind + model only.
package provider

import (
	"context"
	"errors"
)

// ProfileKind is the capability contract a profile selects: a no-tool chat
// backend or a tool-using agent backend.
type ProfileKind string

const (
	// ProfileKindChat selects a ChatProvider backend (reasoning, no tools).
	ProfileKindChat ProfileKind = "chat"
	// ProfileKindAgent selects an AgentProvider backend (agentic, tools).
	ProfileKindAgent ProfileKind = "agent"
)

// SelectionMode determines whether discovery may advance a profile's model.
type SelectionMode string

const (
	SelectionPinned    SelectionMode = "pinned"
	SelectionFollowing SelectionMode = "following"
)

// FollowTarget identifies a stable catalog provider and capability tier,
// independent of connection routing, backend and execution axes.
type FollowTarget struct {
	Provider string
	Tier     string
}

// Profile is the registry entry an alias maps to. Phase 1 carried only
// Alias/Kind/Model; Phase 2 widens it with ConnectionRef (the connection that
// supplies the endpoint + key; empty means the claude-cli no-key subscription
// path).
type Profile struct {
	Alias         string
	SelectionMode SelectionMode
	FollowTarget  *FollowTarget
	// Revision is assigned by persistence, starting at 1, on every write.
	Revision      int64
	Kind          ProfileKind // chat | agent (unchanged from P1)
	ConnectionRef string      // -> Connection.Name; empty = claude-cli no-key (subscription)
	Model         string      // model-id, Azure deployment name, or provider/model for opencode
	// Backend selects the agent CLI (claude-code | opencode), INDEPENDENT of the
	// provider. Empty inherits (node → profile → deployment default). Set only
	// for agent-kind profiles; ignored for chat. The allowed vocabulary lives in
	// domain/workflow (Backend* constants).
	Backend string
	// AllowedTools is the per-profile tool allowlist for agent-kind profiles
	// (PR #18 item 1b). It flows into ResolvedBinding.AllowedTools and is the
	// per-request tool scope the agent adapter prefers over its global scope.
	// Empty for chat-kind profiles (tool-suppressed) and for agent profiles
	// that intentionally inherit the adapter's global allowlist.
	AllowedTools []string
	// ContextWindow is the per-profile context-window axis (Phase 2). The empty
	// string is the "unset/default" sentinel that emits nothing; a non-default
	// value is validated against the CapabilityRules before it is persisted
	// (the store stays a dumb persistence port).
	ContextWindow ContextWindow
	// Effort is the per-profile reasoning-effort axis (Phase 2). Empty == default
	// (no-op); validated against the CapabilityRules like ContextWindow.
	Effort Effort
}

// EffectiveBackend returns the backend a profile is VALIDATED and SEEDED against:
// the capability ruleset is keyed on this value at write time. An explicit
// Backend wins; an empty Backend with no ConnectionRef defaults to the
// "claude-cli" no-key subscription path; an empty Backend WITH a ConnectionRef is
// left empty (the connection supplies the routing). It is the single source of
// truth shared by ProfileService.write and the startup SeedProfiles path so the
// two never drift.
//
// NOTE on the adapter-side fallback: the claude-cli adapters default an empty
// binding Backend to "claude-code" at EMIT time (adapter/claudecli/shared.go).
// That fallback is only reachable when the resolver left ResolvedBinding.Backend
// empty — i.e. the chat/subscription path, where claude-cli and claude-code emit
// identically. On the agent path the executor overwrites ResolvedBinding.Backend
// with the effective node>profile>deployment value before dispatch, so the
// emitter never sees an empty Backend for a connection-backed profile. The
// adapter fallback answering a DIFFERENT question (no resolved backend at all) is
// why it is not folded into this function.
func EffectiveBackend(p Profile) string {
	if p.Backend == "" && p.ConnectionRef == "" {
		return "claude-cli"
	}
	return p.Backend
}

// ResolvedBinding is the fully resolved backend selection for an alias. It is
// the seam Phase 3 adapters consume. RevealKey is lazy: it decrypts the
// connection's key only when invoked, at call/launch time.
type ResolvedBinding struct {
	Kind     ProfileKind
	Provider string            // "claude-cli" for the no-connection subscription path
	BaseURL  string            //
	Extras   map[string]string //
	Model    string            //
	// Backend is the resolved agent backend axis (claude-code | opencode |
	// "" = inherit). The resolver copies Profile.Backend here verbatim; the agent
	// executor then computes the EFFECTIVE backend (node > profile > deployment
	// default) and overwrites this field before the factory dispatches on it.
	// Independent of Provider: a claude-code backend may carry any connection.
	Backend string
	// AllowedTools is the resolved per-profile tool allowlist (PR #18 item 1b).
	// Adapters prefer it over their global scope as the per-request tool scope
	// for agent-kind bindings. Empty means "use the adapter's global scope".
	AllowedTools []string
	// ContextWindow is copied verbatim from the resolved Profile by the resolver
	// (Phase 2). It is consumed by the Phase-3 emission path; an empty value is
	// the default sentinel that emits nothing.
	ContextWindow ContextWindow
	// Effort is copied verbatim from the resolved Profile by the resolver
	// (Phase 2), consumed by Phase-3 emission. Empty == default (no-op).
	Effort Effort
	// Pinned is true when Model was replaced by the run's trigger-time pin
	// (PinningResolver). Strict pins require exact execution evidence; native
	// D8 pins instead retain the configured deployment/model ID.
	Pinned bool
	// OpaquePinRoute is the persisted native D8 family, set only by
	// PinningResolver. Empty means strict exact-model enforcement.
	OpaquePinRoute string
	// ConnectionRef is the profile's live connection_ref ("" on the no-key
	// subscription path). PinningResolver compares it with a D8 pin's
	// trigger-time connection.
	ConnectionRef string
	RevealKey     func(ctx context.Context) (string, error) // nil-safe; no-key path returns ""
}

// RequiresModelEvidence distinguishes strict version pins from native D8
// configured-ID pins. It never grants an exception from live routing alone.
func (b ResolvedBinding) RequiresModelEvidence() bool {
	return b.Pinned && (b.Model == "" || b.OpaquePinRoute != Canonicalize(b.Provider) ||
		(b.OpaquePinRoute != CanonicalAzure && b.OpaquePinRoute != CanonicalBedrock))
}

// ProfileResolver maps a node's alias to a fully-resolved ResolvedBinding,
// running the alias -> profile -> connection -> binding chain. Driven port; the
// engine and workflow validation depend on it, adapters implement it.
//
// Resolve takes a context.Context because the connection-backed path performs
// I/O (a ConnectionStore lookup): a request-scoped ctx lets a cancelled run
// abort the lookup. The executors already hold a ctx; thread it through.
type ProfileResolver interface {
	Resolve(ctx context.Context, alias string) (ResolvedBinding, error)
}

// ErrUnknownProfile is returned by resolvers when an alias has no registry
// entry. Validation and executors match it with errors.Is.
var ErrUnknownProfile = errors.New("provider: unknown profile alias")
