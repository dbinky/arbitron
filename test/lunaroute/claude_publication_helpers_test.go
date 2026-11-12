package lunaroute_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
)

const claudeInputSource = evidencePath + "/sources/lr02-live-models-2026-11-11.json"

// Collect while the workspace exists; publish the narrow isolation observation
// only after source agreement, safe projection and successful private cleanup.
// This does not publish the still-incomplete Claude agent/contract evidence.
func finishClaudeIsolation(repo, experiment string, isolation *claudeIsolation, capture *inferenceCapture, result *engine.AgentResult, started, finished time.Time) error {
	observation, err := collectClaudeSubagentRoutingEvidence(isolation, capture, result, started, finished)
	if err != nil {
		return errAgentEvidence
	}
	title, err := correlateClaudeTitle(capture, isolation, result.Model, started, finished)
	if err != nil {
		return errAgentEvidence
	}
	observation.SessionTitle = &title
	observation.Inference = capture.Records()
	observation.Scope = "main isolation/controls/memory, synchronous subagent and first-user title request routing only; no title adoption, other helper or complete contract verification"
	for _, source := range []string{observation.Source, observation.Limits.Source, observation.ProjectMemory.Source, observation.Effort.Source, observation.Thinking.Source, observation.Subagent.Source, claudeSubagentRoutingSource, observation.Selectors.Source, title.Source, claudeInputSource} {
		raw, err := readRepoFile(repo, source)
		if err != nil || source == claudeInputSource && validateClaudeIsolationInputSource(raw, observation.Limits) != nil {
			return errAgentEvidence
		}
	}
	observation.InputSource = claudeInputSource
	key := capture.key // Close clears the capture's private comparison key.
	raw, err := json.Marshal(observation)
	if err != nil || validateObservationJSON(raw, key) != nil {
		return errAgentEvidence
	}
	root, err := filepath.EvalSymlinks(experiment)
	if err != nil {
		return errAgentEvidence
	}
	workspace, err := filepath.EvalSymlinks(isolation.project)
	if err != nil || !strings.HasPrefix(workspace, root+string(os.PathSeparator)) {
		return errAgentEvidence
	}
	home, err := filepath.EvalSymlinks(isolation.home)
	if err != nil || !strings.HasPrefix(home, root+string(os.PathSeparator)) {
		return errAgentEvidence
	}
	destination, err := filepath.EvalSymlinks(repo)
	if err != nil || destination == root || strings.HasPrefix(destination, root+string(os.PathSeparator)) {
		return errAgentEvidence
	}
	if capture.Close() != nil || os.RemoveAll(root) != nil {
		return errAgentEvidence
	}
	if _, err := saveObservation(repo, key, "lr05-isolation.json", observation); err != nil {
		return errAgentEvidence
	}
	return nil
}

// Limits are authoritative source facts, not learned maxima from a short task.
// The existing collector has already joined these values to the actual loaded
// table, emitted env, final context usage and every captured Messages budget.
func validateClaudeIsolationInputSource(raw []byte, limits claudeMainLimitProof) error {
	if len(raw) == 0 || len(raw) > captureBodyLimit || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) || limits.ContextTokens <= 0 || limits.RequestedOutputTokens <= 0 {
		return errAgentEvidence
	}
	var source struct {
		Models []struct {
			ID      string `json:"id"`
			Context int    `json:"context_window"`
			Output  int    `json:"max_output_tokens"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return errAgentEvidence
	}
	matches := 0
	for _, model := range source.Models {
		if model.ID == limits.Model {
			matches++
			if model.Context != limits.ContextTokens || model.Output != limits.RequestedOutputTokens {
				return errAgentEvidence
			}
		}
	}
	if matches != 1 {
		return errAgentEvidence
	}
	return nil
}
