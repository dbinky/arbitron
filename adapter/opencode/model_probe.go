package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
)

// ErrModelExecutionEvidence means the requested model could not be verified in
// this run's exported assistant messages. Request/config echoes are not proof.
var ErrModelExecutionEvidence = errors.New("opencode: model execution evidence unavailable or mismatched")

// ErrProbeIsolationUnverified means no discovery probe ran, because the D12
// isolation could not be established and verified for this launch: the
// resolved configuration was unreadable, or some MCP server stayed enabled.
var ErrProbeIsolationUnverified = errors.New("opencode: discovery probe isolation unverified; probe refused")

const (
	pureFlag        = "--pure"
	debugSubcommand = "debug"
	configCommand   = "config"
)

const (
	maxModelExportBytes int64 = 1 << 20
	modelExportTimeout        = 30 * time.Second
)

type runModelProof struct {
	sessionID  string
	messageIDs map[string]bool
}

// modelProof correlates the actual run events with the export. OpenCode creates
// a fresh session per RunAgent; no latest-session lookup or prior turn is used.
func modelProof(lines []string) (runModelProof, error) {
	proof := runModelProof{messageIDs: make(map[string]bool)}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line[0] != '{' {
			return proof, ErrModelExecutionEvidence
		}
		var event ocEvent
		if json.Unmarshal([]byte(line), &event) != nil {
			return proof, ErrModelExecutionEvidence
		}
		switch event.Type {
		case "error":
			return proof, ErrModelExecutionEvidence
		case "step_start", "step_finish", "text", "tool_use", "reasoning":
			if !strings.HasPrefix(event.SessionID, "ses_") || event.Part.SessionID != event.SessionID || event.Part.MessageID == "" {
				return proof, ErrModelExecutionEvidence
			}
			if proof.sessionID != "" && proof.sessionID != event.SessionID {
				return proof, ErrModelExecutionEvidence
			}
			proof.sessionID = event.SessionID
			proof.messageIDs[event.Part.MessageID] = true
		}
	}
	if len(proof.messageIDs) == 0 {
		return proof, ErrModelExecutionEvidence
	}
	return proof, nil
}

