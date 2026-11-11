package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	agentExperimentTimeout = 5 * time.Minute
	agentVersionTimeout    = 5 * time.Second
	agentVersionLimit      = 1024
	agentProofLimit        = 64
	agentProofFile         = "lunaroute-proof.txt"
	agentCommandFile       = "lunaroute-command.txt"
	agentProofText         = "LUNAROUTE-OK"
)

var errAgentEvidence = errors.New("agent task evidence incomplete or unsafe")
var errAgentPrerequisite = errors.New("explicit reviewed model required; release acceptance remains pending")

func explicitLiveModel(lookup func(string) (string, bool), name string) (string, error) {
	model, ok := lookup(name)
	if !ok || !safeCaptureModel(model) {
		return "", errAgentPrerequisite
	}
	_, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: model})
	if err != nil {
		return "", errAgentPrerequisite
	}
	return model, nil
}
func liveModel(t *testing.T, envVar string) string {
	t.Helper()
	model, err := explicitLiveModel(os.LookupEnv, envVar)
	if err != nil {
		value, supplied := os.LookupEnv(envVar)
		if supplied && value != "" || os.Getenv("ARBITRON_LUNAROUTE_RELEASE_GATE") == "1" {
			t.Fatal(errAgentPrerequisite)
		}
		t.Skip(errAgentPrerequisite)
	}
	return model
}
func liveBinary(t *testing.T, name string) string {
	t.Helper()
	bin, err := exec.LookPath(name)
	if err == nil {
		bin, err = filepath.Abs(bin)
	}
	if err != nil {
		if os.Getenv("ARBITRON_LUNAROUTE_RELEASE_GATE") == "1" {
			t.Fatal("required binary missing; release acceptance remains pending")
		}
		t.Skip("required binary missing; release acceptance remains pending")
	}
	return bin
}
func liveRules(t *testing.T) provider.CapabilityRules {
	t.Helper()
	rules, err := adapterconfig.LoadCapabilityRules(os.Getenv("ARBITRON_LUNAROUTE_E2E_CAPABILITIES"))
	if err != nil {
		t.Fatal("cannot load live capability table")
	}
	return rules
}
func liveBinding(key, model, backend string) provider.ResolvedBinding {
	return provider.GuardLunaRouteReveal(provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "lunaroute", BaseURL: hostedRoot, ConnectionRef: "live-agent", Model: model, Backend: backend, BackendExplicit: true, Pinned: true, AllowedTools: []string{"Read", "Write", "Bash"}, RevealKey: func(context.Context) (string, error) { return key, nil }})
}

// Raw lines remain in memory for the owning binary's lifecycle parser. Neither
// stderr nor exec errors may escape this boundary into console/artifact text.
func runCaptured(ctx context.Context, runner claudecli.CommandRunner, spec claudecli.RunSpec) ([]string, error) {
	if runner == nil || spec.MaxResponseBytes <= 0 || spec.MaxResponseBytes > captureBodyLimit {
		return nil, errAgentEvidence
	}
	ctx, cancel := context.WithTimeout(ctx, agentExperimentTimeout)
	defer cancel()
	lines, wait, err := runner.Run(ctx, spec)
	if err != nil {
		return nil, errAgentEvidence
	}
	var out []string
	total := int64(0)
	overflow := false
	for line := range lines {
		total += int64(len(line)) + 1
		if total > spec.MaxResponseBytes {
			overflow = true
			cancel()
			continue
		}
		if !overflow {
			out = append(out, line)
		}
	}
	if err := wait(); err != nil || overflow || ctx.Err() != nil {
		return nil, errAgentEvidence
	}
	return out, nil
}
func queryVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, agentVersionTimeout)
	defer cancel()
	lines, err := runCaptured(ctx, claudecli.NewExecRunner(), claudecli.RunSpec{Bin: bin, Args: []string{"--version"}, Env: procenv.BaseEnv(), MaxResponseBytes: agentVersionLimit})
	if err != nil || len(lines) != 1 {
		return "", errAgentEvidence
	}
	version := strings.TrimSpace(lines[0])
	if !safeAgentField(version, "") {
		return "", errAgentEvidence
	}
	return version, nil
}
func versionOf(t *testing.T, bin string) string {
	t.Helper()
	version, err := queryVersion(context.Background(), bin)
	if err != nil {
		t.Fatal("cannot obtain safe binary version")
	}
	return version
}
func requireEmptyAgentWorkspace(ws string) error {
	entries, err := os.ReadDir(ws)
	if err != nil || len(entries) != 0 {
		return errAgentEvidence
	}
	return nil
}

