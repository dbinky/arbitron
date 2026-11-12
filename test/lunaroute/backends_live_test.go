package lunaroute_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	backendOutputLimit = 1 << 20
	backendRecordLimit = 64 << 10
	backendTreeLimit   = 1 << 20
	backendProofFile   = "lunaroute-ok.txt"
	backendCommandFile = "listing.txt"
)

var errBackendEvidence = errors.New("backend evidence incomplete or unsafe; release acceptance remains pending")

func TestLiveCodexHelp(t *testing.T) { runBackendHelpExperiment(t, "codex") }
func TestLivePiHelp(t *testing.T)    { runBackendHelpExperiment(t, "pi") }

// This inference-free experiment has a synthetic credential and cannot verify
// Bearer auth, tool work, helper routing, or the complete LR-08 contract.
func TestLivePiModelControls(t *testing.T) {
	model := liveModel(t, "ARBITRON_LUNAROUTE_E2E_PI_MODEL")
	if err := publishPiModelControlExperiment(context.Background(), repoRoot(t), liveBinary(t, "pi"), model, liveRules(t), t.TempDir(), "lr08-pi-model-controls.json"); err != nil {
		t.Fatal(errBackendEvidence)
	}
}

// This independent app-server query has no credential and launches no turn.
// Its proof cannot verify the exec mission, auth, helpers or the LR-07 contract.
func TestLiveCodexConfigControls(t *testing.T) {
	model := liveModel(t, "ARBITRON_LUNAROUTE_E2E_CODEX_MODEL")
	if err := publishCodexConfigControls(context.Background(), repoRoot(t), liveBinary(t, "codex"), model, t.TempDir(), "lr07-codex-config-controls.json"); err != nil {
		t.Fatal(errBackendEvidence)
	}
}

func runBackendHelpExperiment(t *testing.T, backend string) {
	t.Helper()
	bin := liveBinary(t, backend)
	proof, err := collectBackendHelp(context.Background(), backend, bin, t.TempDir())
	if err != nil {
		t.Fatal(errBackendEvidence)
	}
	t.Logf("%s %s: required CLI options advertised; inference, isolation controls and release acceptance remain unverified", proof.Backend, proof.Version)
}

// The raw output is private in-memory input to a lifecycle parser, never an
// observation or a diagnostic. The shared runner bounds both streams and kills
// the process group on cancellation. No caller inherits os.Environ implicitly.
func runCaptured(t *testing.T, ctx context.Context, bin string, args, env []string, cwd, stdin string) ([]byte, error) {
	t.Helper()
	if len(env) == 0 || cwd == "" {
		return nil, errBackendEvidence
	}
	lines, err := runCapturedLines(ctx, claudecli.NewExecRunner(), claudecli.RunSpec{Bin: bin, Args: args, Env: env, Dir: cwd, Stdin: stdin, MaxResponseBytes: backendOutputLimit})
	if err != nil {
		return nil, errBackendEvidence
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// All conflicting files live under a test-owned HOME/workspace, not the real
// user's home. These are deliberately foreign credentials, never live keys.
func seedConflictingHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"home/.codex/auth.json":        `{"OPENAI_API_KEY":"conflicting-subscription-secret"}`,
		"home/.codex/config.toml":      "model_provider = \"openai\"\nmodel = \"foreign-model\"\n",
		"home/.pi/agent/auth.json":     `{"openai":{"type":"api_key","key":"conflicting-subscription-secret"}}`,
		"home/.pi/agent/settings.json": `{"defaultProvider":"openai","defaultModel":"foreign-model"}`,
		"workspace/.codex/config.toml": "model_provider = \"openai\"\nmodel = \"foreign-project-model\"\n",
		"workspace/.pi/settings.json":  `{"defaultProvider":"openai","defaultModel":"foreign-project-model"}`,
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, []byte(body), 0600) != nil {
			t.Fatal("cannot seed private conflicting configuration")
		}
	}
	return root
}