// verifyModelExport consumes the source-recorded export envelope, not a model
// field from stdout's text/request. All assistant steps must match, and every
// observed run message must be present and complete in the same session.
func verifyModelExport(raw []byte, proof runModelProof, providerID, model string) error {
	var data struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
		Messages []struct {
			Info struct {
				ID         string          `json:"id"`
				SessionID  string          `json:"sessionID"`
				Role       string          `json:"role"`
				ProviderID string          `json:"providerID"`
				ModelID    string          `json:"modelID"`
				Error      json.RawMessage `json:"error"`
				Time       struct {
					Completed int64 `json:"completed"`
				} `json:"time"`
			} `json:"info"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Info.ID != proof.sessionID {
		return ErrModelExecutionEvidence
	}
	seen := make(map[string]bool)
	for _, message := range data.Messages {
		info := message.Info
		if info.Role != "assistant" {
			continue
		}
		if info.ID == "" || seen[info.ID] || info.SessionID != proof.sessionID ||
			info.ProviderID != providerID || info.ModelID != model || info.Time.Completed <= 0 ||
			len(info.Error) > 0 && string(info.Error) != "null" {
			return ErrModelExecutionEvidence
		}
		seen[info.ID] = true
	}
	for id := range proof.messageIDs {
		if !seen[id] {
			return ErrModelExecutionEvidence
		}
	}
	return nil
}

// checkModelExecution reuses the run's binary, cwd and credential/config env.
// ctx is the caller context, not the mission's timed context: export gets its
// own budget while retaining caller cancellation and deadlines. Export is also
// bounded independently even when normal agent output has no cap.
// No transcript, credential-bearing stderr or runner error is surfaced.
//
// It returns the verified session id. A probe launch carries --pure, and its
// export keeps it so the export cannot load inherited plugins either.
func (p *Provider) checkModelExecution(ctx context.Context, spec claudecli.RunSpec, lines []string, d providerDescriptor) (string, error) {
	proof, err := modelProof(lines)
	if err != nil {
		return "", err
	}
	exportCtx, cancel := context.WithTimeout(ctx, modelExportTimeout)
	defer cancel()
	pure := slices.Contains(spec.Args, pureFlag)
	spec.Args = []string{"export", proof.sessionID}
	if pure {
		spec.Args = append(spec.Args, pureFlag)
	}
	spec.Stdin = ""
	spec.MaxResponseBytes = maxModelExportBytes
	output, wait, err := p.runner.Run(exportCtx, spec)
	if err != nil {
		return "", fmt.Errorf("%w: export launch failed", ErrModelExecutionEvidence)
	}
	var raw strings.Builder
	var total int64
	first := true
	for line := range output {
		separator := !first
		first = false
		total += int64(len(line))
		if separator {
			total++
		}
		if total <= maxModelExportBytes {
			if separator {
				raw.WriteByte('\n')
			}
			raw.WriteString(line)
		}
	}
	waitErr := wait()
	if exportCtx.Err() != nil {
		return "", fmt.Errorf("%w: export: %w", ErrModelExecutionEvidence, exportCtx.Err())
	}
	if waitErr != nil || total > maxModelExportBytes {
		return "", fmt.Errorf("%w: export failed or exceeded limit", ErrModelExecutionEvidence)
	}
	if err := verifyModelExport([]byte(raw.String()), proof, d.opencodeID, modelLeaf(p.binding.Model, d.opencodeID)); err != nil {
		return "", err
	}
	return proof.sessionID, nil
}

// ProbeExecutedModel is this agent route's discovery probe (D6), isolated per
// D12: one `opencode run` through the same binary, argv, credential env and
// provider routing a real mission uses, with workDir as cwd, plus
//   - an explicit deny-all permission block, whatever the profile's tools or the
//     allow-all knob say;
//   - every MCP server in OpenCode's fully resolved configuration (global,
//     $HOME/.opencode, account, managed and project layers, as `debug config`
//     reports them for this cwd and env) overridden with enabled:false in the
//     inline OPENCODE_CONFIG_CONTENT overlay, which OpenCode deep-merges over the
//     inherited layers;
//   - a second `debug config` under that overlay proving no server is enabled,
//     before the run launches; and
//   - --pure on the run and its export, so inherited plugins do not load.
//
// It returns the binding's model and the session id only when the run's
// correlated export names the expected provider and model on every assistant
// step. Errors never carry opencode output or credentials.
func (p *Provider) ProbeExecutedModel(ctx context.Context, prompt, workDir string) (string, string, error) {
	if workDir == "" {
		return "", "", ErrWorkDirRequired
	}
	d, ok := descriptorFor(p.binding.Provider)
	if !ok {
		return "", "", ErrUnknownProvider
	}
	args, err := buildArgs(engine.AgentMission{Prompt: prompt}, p.binding, d)
	if err != nil {
		return "", "", err
	}
	env, err := opencodeEnv(ctx, d, p.binding)
	if err != nil {
		return "", "", fmt.Errorf("%w: credential env", ErrModelExecutionEvidence)
	}
	base, _, err := buildConfig(d, p.binding, Posture{})
	if err != nil {
		return "", "", fmt.Errorf("%w: config", ErrModelExecutionEvidence)
	}
	if err := p.ensureCompatible(ctx, workDir); err != nil {
		// Version output can carry account/config details; expose a static verdict.
		return "", "", fmt.Errorf("%w: version check", ErrModelExecutionEvidence)
	}
	if compareVersion(p.version, mustParseVersion(minOpencodeProbeVersion)) < 0 {
		return "", "", fmt.Errorf("%w: isolated discovery probe requires >= %s", ErrIncompatibleOpencode, minOpencodeProbeVersion)
	}

	inherited, err := p.resolvedMCP(ctx, workDir, env, base)
	if err != nil {
		return "", "", err
	}
	overlay, err := disableMCP(base, inherited)
	if err != nil {
		return "", "", err
	}
	effective, err := p.resolvedMCP(ctx, workDir, env, overlay)
	if err != nil {
		return "", "", err
	}
	for _, enabled := range effective {
		if enabled == nil || *enabled {
			return "", "", fmt.Errorf("%w: an MCP server stays enabled", ErrProbeIsolationUnverified)
		}
	}

	runCtx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()
	probeArgs := append([]string{args[0], pureFlag}, args[1:]...)
	spec := claudecli.RunSpec{Bin: p.binPath, Args: probeArgs, Dir: workDir,
		Env: append(slices.Clone(env), "OPENCODE_CONFIG_CONTENT="+overlay), MaxResponseBytes: p.maxResponseBytes}
	lines, wait, err := p.runner.Run(runCtx, spec)
	if err != nil {
		return "", "", fmt.Errorf("%w: launch", ErrModelExecutionEvidence)
	}
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	if wait() != nil {
		return "", "", fmt.Errorf("%w: exit", ErrModelExecutionEvidence)
	}
	session, err := p.checkModelExecution(ctx, spec, collected, d)
	if err != nil {
		return "", "", err
	}
	return p.binding.Model, session, nil
}

// resolvedMCP runs `opencode debug config --pure` with the probe's cwd, env and
// the given inline config, and returns every resolved MCP server with its
// effective enabled flag (nil when the field is absent, which OpenCode treats
// as enabled). Output is bounded like the export; any read, exit or shape
// failure is an isolation failure, never an empty server set.
func (p *Provider) resolvedMCP(ctx context.Context, workDir string, env []string, cfgJSON string) (map[string]*bool, error) {
	debugCtx, cancel := context.WithTimeout(ctx, modelExportTimeout)
	defer cancel()
	spec := claudecli.RunSpec{Bin: p.binPath, Args: []string{debugSubcommand, configCommand, pureFlag}, Dir: workDir,
		Env: append(slices.Clone(env), "OPENCODE_CONFIG_CONTENT="+cfgJSON), MaxResponseBytes: maxModelExportBytes}
	output, wait, err := p.runner.Run(debugCtx, spec)
	if err != nil {
		return nil, fmt.Errorf("%w: config resolution launch failed", ErrProbeIsolationUnverified)
	}
	var raw strings.Builder
	var total int64
	for line := range output {
		total += int64(len(line)) + 1
		if total <= maxModelExportBytes {
			raw.WriteString(line)
			raw.WriteByte('\n')
		}
	}
	if wait() != nil || debugCtx.Err() != nil || total > maxModelExportBytes {
		return nil, fmt.Errorf("%w: config resolution failed or exceeded limit", ErrProbeIsolationUnverified)
	}
	var doc struct {
		MCP map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"mcp"`
	}
	if json.Unmarshal([]byte(raw.String()), &doc) != nil {
		return nil, fmt.Errorf("%w: unreadable resolved config", ErrProbeIsolationUnverified)
	}
	servers := make(map[string]*bool, len(doc.MCP))
	for name, entry := range doc.MCP {
		servers[name] = entry.Enabled
	}
	return servers, nil
}

// disableMCP returns base with an mcp block setting enabled:false on every
// named server. OpenCode deep-merges this over the inherited layers, keeping
// each server's definition but never starting it.
func disableMCP(base string, servers map[string]*bool) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(base), &doc); err != nil {
		return "", fmt.Errorf("%w: config", ErrModelExecutionEvidence)
	}
	if len(servers) > 0 {
		mcp := make(map[string]any, len(servers))
		for name := range servers {
			mcp[name] = map[string]any{"enabled": false}
		}
		doc["mcp"] = mcp
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("%w: config", ErrModelExecutionEvidence)
	}
	return string(out), nil
}