// The projection is read from the same-run emitted configuration observation,
// not from ambient os.Environ or a reconstructed launch. Its source is supplied
// by the owning backend's runner/lifecycle collector; fixtures cannot verify LR.
type agentConfigReference struct {
	RunID     string `json:"run_id"`
	Backend   string `json:"backend"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
	Source    string `json:"source"`
}
type agentEvidenceInput struct {
	RunID, Backend, Model, Version, ConfigRef string
	StartedAt, FinishedAt                     time.Time
	RequiredCategories                        []string
	Records                                   []capturedInference
	Result                                    *engine.AgentResult
}
type agentTaskObservation struct {
	RunID              string                 `json:"run_id"`
	Backend            string                 `json:"backend"`
	Model              string                 `json:"model"`
	Version            string                 `json:"version"`
	SessionID          string                 `json:"session_id"`
	Completed          bool                   `json:"completed"`
	FileOK             bool                   `json:"file_ok"`
	CommandOK          bool                   `json:"command_ok"`
	StepKinds          []string               `json:"step_kinds"`
	ModelEvidence      []engine.ModelEvidence `json:"model_evidence"`
	ConfigRef          string                 `json:"config_ref"`
	RequiredCategories []string               `json:"required_categories"`
	Inference          []capturedInference    `json:"inference"`
}

func safeAgentField(s, key string) bool {
	if !safeCaptureText(s, key) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func workspaceProof(ws, name string, limit int64, started, finished time.Time) ([]byte, error) {
	path := filepath.Join(ws, name)
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit || st.ModTime().Before(started) || st.ModTime().After(finished) {
		return nil, errAgentEvidence
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errAgentEvidence
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errAgentEvidence
	}
	return raw, nil
}
func collectAgentEvidence(root, ws, key string, in agentEvidenceInput) (agentTaskObservation, error) {
	fail := func() (agentTaskObservation, error) { return agentTaskObservation{}, errAgentEvidence }
	for _, s := range []string{in.RunID, in.Backend, in.Model, in.Version, in.ConfigRef} {
		if !safeAgentField(s, key) {
			return fail()
		}
	}
	if in.StartedAt.IsZero() || !in.FinishedAt.After(in.StartedAt) {
		return fail()
	}
	path := "/v1/messages"
	if in.Backend == "opencode" {
		path = "/v1/chat/completions"
	} else if in.Backend != "claude-code" {
		return fail()
	}
	if in.Result == nil || in.Result.Model != in.Model || !safeAgentField(in.Result.SessionID, key) || len(in.Result.ModelEvidence) == 0 {
		return fail()
	}
	// The caller must name an applicable helper set, not weaken it to just main.
	if len(in.RequiredCategories) < 2 || !slices.Contains(in.RequiredCategories, "main") {
		return fail()
	}
	seen := map[string]bool{}
	for _, category := range in.RequiredCategories {
		if !safeAgentField(category, key) || seen[category] {
			return fail()
		}
		seen[category] = true
	}
	if validateCapturedInference(in.Records, in.Model, in.RequiredCategories) != nil {
		return fail()
	}
	for _, r := range in.Records {
		if r.StartedAt.Before(in.StartedAt) || r.FinishedAt.After(in.FinishedAt) || r.RunID != in.RunID || r.Backend != in.Backend || r.Path != path || r.Operation != nil && (r.Operation.Session != in.Result.SessionID || r.Operation.Version != in.Version) {
			return fail()
		}
	}
	for _, e := range in.Result.ModelEvidence {
		if !safeCaptureModel(e.Model) || !safeAgentField(e.Source, key) || !modelidentity.LunaRouteExecutedModelAccepted(in.Model, e.Model) {
			return fail()
		}
		if e.Provider != "" && (!safeAgentField(e.Provider, key) || e.Provider != "lunaroute" && e.Provider != string(modelidentity.ClassifyFamily("lunaroute", e.Model))) {
			return fail()
		}
	}
	if !strings.HasPrefix(in.ConfigRef, evidencePath+"/observations/") {
		return fail()
	}
	raw, err := readRepoFile(root, in.ConfigRef)
	if err != nil || validateObservationJSON(raw, key) != nil {
		return fail()
	}
	var config agentConfigReference
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || config.RunID != in.RunID || config.Backend != in.Backend || config.Model != in.Model || config.SessionID != in.Result.SessionID || !safeAgentField(config.Source, key) {
		return fail()
	}
	proof, err := workspaceProof(ws, agentProofFile, agentProofLimit, in.StartedAt, in.FinishedAt)
	if err != nil || string(proof) != agentProofText {
		return fail()
	}
	command, err := workspaceProof(ws, agentCommandFile, agentProofLimit, in.StartedAt, in.FinishedAt)
	if err != nil || strings.TrimSpace(string(command)) != strconv.Itoa(len(agentProofText)) {
		return fail()
	}
	var kinds []string
	for _, step := range in.Result.Steps {
		if !safeAgentField(step.Kind, key) {
			return fail()
		}
		kinds = append(kinds, step.Kind)
	}
	if !slices.Contains(kinds, "tool_call") {
		return fail()
	}
	obs := agentTaskObservation{RunID: in.RunID, Backend: in.Backend, Model: in.Model, Version: in.Version, SessionID: in.Result.SessionID, Completed: true, FileOK: true, CommandOK: true, StepKinds: kinds, ModelEvidence: in.Result.ModelEvidence, ConfigRef: in.ConfigRef, RequiredCategories: in.RequiredCategories, Inference: in.Records}
	raw, err = json.Marshal(obs)
	if err != nil || validateObservationJSON(raw, key) != nil {
		return fail()
	}
	return obs, nil
}
func publishAgentEvidence(root, ws, key, name string, in agentEvidenceInput) error {
	obs, err := collectAgentEvidence(root, ws, key, in)
	if err != nil {
		return err
	}
	_, err = saveObservation(root, key, name, obs)
	return err
}
