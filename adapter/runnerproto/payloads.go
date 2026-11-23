// adapter/runnerproto/payloads.go
package runnerproto

import (
	"encoding/hex"
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
	if err != nil || len(fingerprint) != 32 {
		return errors.New("capability endpoint fingerprint must be SHA-256 hex")
	}
	if len(c.Families) == 0 || len(c.Families) > maxCapabilities || len(c.Models) == 0 || len(c.Models) > maxCapabilities || len(c.ContextWindows) > maxCapabilities || len(c.Efforts) > maxCapabilities || len(c.Tools) > maxTools {
		return errors.New("invalid capability evidence bounds")
	}
	for _, family := range c.Families {
		switch family {
		case "anthropic", "openai", "google", "meta", "mistral", "deepseek", "qwen", "xai":
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