type backendHomeEntry struct {
	Mode fs.FileMode
	Data []byte
}
type backendHomeSnapshot map[string]backendHomeEntry

// Snapshot only the four protected config trees; task outputs and ephemeral
// provider state are separate sinks. Bytes remain in memory, never in JSON.
func snapshotBackendHomes(root string) (backendHomeSnapshot, error) {
	out := backendHomeSnapshot{}
	total := 0
	for _, name := range []string{"home/.codex", "home/.pi", "workspace/.codex", "workspace/.pi"} {
		path := filepath.Join(root, name)
		err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return errBackendEvidence
			}
			info, err := d.Info()
			if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errBackendEvidence
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return errBackendEvidence
			}
			entry := backendHomeEntry{Mode: info.Mode()}
			total += len(rel) + 1
			if info.Mode().IsRegular() {
				if info.Size() > backendTreeLimit-int64(total) {
					return errBackendEvidence
				}
				f, err := os.Open(path)
				if err != nil {
					return errBackendEvidence
				}
				entry.Data, err = io.ReadAll(io.LimitReader(f, int64(backendTreeLimit-total)+1))
				f.Close()
				if err != nil {
					return errBackendEvidence
				}
				total += len(entry.Data)
			}
			if total > backendTreeLimit {
				return errBackendEvidence
			}
			out[rel] = entry
			return nil
		})
		if err != nil {
			return nil, errBackendEvidence
		}
	}
	return out, nil
}
func requireEmptyBackendProofs(ws string) error {
	for _, name := range []string{backendProofFile, backendCommandFile} {
		if _, err := os.Lstat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			return errBackendEvidence
		}
	}
	return nil
}

type backendTurnProof struct {
	SessionID             string
	Completed, ToolCalled bool
	Models                []engine.ModelEvidence
}

