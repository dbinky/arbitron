package lunaroute_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
)

const openCodeControlsSource = "docs/evidence/lunaroute-provider/sources/lr06-effective-controls.md"

// Only validated projections cross this boundary. Executable/cwd are retained
// privately to join both checks to the actual mission, never serialized.
type openCodeControlCheck struct {
	Controls   openCodeControlProof `json:"controls"`
	SessionID  string               `json:"session_id,omitempty"`
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt time.Time            `json:"finished_at"`
	bin, dir   string
	isolation  *openCodeIsolation
}

type openCodeControlJournal struct {
	mu        sync.Mutex
	entries   []openCodeControlCheck
	isolation *openCodeIsolation
}

func (j *openCodeControlJournal) record(spec claudecli.RunSpec, proof openCodeControlProof, started, finished time.Time) error {
	check := openCodeControlCheck{Controls: proof, StartedAt: started, FinishedAt: finished, bin: spec.Bin, dir: spec.Dir, isolation: j.isolation}
	if j.isolation != nil && j.isolation.validateLaunch(spec) != nil {
		return errAgentEvidence
	}
	for i, arg := range spec.Args {
		if arg == "--session" {
			if check.SessionID != "" || i+1 >= len(spec.Args) || !openCodeID(spec.Args[i+1], "ses_") {
				return errAgentEvidence
			}
			check.SessionID = spec.Args[i+1]
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	// Exactly the warm-up launch and its single same-session continuation.
	if len(j.entries) >= 2 || check.bin == "" || check.dir == "" || !finished.After(started) {
		return errAgentEvidence
	}
	j.entries = append(j.entries, check)
	return nil
}

func (j *openCodeControlJournal) checks() []openCodeControlCheck {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.entries)
}

type openCodeSupportingObservation struct {
	RunID              string                 `json:"run_id"`
	Backend            string                 `json:"backend"`
	Version            string                 `json:"version"`
	Model              string                 `json:"model"`
	SessionID          string                 `json:"session_id"`
	Source             string                 `json:"source"`
	InputSource        string                 `json:"input_source"`
	ControlsSource     string                 `json:"controls_source"`
	Scope              string                 `json:"scope"`
	FileOK             bool                   `json:"file_ok"`
	CommandOK          bool                   `json:"command_ok"`
	RequiredCategories []string               `json:"required_categories"`
	Controls           []openCodeControlCheck `json:"effective_controls"`
	Isolation          openCodeIsolationProof `json:"isolation"`
	Inference          []capturedInference    `json:"inference"`
}

// Called before workspace/capture cleanup; the caller publishes only after
// cleanup succeeds. This remains supporting evidence, not full LR-06 readiness.
func collectOpenCodeSupportingEvidence(ws string, capture *inferenceCapture, journal *openCodeControlJournal, result *engine.AgentResult, started, finished time.Time) (openCodeSupportingObservation, error) {
	fail := func() (openCodeSupportingObservation, error) {
		return openCodeSupportingObservation{}, errAgentEvidence
	}
	if capture == nil || capture.backend != "opencode" || result == nil || !openCodeID(result.SessionID, "ses_") || !safeCaptureModel(result.Model) || started.IsZero() || !finished.After(started) {
		return fail()
	}
	if journal == nil || journal.isolation == nil {
		return fail()
	}
	isolation, err := journal.isolation.proof()
	if err != nil {
		return fail()
	}
	checks := journal.checks()
	if len(checks) != 2 || checks[0].SessionID != "" || checks[1].SessionID != result.SessionID || checks[0].Controls != checks[1].Controls || checks[0].Controls.Model != result.Model || !checks[0].Controls.ToolCall || checks[0].Controls.ContextTokens <= 0 || checks[0].Controls.OutputTokens <= 0 {
		return fail()
	}
	workspace, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return fail()
	}
	for i, check := range checks {
		dir, err := filepath.EvalSymlinks(check.dir)
		if err != nil || check.isolation != journal.isolation || dir != workspace || check.bin == "" || check.bin != checks[0].bin || check.StartedAt.Before(started) || !check.FinishedAt.After(check.StartedAt) || check.FinishedAt.After(finished) || i > 0 && check.StartedAt.Before(checks[i-1].FinishedAt) {
			return fail()
		}
	}
	categories := []string{"main", "subagent", "title", "compaction"}
	records := capture.Records()
	if validateCapturedInference(records, result.Model, categories) != nil {
		return fail()
	}
	mainBeforeResume, compactionAfterCheck := false, false
	for _, record := range records {
		if record.RunID != capture.runID || record.Backend != "opencode" || record.Path != "/v1/chat/completions" || record.StartedAt.Before(checks[0].FinishedAt) || record.FinishedAt.After(finished) {
			return fail()
		}
		if record.Operation != nil && (record.Operation.Session != result.SessionID || record.Operation.Version != "1.19.5") {
			return fail()
		}
		mainBeforeResume = mainBeforeResume || record.Category == "main" && record.FinishedAt.Before(checks[1].StartedAt)
		compactionAfterCheck = compactionAfterCheck || record.Category == "compaction" && !record.StartedAt.Before(checks[1].FinishedAt)
	}
	if !mainBeforeResume || !compactionAfterCheck {
		return fail()
	}
	proof, err := workspaceProof(ws, agentProofFile, agentProofLimit, started, finished)
	if err != nil || string(proof) != agentProofText {
		return fail()
	}
	command, err := workspaceProof(ws, agentCommandFile, agentProofLimit, started, finished)
	if err != nil || strings.TrimSpace(string(command)) != strconv.Itoa(len(agentProofText)) {
		return fail()
	}
	toolCall := false
	for _, step := range result.Steps {
		toolCall = toolCall || step.Kind == "tool_call"
	}
	if !toolCall || len(result.ModelEvidence) == 0 {
		return fail()
	}
	for _, model := range result.ModelEvidence {
		if model.Model != result.Model || model.Provider != "lunaroute" {
			return fail()
		}
	}
	obs := openCodeSupportingObservation{
		RunID: capture.runID, Backend: "opencode", Version: "1.19.5",
		Model: result.Model, SessionID: result.SessionID,
		Source: openCodeCompactionSource, ControlsSource: openCodeControlsSource,
		InputSource: evidencePath + "/sources/lr02-live-models-2026-11-11.json",
		Scope:       "supporting-call experiment only; no contract verification or supported capability population",
		FileOK:      true, CommandOK: true, RequiredCategories: categories,
		Controls: checks, Isolation: isolation, Inference: records,
	}
	raw, err := json.Marshal(obs)
	if err != nil || validateObservationJSON(raw, capture.key) != nil {
		return fail()
	}
	return obs, nil
}
