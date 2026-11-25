// adapter/runnerproto/workspace.go
package runnerproto

import (
	"errors"
	"fmt"
	"path"

	"github.com/google/uuid"
)

// WireWorkspace is the sync plan for a workspace op (Phase 3 §3.3). The runner
// reuses its local copy only when HomeGeneration AND CheckpointVersion match
// its record and the copy is not dirty; it pushes with PushToken afterwards.
type WireWorkspace struct {
	RunID             string `json:"runId"`
	WorkDir           string `json:"workDir"` // canonical {{workspace}} (identical on every runner, D2)
	HomeGeneration    int64  `json:"homeGeneration"`
	CheckpointVersion int64  `json:"checkpointVersion"`
	ManifestHash      string `json:"manifestHash"`
	// BaseRef pins the committed checkpoint; Fence* identifies the live resource claim.
	BaseRef         string `json:"baseRef"`
	FenceKey        string `json:"fenceKey"`
	FenceOwner      string `json:"fenceOwner"`
	FenceGeneration int64  `json:"fenceGeneration"`
	FenceToken      string `json:"fenceToken"`
	PullToken       string `json:"pullToken,omitempty"`
	PushToken       string `json:"pushToken,omitempty"`
	PartBytes       int64  `json:"partBytes"`
}

// Validate rejects an incomplete scoped workspace descriptor.
func (w *WireWorkspace) Validate() error {
	run, err := uuid.Parse(w.RunID)
	if err != nil || run == uuid.Nil {
		return errors.New("workspace.runId must be a nonzero UUID")
	}
	if w.WorkDir == "" || !path.IsAbs(w.WorkDir) || path.Clean(w.WorkDir) != w.WorkDir {
		return errors.New("workspace.workDir must be a clean absolute path")
	}
	if w.CheckpointVersion < 1 || w.ManifestHash == "" ||
		w.BaseRef != fmt.Sprintf("%s@%d:%s", run, w.CheckpointVersion, w.ManifestHash) {
		return errors.New("workspace.baseRef must pin this run's checkpoint version and manifest hash")
	}
	if tok, err := uuid.Parse(w.FenceToken); err != nil || tok == uuid.Nil {
		return errors.New("workspace.fenceToken must be a nonzero UUID")
	}
	if w.FenceKey != "run:"+run.String() || w.FenceOwner == "" || w.FenceGeneration < 1 {
		return errors.New("workspace fence requires this run's key, an owner and a positive generation")
	}
	if w.PartBytes <= 0 {
		return errors.New("workspace.partBytes must be positive")
	}
	return nil
}

// WireProcess is a script or tool_call. NodeEnv values are already resolved on
// the server; Passthrough carries NAMES only — values are read on the runner.
// Dir is set for tool_call (the workspace); a script runs in its unpacked
// bundle, so Dir is empty and BundleHash/BundleEntrypoint are required.
type WireProcess struct {
	Kind             string            `json:"kind"` // "script" | "tool_call"
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	Dir              string            `json:"dir,omitempty"`
	Stdin            []byte            `json:"stdin,omitempty"`
	NodeEnv          map[string]string `json:"nodeEnv,omitempty"`
	Passthrough      []string          `json:"passthrough,omitempty"`
	TimeoutSecs      int               `json:"timeoutSecs,omitempty"`
	WorkDir          string            `json:"workDir,omitempty"`
	BundleHash       string            `json:"bundleHash,omitempty"`
	BundleEntrypoint string            `json:"bundleEntrypoint,omitempty"`
	BundleToken      string            `json:"bundleToken,omitempty"`
	Requires         []string          `json:"requires,omitempty"` // RR-09 decision 2026-11-22: checked by the runner before accept
}

type WireProcessResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// WireAgent is an agent mission plus a binding DESCRIPTOR: no endpoint, key,
// connection reference or provider extras ever leave the server. Only request
// facts travel; the runner uses its own loaded capability rules (P3 T15).
type WireAgent struct {
	Provider             string   `json:"provider"`
	Model                string   `json:"model"`
	Prompt               string   `json:"prompt"`
	WorkDir              string   `json:"workDir"`
	ToolScope            []string `json:"toolScope,omitempty"`
	SessionID            string   `json:"sessionId,omitempty"`
	TimeoutSecs          int      `json:"timeoutSecs,omitempty"`
	CredentialKind       string   `json:"credentialKind"`
	Backend              string   `json:"backend"`
	Effort               string   `json:"effort,omitempty"`
	ContextWindow        string   `json:"contextWindow,omitempty"`
	RequireExecutedModel bool     `json:"requireExecutedModel,omitempty"`
}

type WireAgentStep struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

type WireModelEvidence struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
	Source   string `json:"source"`
	Kind     string `json:"kind,omitempty"`
}

type WireAgentResult struct {
	Summary       string              `json:"summary"`
	Steps         []WireAgentStep     `json:"steps,omitempty"`
	Model         string              `json:"model,omitempty"`
	ModelEvidence []WireModelEvidence `json:"modelEvidence,omitempty"`
	DroppedTools  []string            `json:"droppedTools,omitempty"`
	TokensIn      int                 `json:"tokensIn,omitempty"`
	TokensOut     int                 `json:"tokensOut,omitempty"`
	SessionID     string              `json:"sessionId,omitempty"`
}
