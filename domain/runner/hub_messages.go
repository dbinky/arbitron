// domain/runner/hub_messages.go
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
)

// Message kinds carried by Inbound/Outbound; identical strings to the
// runnerproto Kind* constants (contracts §15). adapter/http/runner_connect.go
// translates between the two.
const (
	MsgHello     = "hello"
	MsgHeartbeat = "heartbeat"
	MsgAccept    = "accept"
	MsgReject    = "reject"
	MsgOutput    = "output"
	MsgResult    = "result"
	MsgDispatch  = "dispatch"
	MsgCancel    = "cancel"
	MsgAck       = "ack"
	MsgFenced    = "fenced"
	MsgAdmitted  = "admitted"
)

// WebSocket close codes (private 4000-4999 range) used by the endpoint and hub.
const (
	CloseBadKey       = 4001
	CloseWrongMachine = 4002
	CloseMalformed    = 4003
	CloseRevoked      = 4004
	CloseIncompatible = 4005
	CloseSuperseded   = 4006
	CloseLost         = 4007
)

// Held-job states reported by runners in hello and heartbeats.
const (
	HeldRunning  = "running"
	HeldTerminal = "terminal"
)

// Pool event kinds emitted by the hub (contracts §9).
const (
	evtRunnerConnected    = "runner.connected"
	evtRunnerSuspect      = "runner.suspect"
	evtRunnerLost         = "runner.lost"
	evtRunnerReconnected  = "runner.reconnected"
	evtRunnerIncompatible = "runner.incompatible"
	evtRunnerKeyRevoked   = "runner.key_revoked"
	evtSourceRecovered    = "source.recovered"
)

var (
	ErrRunnerRevoked       = errors.New("runner: runner key revoked")
	ErrRemoteOpUnsupported = errors.New("runner: operation is not supported on remote runners")
	ErrRemoteCredential    = errors.New("runner: credential kind cannot execute on a remote runner")
	ErrOfferExpired        = errors.New("runner: remote runner did not accept the offer in time")
	ErrJobRejected         = errors.New("runner: remote runner rejected the job")
	ErrSessionClosed       = errors.New("runner: runner session superseded or closed")
	ErrMalformedMessage    = errors.New("runner: malformed runner message")
	ErrRemoteStepFailed    = errors.New("runner: remote step failed")
)

// IncompatibleError is returned by Admit for an incompatible hello. The
// endpoint sends a refused frame carrying Compat.Reason and closes with
// CloseIncompatible; the runner stays visible in the pool.
type IncompatibleError struct{ Compat Compatibility }

func (e *IncompatibleError) Error() string { return "runner: incompatible runner: " + e.Compat.Reason }

// HelloInfo is the domain mirror of runnerproto.Hello.
type HelloInfo struct {
	OS, Arch        string
	ProtocolVersion int
	RunnerVersion   string
	HarnessVersions map[string]string
	WorkspaceRoot   string
	ScratchRoot     string
	Capabilities    []string
	Sources         []SourceReport
	Held            []HeldJobInfo
	PullReceipts    []PullReceiptPayload
}

// SourceReport is one credential source a runner advertises.
type SourceReport struct {
	Observation       *ProbeResult // Fresh sample; Health is only a projection.
	ObservationSeq    uint64       // Monotonic per runner process.
	SampleAgeMs       int64        // Monotonic sample age at send time.
	UnavailableReason string
	Capabilities      []ExecutionCapability
	Kind              CredentialKind
	CredentialRef     string
	Families          []modelidentity.Family
	Backends          []string
	Models            []string
	Health            SourceHealth
}

// HeldJobInfo is a job the runner still holds (running or with an unacked result).
type HeldJobInfo struct {
	JobID         uuid.UUID
	Generation    int64
	State         string
	LastOutputSeq uint64
}

// HeartbeatInfo is the domain mirror of runnerproto.Heartbeat. A nil Sources
// slice means "unchanged"; an empty non-nil slice means "no sources".
type HeartbeatInfo struct {
	TelemetryAgeMs int64
	Telemetry      Telemetry
	Sources        []SourceReport
	Jobs           []HeldJobInfo
}

// OutputFrame is one streamed token with its per-(job,generation) sequence.
type OutputFrame struct {
	Seq   uint64
	Token string
}

// RemoteChatResult is the chat payload of a successful remote result.
type RemoteChatResult struct {
	Output       string
	Conversation json.RawMessage
	Model        string
	TokensIn     int
	TokensOut    int
	SessionID    string
}

// RemoteResult is a terminal job result. ErrorText is already sanitized by the runner.
type RemoteResult struct {
	Process      *engine.ProcessResult
	Agent        *engine.AgentResult
	Confirmation *ConfirmationResult
	OK           bool
	Chat         *RemoteChatResult
	Outcome      SourceOutcome
	ErrorText    string
}

// ChatDispatch is the hermetic chat request sent to a runner. SECURITY: it has
// no WorkDir, BaseURL, ConnectionRef or key material; the runner uses its own
// credentials for CredentialKind.
type ChatDispatch struct {
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
	CredentialKind                CredentialKind
	Backend                       string
	ContextWindow                 string
	Effort                        string
	Tools                         []string
}

// ConfirmationDispatch is the hermetic native exact-model descriptor.
type ConfirmationDispatch struct {
	Model, Backend, Provider, EndpointFingerprint, ContextWindow, Effort string
}

// DispatchPayload is an offer of one job generation.
type DispatchPayload struct {
	Process      *ProcessDispatch
	Agent        *AgentDispatch
	Workspace    *WorkspaceDispatch
	Confirmation *ConfirmationDispatch
	Op           OpType
	Chat         *ChatDispatch
	LeaseUntil   time.Time
}

// AckPayload acknowledges output through Seq, or the terminal result.
type AckPayload struct {
	Seq    uint64
	Result bool
}

// Outbound is a hub -> runner message.
// AdmittedPayload carries the exact subscription models to verify natively.
type AdmittedPayload struct{ ClaudeModels []string }

type Outbound struct {
	PullWorkspace  *PullWorkspacePayload
	PullReceiptAck string
	Admitted       *AdmittedPayload
	Probe          *ProbePayload
	Kind           string
	JobID          uuid.UUID
	Generation     int64
	Dispatch       *DispatchPayload
	Ack            *AckPayload
}

// Inbound is a runner -> hub message after admission.
type Inbound struct {
	PullReceipt  *PullReceiptPayload
	ProbeResult  *ProbeReply
	Kind         string
	JobID        uuid.UUID
	Generation   int64
	Seq          uint64
	Hello        *HelloInfo
	Heartbeat    *HeartbeatInfo
	Output       *OutputFrame
	Result       *RemoteResult
	Accept       bool
	RejectReason string
	RejectCode   string
}

// RunnerConn is the hub's view of one runner connection. Send must be safe
// for concurrent use; Close is idempotent.
type RunnerConn interface {
	Send(ctx context.Context, m Outbound) error
	Close(code int, reason string) error
}

// Session is one admitted connection. Only the newest session of a runner is current.
type Session struct {
	RunnerID RunnerID
	KeyID    uuid.UUID
	conn     RunnerConn
	serial   uint64
}

// HubConfig holds the hub's timing and admission settings.
type HubConfig struct {
	ProbeTimeout        time.Duration // Zero uses DefaultRemoteProbeTimeout.
	Heartbeat           time.Duration
	SuspectAfter        time.Duration
	LostAfter           time.Duration
	OfferTTL            time.Duration
	TelemetryWindow     int
	ServerWorkspaceRoot string
	Compat              CompatTable
}
