// adapter/runnerproto/payloads.go
package runnerproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Bounds keep a single admission/heartbeat frame far below MaxFrameBytes.
const (
	maxSources      = 64
	maxHeld         = 4096
	maxCapabilities = 32
	maxHarnesses    = 16
	maxWindows      = 32
	maxTools        = 256
	maxErrorText    = 4096
	nonceBytes      = 32
)

// remoteCredentialKinds are the only credential kinds a remote runner may
// advertise or be dispatched (server_connection/local_harness are built-in only).
var remoteCredentialKinds = map[string]bool{
	"anthropic_subscription": true, "openai_subscription": true, "lunaroute": true,
}

var outcomeClasses = map[string]bool{
	"": true, "exhausted": true, "logged_out": true, "transient": true, "runner_lost": true, "other": true,
}

var authStates = map[string]bool{"": true, "unknown": true, "ok": true, "logged_out": true}
var capacityStates = map[string]bool{"": true, "available": true, "exhausted": true, "probation": true}

// Hello is the runner's admission metadata (minimal admission envelope).
type Hello struct {
	OS              string            `json:"os"`
	Arch            string            `json:"arch"`
	ProtocolVersion int               `json:"protocolVersion"`
	RunnerVersion   string            `json:"runnerVersion"`
	HarnessVersions map[string]string `json:"harnessVersions,omitempty"`
	WorkspaceRoot   string            `json:"workspaceRoot"`
	ScratchRoot     string            `json:"scratchRoot"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	Sources         []WireSource      `json:"sources,omitempty"`
	Held            []HeldJob         `json:"held,omitempty"`
}

func (h *Hello) Validate() error {
	switch {
	case h.OS == "":
		return errors.New("os is required")
	case h.ProtocolVersion < 1:
		return errors.New("protocolVersion must be >= 1")
	case h.RunnerVersion == "":
		return errors.New("runnerVersion is required")
	case len(h.Sources) > maxSources:
		return fmt.Errorf("at most %d sources", maxSources)
	case len(h.Held) > maxHeld:
		return fmt.Errorf("at most %d held jobs", maxHeld)
	case len(h.Capabilities) > maxCapabilities:
		return fmt.Errorf("at most %d capabilities", maxCapabilities)
	case len(h.HarnessVersions) > maxHarnesses:
		return fmt.Errorf("at most %d harness versions", maxHarnesses)
	}
	for i := range h.Sources {
		if err := h.Sources[i].Validate(); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
	}
	for i := range h.Held {
		if err := h.Held[i].Validate(); err != nil {
			return fmt.Errorf("held[%d]: %w", i, err)
		}
	}
	return nil
}

// HeldJob is a job the runner still owns locally (running, or terminal with a
// result not yet acknowledged by the server).
type HeldJob struct {
	JobID         string `json:"jobId"`
	Generation    int64  `json:"generation"`
	State         string `json:"state"` // "running" | "terminal"
	LastOutputSeq uint64 `json:"lastOutputSeq"`
}

func (j *HeldJob) Validate() error {
	if _, err := uuid.Parse(j.JobID); err != nil {
		return errors.New("jobId must be a UUID")
	}
	if j.Generation < 1 {
		return errors.New("generation must be >= 1")
	}
	if j.State != "running" && j.State != "terminal" {
		return fmt.Errorf("state %q must be running or terminal", j.State)
	}
	return nil
}

// WireSource is one capacity source the runner advertises.
// ExecutionCapability is allowlisted execution evidence, never a local binding.
type ExecutionCapability struct {
	ToolPolicy          string   `json:"toolPolicy,omitempty"`
	Op                  string   `json:"op"`
	Backend             string   `json:"backend,omitempty"`
	Provider            string   `json:"provider"`
	EndpointFingerprint string   `json:"endpointFingerprint"`
	Families            []string `json:"families"`
	Models              []string `json:"models"`
	ContextWindows      []string `json:"contextWindows,omitempty"`
	Efforts             []string `json:"efforts,omitempty"`
	Tools               []string `json:"tools,omitempty"`
	ExecutedModel       bool     `json:"executedModel,omitempty"`
}

func (c *ExecutionCapability) Validate() error {
	if c.Op != "chat" && c.Op != "stream" && c.Op != "agent" && c.Op != "process" {
		return errors.New("unknown capability op")
	}
	if c.Provider == "" {
		return errors.New("capability provider is required")
	}
	fingerprint, err := hex.DecodeString(c.EndpointFingerprint)
	if err != nil || len(fingerprint) != sha256.Size {
		return errors.New("capability endpoint fingerprint must be SHA-256 hex")
	}
	if len(c.Families) == 0 || len(c.Families) > maxCapabilities || len(c.Models) == 0 || len(c.Models) > maxCapabilities || len(c.ContextWindows) > maxCapabilities || len(c.Efforts) > maxCapabilities || len(c.Tools) > maxTools {
		return errors.New("invalid capability evidence bounds")
	}
	for _, family := range c.Families {
		switch family {
		case "anthropic", "openai", "deepseek", "zhipuai", "moonshotai":
		default:
			return errors.New("unknown capability family")
		}
	}
	for _, values := range [][]string{c.Models, c.ContextWindows, c.Efforts, c.Tools} {
		for _, value := range values {
			if value == "" {
				return errors.New("empty capability value")
			}
		}
	}
	return nil
}

type WireSource struct {
	UnavailableReason string                `json:"unavailableReason,omitempty"`
	Capabilities      []ExecutionCapability `json:"capabilities,omitempty"`
	Observation       *WireProbeObservation `json:"observation,omitempty"`
	ObservationSeq    uint64                `json:"observationSeq,omitempty"`
	SampleAgeMs       int64                 `json:"sampleAgeMs,omitempty"`
	Kind              string                `json:"kind"`
	CredentialRef     string                `json:"credentialRef,omitempty"`
	Families          []string              `json:"families,omitempty"`
	Backends          []string              `json:"backends,omitempty"`
	Models            []string              `json:"models,omitempty"`
	Health            WireHealth            `json:"health"`
}

func (s *WireSource) Validate() error {
	if !remoteCredentialKinds[s.Kind] {
		return fmt.Errorf("source kind %q cannot be served by a remote runner", s.Kind)
	}
	if len(s.Capabilities) > maxCapabilities {
		return errors.New("too many source capabilities")
	}
	for i := range s.Capabilities {
		if err := s.Capabilities[i].Validate(); err != nil {
			return fmt.Errorf("capabilities[%d]: %w", i, err)
		}
	}
	if s.Observation != nil {
		if s.ObservationSeq == 0 || s.SampleAgeMs < 0 {
			return errors.New("invalid observation sequence or age")
		}
		if err := s.Observation.Validate(); err != nil {
			return err
		}
	}
	return s.Health.Validate()
}

// WireHealth mirrors runner.SourceHealth. Detail is a class/reset summary only.
type WireHealth struct {
	Auth           string       `json:"auth,omitempty"`
	Capacity       string       `json:"capacity,omitempty"`
	ResetAt        *time.Time   `json:"resetAt,omitempty"`
	Headroom       *float64     `json:"headroom,omitempty"`
	Windows        []WireWindow `json:"windows,omitempty"`
	Congested      bool         `json:"congested,omitempty"`
	CongestedUntil *time.Time   `json:"congestedUntil,omitempty"`
	LastProbeAt    *time.Time   `json:"lastProbeAt,omitempty"`
	LastOutcome    string       `json:"lastOutcome,omitempty"`
	Detail         string       `json:"detail,omitempty"`
}

func (h *WireHealth) Validate() error {
	if !authStates[h.Auth] {
		return fmt.Errorf("auth %q unknown", h.Auth)
	}
	if !capacityStates[h.Capacity] {
		return fmt.Errorf("capacity %q unknown", h.Capacity)
	}
	if !outcomeClasses[h.LastOutcome] {
		return fmt.Errorf("lastOutcome %q unknown", h.LastOutcome)
	}
	if h.Headroom != nil && (*h.Headroom < 0 || *h.Headroom > 1) {
		return errors.New("headroom must be within 0..1")
	}
	if len(h.Windows) > maxWindows {
		return fmt.Errorf("at most %d windows", maxWindows)
	}
	return nil
}

// WireWindow mirrors runner.LimitWindow.
type WireWindow struct {
	LimitID      string     `json:"limitId"`
	ModelScope   string     `json:"modelScope,omitempty"`
	DurationMins int        `json:"durationMins"`
	UsedPercent  *float64   `json:"usedPercent,omitempty"`
	ResetAt      *time.Time `json:"resetAt,omitempty"`
}

// WireTelemetry mirrors runner.Telemetry; nil fields are "unavailable".
type WireTelemetry struct {
	SampleAgeMs   int64     `json:"sampleAgeMs"`
	SampledAt     time.Time `json:"sampledAt"`
	CPUPercent    *float64  `json:"cpuPercent,omitempty"`
	MemUsedBytes  *uint64   `json:"memUsedBytes,omitempty"`
	MemPressure   *string   `json:"memPressure,omitempty"`
	Load1Pct      *float64  `json:"load1Pct,omitempty"`
	DiskFreeBytes *uint64   `json:"diskFreeBytes,omitempty"`
	Thermal       *string   `json:"thermal,omitempty"`
	UptimeSeconds *uint64   `json:"uptimeSeconds,omitempty"`
	Cores         *int      `json:"cores,omitempty"`
	RAMBytes      *uint64   `json:"ramBytes,omitempty"`
}

// Heartbeat is sent every heartbeatSeconds.
type Heartbeat struct {
	Telemetry WireTelemetry `json:"telemetry"`
	Sources   []WireSource  `json:"sources,omitempty"`
	Jobs      []HeldJob     `json:"jobs,omitempty"`
}

func (h *Heartbeat) Validate() error {
	if h.Telemetry.SampleAgeMs < 0 {
		return errors.New("sampleAgeMs must not be negative")
	}
	if len(h.Sources) > maxSources {
		return fmt.Errorf("at most %d sources", maxSources)
	}
	if len(h.Jobs) > maxHeld {
		return fmt.Errorf("at most %d jobs", maxHeld)
	}
	for i := range h.Sources {
		if err := h.Sources[i].Validate(); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
	}
	for i := range h.Jobs {
		if err := h.Jobs[i].Validate(); err != nil {
			return fmt.Errorf("jobs[%d]: %w", i, err)
		}
	}
	return nil
}

// Dispatch offers one job to the runner. Protocol v1 carries chat/stream only.
type Dispatch struct {
	Op         string       `json:"op"` // "chat" | "stream" (v1); "agent" | "process" arrive with Phase 3
	Chat       *WireChat    `json:"chat,omitempty"`
	Agent      *WireAgent   `json:"agent,omitempty"`
	Process    *WireProcess `json:"process,omitempty"`
	LeaseUntil time.Time    `json:"leaseUntil"` // offer deadline; unaccepted offers are re-offered with generation+1
}

func (d *Dispatch) Validate() error {
	switch d.Op {
	case "chat", "stream":
	default:
		return fmt.Errorf("op %q is not supported by protocol v%d", d.Op, ProtocolVersion)
	}
	if d.Chat == nil || d.Agent != nil || d.Process != nil {
		return errors.New("chat/stream dispatch carries exactly the chat payload")
	}
	return d.Chat.Validate()
}

// WireChat is the allowlisted, hermetic chat descriptor. It carries NO server
// credential material: no RevealKey, no BaseURL, no ConnectionRef, no Extras,
// no environment and no working directory (the runner uses a per-job scratch
// directory). The runner resolves CredentialKind to its OWN local credential.
type WireChat struct {
	Provider             string          `json:"provider,omitempty"`
	EndpointFingerprint  string          `json:"endpointFingerprint,omitempty"`
	Model                string          `json:"model"`
	SystemPrompt         string          `json:"systemPrompt,omitempty"`
	Prompt               string          `json:"prompt"`
	MaxTokens            int             `json:"maxTokens,omitempty"`
	Temperature          float64         `json:"temperature,omitempty"`
	OutputSchema         json.RawMessage `json:"outputSchema,omitempty"`
	SessionID            string          `json:"sessionId,omitempty"`
	RequireExecutedModel bool            `json:"requireExecutedModel,omitempty"`
	Stream               bool            `json:"stream,omitempty"`
	CredentialKind       string          `json:"credentialKind"`
	Backend              string          `json:"backend,omitempty"`
	ContextWindow        string          `json:"contextWindow,omitempty"`
	Effort               string          `json:"effort,omitempty"`
	Tools                []string        `json:"tools,omitempty"`
}

func (c *WireChat) Validate() error {
	if c.EndpointFingerprint != "" {
		fp, err := hex.DecodeString(c.EndpointFingerprint)
		if err != nil || len(fp) != sha256.Size {
			return errors.New("chat.endpointFingerprint must be a SHA-256 hex digest")
		}
	}
	switch {
	case c.Model == "":
		return errors.New("chat.model is required (exact model, never substituted)")
	case c.Prompt == "":
		return errors.New("chat.prompt is required")
	case !remoteCredentialKinds[c.CredentialKind]:
		return fmt.Errorf("chat.credentialKind %q cannot run on a remote runner", c.CredentialKind)
	case len(c.Tools) > maxTools:
		return fmt.Errorf("at most %d tools", maxTools)
	}
	return nil
}

// WireAgent and WireProcess are reserved for Phase 3 (agent/process jobs);
// protocol v1 rejects any dispatch that sets them.
type WireAgent struct{}

// WireProcess is reserved for Phase 3; see WireAgent.
type WireProcess struct{}

// Output is one sequenced output token for (jobId, generation).
type Output struct {
	Seq   uint64 `json:"seq"`
	Token string `json:"token"`
}

func (o *Output) Validate() error {
	if o.Seq < 1 {
		return errors.New("seq must be >= 1")
	}
	return nil
}

// Result is the terminal outcome of one (jobId, generation).
type Result struct {
	OK        bool               `json:"ok"`
	Chat      *WireChatResult    `json:"chat,omitempty"`
	Agent     *WireAgentResult   `json:"agent,omitempty"`
	Process   *WireProcessResult `json:"process,omitempty"`
	Outcome   WireOutcome        `json:"outcome"`
	ErrorText string             `json:"errorText,omitempty"` // sanitized by the runner; never provider prose with secrets
}

func (r *Result) Validate() error {
	n := 0
	for _, set := range []bool{r.Chat != nil, r.Agent != nil, r.Process != nil} {
		if set {
			n++
		}
	}
	if r.OK && n != 1 {
		return errors.New("an ok result carries exactly one payload")
	}
	if r.Agent != nil || r.Process != nil {
		return fmt.Errorf("agent/process results are not supported by protocol v%d", ProtocolVersion)
	}
	if !outcomeClasses[r.Outcome.Class] {
		return fmt.Errorf("outcome class %q unknown", r.Outcome.Class)
	}
	if len(r.ErrorText) > maxErrorText {
		return fmt.Errorf("errorText longer than %d bytes", maxErrorText)
	}
	if len(r.Outcome.Windows) > maxWindows {
		return fmt.Errorf("at most %d windows", maxWindows)
	}
	return nil
}

// WireChatResult mirrors engine.ChatResponse; Output is the raw assistant text.
type WireChatResult struct {
	Output       string          `json:"output"`
	Conversation json.RawMessage `json:"conversation,omitempty"`
	Model        string          `json:"model,omitempty"`
	TokensIn     int             `json:"tokensIn,omitempty"`
	TokensOut    int             `json:"tokensOut,omitempty"`
	SessionID    string          `json:"sessionId,omitempty"`
}

// WireAgentResult is reserved for Phase 3.
type WireAgentResult struct{}

// WireProcessResult is reserved for Phase 3.
type WireProcessResult struct{}

// WireOutcome mirrors runner.SourceOutcome.
type WireOutcome struct {
	Class        string       `json:"class,omitempty"`
	ResetAt      *time.Time   `json:"resetAt,omitempty"`
	RetryAfterMs int64        `json:"retryAfterMs,omitempty"`
	Windows      []WireWindow `json:"windows,omitempty"`
	HeadroomHint *float64     `json:"headroomHint,omitempty"`
}

// Challenge asks the runner to prove possession of its machine key.
type Challenge struct {
	KeyID string `json:"keyId"`
	Nonce string `json:"nonce"` // base64 std of 32 random bytes
}

func (c *Challenge) Validate() error {
	if _, err := uuid.Parse(c.KeyID); err != nil {
		return errors.New("keyId must be a UUID")
	}
	n, err := base64.StdEncoding.DecodeString(c.Nonce)
	if err != nil || len(n) != nonceBytes {
		return fmt.Errorf("nonce must be %d bytes base64", nonceBytes)
	}
	return nil
}

// ChallengeResponse carries the machine public key and the signature.
type ChallengeResponse struct {
	PublicKey string `json:"publicKey"` // base64 std, 32 bytes
	Signature string `json:"signature"` // base64 std, 64 bytes
}

// Decoded returns the raw key and signature.
func (c *ChallengeResponse) Decoded() (ed25519.PublicKey, []byte, error) {
	pub, err := base64.StdEncoding.DecodeString(c.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, nil, fmt.Errorf("publicKey must be %d bytes base64", ed25519.PublicKeySize)
	}
	sig, err := base64.StdEncoding.DecodeString(c.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, nil, fmt.Errorf("signature must be %d bytes base64", ed25519.SignatureSize)
	}
	return ed25519.PublicKey(pub), sig, nil
}

func (c *ChallengeResponse) Validate() error {
	_, _, err := c.Decoded()
	return err
}

// Admitted confirms admission.
type Admitted struct {
	RunnerID         string `json:"runnerId"`
	HeartbeatSeconds int    `json:"heartbeatSeconds"`
	ProtocolVersion  int    `json:"protocolVersion"`
}

func (a *Admitted) Validate() error {
	if a.RunnerID == "" || a.HeartbeatSeconds < 1 {
		return errors.New("runnerId and heartbeatSeconds are required")
	}
	return nil
}

// Refused explains why admission failed; the socket closes after it.
type Refused struct {
	Code        string `json:"code"` // "incompatible" | "revoked" | "wrong_machine" | "malformed"
	Reason      string `json:"reason"`
	ProtocolMin int    `json:"protocolMin,omitempty"`
	ProtocolMax int    `json:"protocolMax,omitempty"`
}

func (r *Refused) Validate() error {
	if r.Code == "" || r.Reason == "" {
		return errors.New("code and reason are required")
	}
	return nil
}

// Reject declines an offered job.
type Reject struct {
	Reason string `json:"reason"`
}

func (r *Reject) Validate() error { return nil }

// Ack acknowledges output up to Seq for (jobId, generation); Result=true also
// acknowledges the terminal result (the runner may drop it from its spool).
type Ack struct {
	Seq    uint64 `json:"seq"`
	Result bool   `json:"result,omitempty"`
}

func (a *Ack) Validate() error { return nil }

// Fenced tells the runner its (jobId, generation) is stale: kill and drop it.
type Fenced struct {
	Reason string `json:"reason,omitempty"`
}

func (f *Fenced) Validate() error { return nil }

// Cancel asks the runner to stop (jobId, generation).
type Cancel struct {
	Reason string `json:"reason,omitempty"`
}

func (c *Cancel) Validate() error { return nil }

// Probe asks the runner for a fresh read-only health probe of one source.
type Probe struct {
	RequestID      string `json:"requestId"`
	HealthRevision uint64 `json:"healthRevision"`
	SourceKind     string `json:"sourceKind"`
	CredentialRef  string `json:"credentialRef,omitempty"`
}

func (p *Probe) Validate() error {
	if _, err := uuid.Parse(p.RequestID); err != nil {
		return errors.New("requestId must be a UUID")
	}
	if !remoteCredentialKinds[p.SourceKind] {
		return fmt.Errorf("source kind %q cannot be probed remotely", p.SourceKind)
	}
	return nil
}

// ProbeResult answers a Probe.
type ProbeResult struct {
	RequestID      string     `json:"requestId"`
	HealthRevision uint64     `json:"healthRevision"`
	Source         WireSource `json:"source"`
}

func (p *ProbeResult) Validate() error {
	if _, err := uuid.Parse(p.RequestID); err != nil {
		return errors.New("requestId must be a UUID")
	}
	return p.Source.Validate()
}

// WireProbeObservation carries a fresh read-only result, not a health projection.
// Sequence and monotonic sample age are carried by WireSource; the receiver
// scopes them to the admitted connection.
type WireProbeObservation struct {
	HealthRevision uint64                   `json:"healthRevision"`
	Auth           string                   `json:"auth"`
	Capacity       *WireCapacityObservation `json:"capacity,omitempty"`
}

type WireCapacityObservation struct {
	ModelScope           string       `json:"modelScope,omitempty"`
	State                string       `json:"state"`
	OrdinaryUsageAllowed *bool        `json:"ordinaryUsageAllowed"`
	ResetAt              *time.Time   `json:"resetAt,omitempty"`
	Headroom             *float64     `json:"headroom,omitempty"`
	Windows              []WireWindow `json:"windows,omitempty"`
}

func (o *WireProbeObservation) Validate() error {
	if !authStates[o.Auth] {
		return errors.New("unknown observation auth")
	}
	if o.Capacity != nil {
		return o.Capacity.Validate()
	}
	return nil
}
func (o *WireCapacityObservation) Validate() error {
	return (&WireHealth{Capacity: o.State, Headroom: o.Headroom, Windows: o.Windows}).Validate()
}

func (w *WireWindow) Validate() error     { return nil }
func (t *WireTelemetry) Validate() error  { return nil }
func (r *WireChatResult) Validate() error { return nil }
func (o *WireOutcome) Validate() error {
	return (&WireHealth{LastOutcome: o.Class, Headroom: o.HeadroomHint, Windows: o.Windows}).Validate()
}
func (a *WireAgent) Validate() error       { return errors.New("agent dispatch is reserved for phase 3") }
func (p *WireProcess) Validate() error     { return errors.New("process dispatch is reserved for phase 3") }
func (a *WireAgentResult) Validate() error { return errors.New("agent result is reserved for phase 3") }
func (p *WireProcessResult) Validate() error {
	return errors.New("process result is reserved for phase 3")
}
