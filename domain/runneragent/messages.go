// Package runneragent is the runner binary's domain: admission, reconnect,
// heartbeats, job execution bookkeeping, output sequencing and replay. It is
// transport-neutral; adapter/runneragent/wsclient translates these mirrors to
// adapter/runnerproto envelopes.
package runneragent

import (
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
)

// Message kinds. The strings equal adapter/runnerproto's Kind* constants.
const (
	KindHello             = "hello"
	KindHeartbeat         = "heartbeat"
	KindAccept            = "accept"
	KindReject            = "reject"
	KindOutput            = "output"
	KindResult            = "result"
	KindDispatch          = "dispatch"
	KindCancel            = "cancel"
	KindProbe             = "probe"
	KindProbeResult       = "probe_result"
	KindAck               = "ack"
	KindFenced            = "fenced"
	KindChallenge         = "challenge"
	KindChallengeResponse = "challenge_response"
	KindAdmitted          = "admitted"
	KindRefused           = "refused"
)

// Operations Phase 2 runners execute, including native confirmation probes.
const (
	OpChat    = "chat"
	OpStream  = "stream"
	OpConfirm = "confirm"
)

// Held job states reported in hello and heartbeats.
const (
	HeldRunning  = "running"
	HeldTerminal = "terminal"
)

// JobRef identifies one assignment: a job at one generation.
type JobRef struct {
	JobID      string
	Generation int64
}

// Source is one locally usable capacity source. Vendor credentials never
// appear here: only kind, reference names and health.
type Source struct {
	Observation       *runner.ProbeResult
	ObservationSeq    uint64
	SampleAgeMs       int64
	UnavailableReason string
	Capabilities      []runner.ExecutionCapability
	Kind              runner.CredentialKind
	CredentialRef     string
	Families          []modelidentity.Family
	Backends          []string
	Models            []string
	Health            runner.SourceHealth
}

// HeldJob is a job the runner still owns: running, or terminal and not yet
// acknowledged by the server.
type HeldJob struct {
	JobID         string
	Generation    int64
	State         string
	LastOutputSeq uint64
}

// Hello is the admission metadata sent after the challenge response.
type Hello struct {
	OS              string
	Arch            string
	ProtocolVersion int
	RunnerVersion   string
	HarnessVersions map[string]string
	WorkspaceRoot   string
	ScratchRoot     string
	Capabilities    []string
	Sources         []Source
	Held            []HeldJob
}

// Heartbeat carries liveness, telemetry, source health and held jobs.
type Heartbeat struct {
	TelemetryAgeMs int64
	Telemetry      runner.Telemetry
	Sources        []Source
	Jobs           []HeldJob
}

// ChatJob is the allowlisted chat descriptor (no server credentials).
type ChatJob struct {
	Provider, EndpointFingerprint string
	Model                         string
	SystemPrompt                  string
	Prompt                        string
	MaxTokens                     int
	Temperature                   float64
	OutputSchema                  json.RawMessage
	SessionID                     string
	RequireExecutedModel          bool
	Stream                        bool
	CredentialKind                string
	Backend                       string
	ContextWindow                 string
	Effort                        string
	Tools                         []string
}

// ConfirmationJob describes a native confirmation using the runner's own login.
// It contains no prompt, credentials, path or environment.
type ConfirmationJob struct {
	Model, Backend, Provider, EndpointFingerprint, ContextWindow, Effort string
}

// Dispatch is one job offer.
type Dispatch struct {
	Confirmation *ConfirmationJob
	JobID        string
	Generation   int64
	Op           string
	Chat         *ChatJob
	LeaseUntil   time.Time
}

// OutputFrame is one streamed token, numbered per (job, generation) from 1.
type OutputFrame struct {
	Seq   uint64
	Token string
}

// ChatResult is a completed chat.
type ChatResult struct {
	Output    string
	Model     string
	SessionID string
	TokensIn  int
	TokensOut int
}

// Result is a job's terminal result with its classified outcome. ErrorText is
// already sanitized by the executor (no provider prose, no secrets).
type Result struct {
	Confirmation *runner.ConfirmationResult
	OK           bool
	Chat         *ChatResult
	Outcome      runner.SourceOutcome
	ErrorText    string
}

// Challenge is the server's machine-binding challenge.
type Challenge struct {
	KeyID string
	Nonce []byte
}

// ChallengeResponse proves possession of the machine key.
type ChallengeResponse struct {
	PublicKey ed25519.PublicKey
	Signature []byte
}

// Refusal is a typed admission refusal.
type Refusal struct {
	Code   string
	Reason string
}

// Admission confirms the session (mirrors runnerproto.Admitted). A positive
// HeartbeatSeconds overrides Config.Heartbeat for that session.
type Admission struct {
	RunnerID         string
	HeartbeatSeconds int
}

// ProbeRequest asks for a fresh read-only probe of one source (mirrors
// runnerproto.Probe).
type ProbeRequest struct {
	RequestID      string
	HealthRevision uint64
	SourceKind     runner.CredentialKind
	CredentialRef  string
}

// Inbound is a server-to-runner message.
type Inbound struct {
	Kind       string
	JobID      string
	Generation int64
	Seq        uint64
	Challenge  *Challenge
	Dispatch   *Dispatch
	AckResult  bool
	Refused    *Refusal
	Admitted   *Admission
	Probe      *ProbeRequest
}

// Outbound is a runner-to-server message.
type Outbound struct {
	RequestID         string
	HealthRevision    uint64
	Kind              string
	JobID             string
	Generation        int64
	Seq               uint64
	ChallengeResponse *ChallengeResponse
	Hello             *Hello
	Heartbeat         *Heartbeat
	Output            *OutputFrame
	Result            *Result
	RejectReason      string
	RejectCode        string
	ProbeResult       *Source
}