// Parse bounded JSONL, including scanner.Err, and reject duplicate JSON keys
// before projections can conceal contradictory identity/terminal fields.
func backendJSONLines(raw []byte, fold func([]byte) error) error {
	if len(raw) == 0 || len(raw) > backendOutputLimit {
		return errBackendEvidence
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), backendRecordLimit)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		if !uniqueJSONFields(decoder) {
			return errBackendEvidence
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || fold(line) != nil {
			return errBackendEvidence
		}
	}
	if scanner.Err() != nil {
		return errBackendEvidence
	}
	return nil
}
func backendModel(selected, model, providerID, source, key string) (engine.ModelEvidence, error) {
	if !safeCaptureModel(model) || !safeAgentField(model, key) || providerID != "lunaroute" || !modelidentity.LunaRouteExecutedModelAccepted(selected, model) {
		return engine.ModelEvidence{}, errBackendEvidence
	}
	return engine.ModelEvidence{Model: model, Provider: providerID, Source: source}, nil
}
func parseBackendTurn(backend, selected, key string, stdout, rollout []byte) (backendTurnProof, error) {
	var proof backendTurnProof
	fail := func() (backendTurnProof, error) { return backendTurnProof{}, errBackendEvidence }
	if backend != "codex" && backend != "pi" {
		return fail()
	}
	activeTurn, agentEnded := false, false
	err := backendJSONLines(stdout, func(line []byte) error {
		if agentEnded {
			return errBackendEvidence
		}
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			ID       string `json:"id"`
			Item     *struct {
				Type, Status string
				ExitCode     *int `json:"exit_code"`
			} `json:"item"`
			Message *struct {
				Role, Provider, Model string
				StopReason            string `json:"stopReason"`
				Content               []struct{ Type, Name string }
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return errBackendEvidence
		}
		if backend == "codex" {
			switch event.Type {
			case "thread.started":
				if proof.SessionID != "" || !safeAgentField(event.ThreadID, key) {
					return errBackendEvidence
				}
				proof.SessionID = event.ThreadID
			case "turn.started":
				if proof.SessionID == "" || activeTurn {
					return errBackendEvidence
				}
				activeTurn, proof.Completed = true, false
			case "turn.completed":
				if !activeTurn {
					return errBackendEvidence
				}
				activeTurn, proof.Completed = false, true
			case "turn.failed", "error":
				return errBackendEvidence
			case "item.completed":
				if !activeTurn {
					return errBackendEvidence
				}
				if event.Item != nil && event.Item.Type == "command_execution" && event.Item.Status == "completed" && event.Item.ExitCode != nil && *event.Item.ExitCode == 0 {
					proof.ToolCalled = true
				}
			}
		} else {
			switch event.Type {
			case "session":
				if proof.SessionID != "" || !safeAgentField(event.ID, key) {
					return errBackendEvidence
				}
				proof.SessionID = event.ID
			case "message_end":
				m := event.Message
				if proof.SessionID == "" || m == nil {
					return errBackendEvidence
				}
				if m.Role != "assistant" {
					return nil
				}
				model, err := backendModel(selected, m.Model, m.Provider, "pi_message_end", key)
				if err != nil {
					return err
				}
				proof.Models = append(proof.Models, model)
				switch m.StopReason {
				case "stop":
					proof.Completed = true
				case "toolUse":
					proof.Completed = false
				default:
					return errBackendEvidence
				}
				for _, c := range m.Content {
					if c.Type == "toolCall" && safeAgentField(c.Name, key) {
						proof.ToolCalled = true
					}
				}
			case "agent_start", "turn_start":
				if proof.SessionID == "" {
					return errBackendEvidence
				}
				proof.Completed = false
			case "agent_end":
				if !proof.Completed {
					return errBackendEvidence
				}
				agentEnded = true
			case "error":
				return errBackendEvidence
			}
		}
		return nil
	})
	if err != nil || !proof.Completed || !proof.ToolCalled || proof.SessionID == "" || backend == "pi" && !agentEnded {
		return fail()
	}
	if backend == "codex" {
		session := ""
		err = backendJSONLines(rollout, func(line []byte) error {
			var event struct {
				Type    string
				Payload struct {
					ID, Model string
					Provider  string `json:"model_provider"`
				}
			}
			if json.Unmarshal(line, &event) != nil || event.Type == "" {
				return errBackendEvidence
			}
			switch event.Type {
			case "session_meta":
				if session != "" || event.Payload.ID != proof.SessionID {
					return errBackendEvidence
				}
				session = event.Payload.ID
			case "turn_context":
				if session != proof.SessionID {
					return errBackendEvidence
				}
				model, err := backendModel(selected, event.Payload.Model, event.Payload.Provider, "codex_turn_context_intent", key)
				if err != nil {
					return err
				}
				proof.Models = append(proof.Models, model)
			}
			return nil
		})
		if err != nil || session != proof.SessionID {
			return fail()
		}
	}
	if len(proof.Models) == 0 {
		return fail()
	}
	return proof, nil
}

type backendEvidenceInput struct {
	RunID, Backend, Model, Version, ConfigRef string
	StartedAt, FinishedAt                     time.Time
	RequiredCategories                        []string
	Records                                   []capturedInference
	Stdout, Rollout                           []byte
	Rules                                     provider.CapabilityRules
	Home, PrivateDir                          string
	Before                                    backendHomeSnapshot
}
type backendTaskObservation struct {
	RunID              string                 `json:"run_id"`
	Backend            string                 `json:"backend"`
	Model              string                 `json:"model"`
	Version            string                 `json:"version"`
	SessionID          string                 `json:"session_id"`
	Completed          bool                   `json:"completed"`
	FileOK             bool                   `json:"file_ok"`
	CommandOK          bool                   `json:"command_ok"`
	HomesUnchanged     bool                   `json:"homes_unchanged"`
	PrivateCleaned     bool                   `json:"private_cleaned"`
	ModelEvidence      []engine.ModelEvidence `json:"model_evidence"`
	ConfigRef          string                 `json:"config_ref"`
	RequiredCategories []string               `json:"required_categories"`
	Inference          []capturedInference    `json:"inference"`
}

