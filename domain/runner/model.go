// Package runner is the Remote Runners pool domain: runners, capacity sources,
// placement, the job ledger ports and the step dispatcher. It never imports
// adapter packages.
package runner

import (
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// RunnerID names a runner. The server's own runner is BuiltInRunnerID.
type RunnerID string

// BuiltInRunnerID is the runner inside the Arbitron server process.
const BuiltInRunnerID RunnerID = "builtin"

// Liveness is the connection state of a runner.
type Liveness string

const (
	LivenessOnline  Liveness = "online"
	LivenessSuspect Liveness = "suspect"
	LivenessLost    Liveness = "lost"
	LivenessOffline Liveness = "offline"
)

// CompatState is the result of the version/protocol compatibility check.
type CompatState string

const (
	CompatCompatible   CompatState = "compatible"
	CompatIncompatible CompatState = "incompatible"
	CompatChatOnly     CompatState = "chat_only" // accepts only hermetic (AffinityNone) work
)

// Compatibility is a runner's compatibility state and its operator-readable reason.
type Compatibility struct {
	State  CompatState
	Reason string
}

// Runner is one machine in the pool.
type Runner struct {
	ID              RunnerID
	Name            string
	BuiltIn         bool
	StepLimit       int
	Labels          map[string]string
	Cordoned        bool
	Draining        bool
	Compat          Compatibility
	Liveness        Liveness
	LastContact     *time.Time
	OS              string
	RunnerVersion   string
	HarnessVersions map[string]string
	WorkspaceRoot   string
	CreatedAt       time.Time
}

// CredentialKind is how a capacity source authenticates to its provider.
type CredentialKind string

const (
	CredAnthropicSubscription CredentialKind = "anthropic_subscription"
	CredOpenAISubscription    CredentialKind = "openai_subscription"
	CredLunaRoute             CredentialKind = "lunaroute"
	CredServerConnection      CredentialKind = "server_connection" // built-in only
	CredLocalHarness          CredentialKind = "local_harness"     // pi/opencode no-connection paths; built-in only (RR-02 decision)
	CredNone                  CredentialKind = "none"              // process steps: slot only
)

// SourceID identifies a capacity source: "<runner>/<kind>/<ref>".
type SourceID string

// MakeSourceID builds the source identity (runner, credential kind, credential
// reference). An empty (or blank) reference renders as "-". Two connections
// therefore never share an ID — and so never share health.
func MakeSourceID(r RunnerID, k CredentialKind, ref string) SourceID {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "-"
	}
	return SourceID(string(r) + "/" + string(k) + "/" + ref)
}

// AuthState is a source's authentication observation.
type AuthState string

const (
	AuthUnknown   AuthState = "unknown"
	AuthOK        AuthState = "ok"
	AuthLoggedOut AuthState = "logged_out"
)

// CapacityState is a source's capacity observation, separate from auth.
type CapacityState string

const (
	CapacityAvailable CapacityState = "available"
	CapacityExhausted CapacityState = "exhausted"
	CapacityProbation CapacityState = "probation"
)

// SourceHealth is the health of one capacity source. Auth and capacity are
// separate observations: logged out blocks eligibility without erasing a known
// exhaustion/reset.
type SourceHealth struct {
	Revision          uint64 // incremented by serialized health transitions
	ConfirmationModel string // exact exhausted model used by confirmation probe
	Auth              AuthState
	Capacity          CapacityState
	ResetAt           *time.Time // nil = unknown reset (only meaningful when exhausted)
	Headroom          *float64   // 0..1 remaining; nil = unknown (NEVER treated as 0 or 1)
	Windows           []LimitWindow
	Congested         bool
	CongestedUntil    *time.Time // transient pressure, never exhaustion
	LastProbeAt       *time.Time
	LastOutcome       OutcomeClass
	Detail            string // class/reset only, never provider prose
}

// ExecutionCapability records proven execution support for a source.
// Phase 2 populates these from actual local capability/version evidence.
type ExecutionCapability struct {
	Op                                     OpType
	Backend, Provider, EndpointFingerprint string
	Families                               []modelidentity.Family
	Models                                 []string
	ContextWindows                         []provider.ContextWindow
	Efforts                                []provider.Effort
	Tools                                  []string
	ExecutedModel                          bool
}

// Source is a capacity source on a runner.
type Source struct {
	ID                SourceID
	RunnerID          RunnerID
	Kind              CredentialKind
	CredentialRef     string
	UnavailableReason string                 // missing/unsupported harness; independent of mutable health
	Families          []modelidentity.Family // advertised served families; empty = none proven
	Backends          []string               // e.g. "claude-code","codex"; chat paths use ""
	Models            []string               // optional exact-model allowlist; empty = any model of the family
	Capabilities      []ExecutionCapability  // remote empty => ineligible; P2 produces facts
	Health            SourceHealth
}

// OpType is the kind of dispatched operation.
type OpType string

const (
	OpChat    OpType = "chat"
	OpStream  OpType = "stream"
	OpAgent   OpType = "agent"
	OpProcess OpType = "process"
)

// Affinity says where an operation's state lives and therefore where it may run.
type Affinity string

const (
	AffinityNone      Affinity = "none"      // hermetic chat (Phase 2+)
	AffinityBuiltIn   Affinity = "builtin"   // Phase 1 everything; server-local staging
	AffinityWorkspace Affinity = "workspace" // Phase 3
	AffinitySession   Affinity = "session"   // Phase 4
)

// ExecRequirements is the complete resolved request a placement must honour.
// Group is only a capacity grouping key; eligibility also enforces Model,
// Backend and Tools exactly.
type ExecRequirements struct {
	Provider             string
	EndpointFingerprint  string
	ContextWindow        provider.ContextWindow
	Effort               provider.Effort
	Group                ModelGroup
	Model                string
	Backend              string
	Op                   OpType
	Affinity             Affinity
	RunID                uuid.UUID
	WorkspaceBytes       int64
	PinnedRunner         RunnerID // Phase 4 session pin; "" = none
	RequireExecutedModel bool
	Tools                []string

	RequiredSource SourceID // synthetic OperationProbe only; exact credential source
}
