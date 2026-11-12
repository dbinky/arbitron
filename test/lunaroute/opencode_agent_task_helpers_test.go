package lunaroute_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
)

const (
	openCodeSupportingName = "lr06-opencode-compaction.json"
	openCodeAgentName      = "lr06-opencode-agent.json"
)

// Collect while the real mission workspace exists, then publish only after
// successful proxy/CA and experiment cleanup. No support fact or register status
// follows from producing these artifacts; those require operator review.
func finishOpenCodeAgentTask(repo, experiment, ws string, capture *inferenceCapture, journal *openCodeControlJournal, result *engine.AgentResult, started, finished time.Time) error {
	support, err := collectOpenCodeSupportingEvidence(ws, capture, journal, result, started, finished)
	if err != nil {
		return errAgentEvidence
	}
	key := capture.key
	root, err := filepath.EvalSymlinks(experiment)
	if err != nil {
		return errAgentEvidence
	}
	workspace, err := filepath.EvalSymlinks(ws)
	if err != nil || !strings.HasPrefix(workspace, root+string(os.PathSeparator)) {
		return errAgentEvidence
	}
	destination, err := filepath.EvalSymlinks(repo)
	if err != nil || destination == root || strings.HasPrefix(destination, root+string(os.PathSeparator)) {
		return errAgentEvidence
	}
	sources := []string{support.Source, support.InputSource, support.ControlsSource, support.Isolation.Source}
	operationSources := map[string]string{"subagent": openCodeSubagentSource, "title": openCodeTitleSource, "compaction": openCodeCompactionSource}
	for _, record := range support.Inference {
		if record.Category == "main" {
			continue
		}
		if record.Operation == nil || operationSources[record.Category] == "" || record.Operation.Source != operationSources[record.Category] {
			return errAgentEvidence
		}
		sources = append(sources, record.Operation.Source)
	}
	for _, source := range sources {
		raw, err := readRepoFile(repo, source)
		if err != nil {
			return errAgentEvidence
		}
		if source == support.InputSource && validateOpenCodeAgentInputSource(raw, support.Controls[0].Controls) != nil {
			return errAgentEvidence
		}
	}
	kinds := make([]string, 0, len(result.Steps))
	for _, step := range result.Steps {
		if !safeAgentField(step.Kind, key) {
			return errAgentEvidence
		}
		kinds = append(kinds, step.Kind)
	}
	for _, model := range result.ModelEvidence {
		if !safeAgentField(model.Source, key) {
			return errAgentEvidence
		}
	}
	agent := agentTaskObservation{
		RunID: support.RunID, Backend: support.Backend, Model: support.Model,
		Version: support.Version, SessionID: support.SessionID,
		Completed: true, FileOK: support.FileOK, CommandOK: support.CommandOK,
		StepKinds: kinds, ModelEvidence: slices.Clone(result.ModelEvidence),
		ConfigRef:          evidencePath + "/observations/" + openCodeSupportingName,
		RequiredCategories: support.RequiredCategories, Inference: support.Inference,
	}
	// Validate both complete projections before cleanup or the first write.
	for _, observation := range []any{support, agent} {
		raw, err := json.Marshal(observation)
		if err != nil || validateObservationJSON(raw, key) != nil {
			return errAgentEvidence
		}
	}
	if capture.Close() != nil || os.RemoveAll(root) != nil {
		return errAgentEvidence
	}
	if _, err := saveObservation(repo, key, openCodeSupportingName, support); err != nil {
		return errAgentEvidence
	}
	if _, err := saveObservation(repo, key, openCodeAgentName, agent); err != nil {
		return errAgentEvidence
	}
	return nil
}

// The cited snapshot must actually agree with the emitted/effective model
// definition, not merely exist at the expected path.
func validateOpenCodeAgentInputSource(raw []byte, controls openCodeControlProof) error {
	if _, err := decodeOpenCodeControls(raw); err != nil || !controls.ToolCall {
		return errAgentEvidence
	}
	var source struct {
		Models []struct {
			ID           string `json:"id"`
			Context      int    `json:"context_window"`
			Input        int    `json:"max_input_tokens"`
			Output       int    `json:"max_output_tokens"`
			Capabilities struct {
				Tools *bool `json:"tools"`
			} `json:"capabilities"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return errAgentEvidence
	}
	matches := 0
	for _, model := range source.Models {
		if model.ID == controls.Model {
			matches++
			if model.Context != controls.ContextTokens || model.Output != controls.OutputTokens || model.Input <= 0 || model.Input > model.Context || model.Capabilities.Tools == nil || *model.Capabilities.Tools != controls.ToolCall {
				return errAgentEvidence
			}
		}
	}
	if matches != 1 {
		return errAgentEvidence
	}
	return nil
}