func collectBackendEvidence(root, ws, key string, in backendEvidenceInput) (backendTaskObservation, error) {
	fail := func() (backendTaskObservation, error) { return backendTaskObservation{}, errBackendEvidence }
	for _, s := range []string{in.RunID, in.Backend, in.Model, in.Version, in.ConfigRef} {
		if !safeAgentField(s, key) {
			return fail()
		}
	}
	if in.StartedAt.IsZero() || !in.FinishedAt.After(in.StartedAt) {
		return fail()
	}
	if _, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: in.Model, RouteProvider: "lunaroute"}); err != nil {
		return fail()
	}
	if in.Backend == "pi" {
		m, ok := in.Rules.LunaRouteModel(in.Model)
		if !ok || !m.ContextTokens.Known() || !m.OutputTokens.Known() || m.Reasoning.Evidence == "" || m.Reasoning.Support != provider.SupportSupported && m.Reasoning.Support != provider.SupportUnsupported {
			return fail()
		}
	}
	proof, err := parseBackendTurn(in.Backend, in.Model, key, in.Stdout, in.Rollout)
	if err != nil {
		return fail()
	}
	seenCategories := make(map[string]bool)
	for _, category := range in.RequiredCategories {
		if !safeAgentField(category, key) || seenCategories[category] {
			return fail()
		}
		seenCategories[category] = true
	}
	if len(in.RequiredCategories) < 2 || !slices.Contains(in.RequiredCategories, "main") || validateCapturedInference(in.Records, in.Model, in.RequiredCategories) != nil {
		return fail()
	}
	path := "/v1/responses"
	if in.Backend == "pi" {
		path = "/v1/chat/completions"
	}
	for _, r := range in.Records {
		if r.RunID != in.RunID || r.Backend != in.Backend || r.Path != path || r.StartedAt.Before(in.StartedAt) || r.FinishedAt.After(in.FinishedAt) || r.Operation != nil && (r.Operation.Session != proof.SessionID || r.Operation.Version != in.Version) {
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
	if decoder.Decode(&config) != nil || config.RunID != in.RunID || config.Backend != in.Backend || config.Model != in.Model || config.SessionID != proof.SessionID || !safeAgentField(config.Source, key) {
		return fail()
	}
	after, err := snapshotBackendHomes(in.Home)
	if err != nil || len(in.Before) == 0 || !reflect.DeepEqual(in.Before, after) {
		return fail()
	}
	if in.PrivateDir == "" {
		return fail()
	}
	if _, err := os.Lstat(in.PrivateDir); !os.IsNotExist(err) {
		return fail()
	}
	file, err := workspaceProof(ws, backendProofFile, agentProofLimit, in.StartedAt, in.FinishedAt)
	if err != nil || string(file) != "ok" {
		return fail()
	}
	command, err := workspaceProof(ws, backendCommandFile, agentProofLimit, in.StartedAt, in.FinishedAt)
	if err != nil || strings.TrimSpace(string(command)) != "2" {
		return fail()
	}
	obs := backendTaskObservation{
		RunID: in.RunID, Backend: in.Backend, Model: in.Model, Version: in.Version,
		SessionID: proof.SessionID, Completed: true, FileOK: true, CommandOK: true,
		HomesUnchanged: true, PrivateCleaned: true, ModelEvidence: proof.Models,
		ConfigRef: in.ConfigRef, RequiredCategories: in.RequiredCategories, Inference: in.Records,
	}
	raw, err = json.Marshal(obs)
	if err != nil || validateObservationJSON(raw, key) != nil {
		return fail()
	}
	return obs, nil
}
func publishBackendEvidence(root, ws, key, name string, in backendEvidenceInput) error {
	obs, err := collectBackendEvidence(root, ws, key, in)
	if err != nil {
		return err
	}
	_, err = saveObservation(root, key, name, obs)
	return err
}
