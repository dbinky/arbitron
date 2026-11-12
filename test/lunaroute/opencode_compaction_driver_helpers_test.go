package lunaroute_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
)

const openCodeCompactionContinuation = "Acknowledge the completed file verification in one sentence. Do not use any tools."

// Experiment-only threshold control, not production model metadata. v1.19.5
// overflow.ts uses limit.input - compaction.reserved when input is registered.
// Register the R2 input fact without changing either production-emitted limit.
func openCodeCompactionSpec(spec claudecli.RunSpec, snapshot []byte, threshold int64) (claudecli.RunSpec, error) {
	fail := func() (claudecli.RunSpec, error) { return claudecli.RunSpec{}, errAgentEvidence }
	if len(snapshot) == 0 || len(snapshot) > openCodeControlsLimit {
		return fail()
	}
	if _, err := decodeOpenCodeControls(snapshot); err != nil {
		return fail()
	}
	var source struct {
		Models []struct {
			ID      string `json:"id"`
			Context int    `json:"context_window"`
			Input   int    `json:"max_input_tokens"`
			Output  int    `json:"max_output_tokens"`
		} `json:"models"`
	}
	if json.Unmarshal(snapshot, &source) != nil {
		return fail()
	}
	var overlay string
	index, keys := -1, 0
	for i, entry := range spec.Env {
		if strings.HasPrefix(entry, "LUNAROUTE_API_KEY=") {
			keys++
			if !provider.ValidLunaRouteKey(strings.TrimPrefix(entry, "LUNAROUTE_API_KEY=")) {
				return fail()
			}
		}
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			if index != -1 {
				return fail()
			}
			index = i
			overlay = strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")
		}
	}
	if keys != 1 || index == -1 {
		return fail()
	}
	cfg, err := decodeOpenCodeControls([]byte(overlay))
	if err != nil {
		return fail()
	}
	values, err := openCodeControlValues(cfg, "{env:LUNAROUTE_API_KEY}")
	if err != nil {
		return fail()
	}
	input, matches := 0, 0
	for _, model := range source.Models {
		if model.ID == values.Model {
			matches++
			input = model.Input
			if model.Context != values.ContextTokens || model.Output != values.OutputTokens || threshold <= 0 || threshold >= int64(input) || input > model.Context {
				return fail()
			}
		}
	}
	if matches != 1 {
		return fail()
	}
	var raw map[string]any
	if json.Unmarshal([]byte(overlay), &raw) != nil {
		return fail()
	}
	p := raw["provider"].(map[string]any)["lunaroute"].(map[string]any)
	model := p["models"].(map[string]any)[values.Model].(map[string]any)
	model["limit"].(map[string]any)["input"] = input
	raw["compaction"] = map[string]any{"auto": true, "reserved": int64(input) - threshold}
	emitted, err := json.Marshal(raw)
	if err != nil {
		return fail()
	}
	spec.Env = append([]string(nil), spec.Env...)
	spec.Env[index] = "OPENCODE_CONFIG_CONTENT=" + string(emitted)
	return spec, nil
}

func openCodeForcingControls(spec claudecli.RunSpec) ([]byte, error) {
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			return []byte(strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")), nil
		}
	}
	return nil, errAgentEvidence
}

// The native overflow check needs real nonzero reported usage. Do not inject a
// fake usage record, prepopulate a database or ask the model to summarize.
func openCodeCompactionWarmup(raw []byte, session, model string, started time.Time) (int64, error) {
	out, err := decodeOpenCodeSession(raw)
	if err != nil || out.Info.ID != session || out.Info.ParentID != "" || len(out.Messages) == 0 {
		return 0, errAgentEvidence
	}
	for _, message := range out.Messages {
		if message.Info.Summary {
			return 0, errAgentEvidence
		}
		for _, part := range message.Parts {
			if part.Type == "compaction" {
				return 0, errAgentEvidence
			}
		}
	}
	last := out.Messages[len(out.Messages)-1].Info
	if last.Role != "assistant" || last.SessionID != session || last.ProviderID != "lunaroute" || last.ModelID != model || last.Finish != "stop" || last.Time.Created < started.UnixMilli() || last.Time.Completed <= last.Time.Created || last.Time.Completed > time.Now().UnixMilli() || len(last.Error) > 0 && string(last.Error) != "null" {
		return 0, errAgentEvidence
	}
	var usage struct {
		Total  int64 `json:"total"`
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Cache  struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	}
	if json.Unmarshal(last.Tokens, &usage) != nil {
		return 0, errAgentEvidence
	}
	sum := int64(0)
	for _, count := range []int64{usage.Input, usage.Output, usage.Cache.Read, usage.Cache.Write} {
		if count < 0 || count > math.MaxInt64-sum {
			return 0, errAgentEvidence
		}
		sum += count
	}
	if usage.Total < 0 || usage.Total == 0 && sum == 0 {
		return 0, errAgentEvidence
	}
	if usage.Total > 0 {
		return usage.Total, nil
	}
	return sum, nil
}

// Constructed only by the compaction experiment root. Complete the initial
// launch, resume its actual session within the mission budget (not the adapter's
// short export budget), then let the adapter export into the existing joins.
type openCodeCompactionRunner struct {
	next     claudecli.CommandRunner
	snapshot []byte
	started  time.Time
}

func (r openCodeCompactionRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	fail := func() (<-chan string, func() error, error) {
		return nil, nil, provider.LocalLunaRouteError("automatic compaction", errAgentEvidence)
	}
	if r.next == nil {
		return fail()
	}
	if len(spec.Args) == 0 || spec.Args[0] != "run" {
		return r.next.Run(ctx, spec)
	}
	if spec.Dir == "" || r.started.IsZero() {
		return fail()
	}
	initial, err := runCapturedLines(ctx, r.next, spec)
	if err != nil {
		return fail()
	}
	session := ""
	for _, line := range initial {
		var event struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || !openCodeID(event.SessionID, "ses_") || session != "" && session != event.SessionID {
			return fail()
		}
		session = event.SessionID
	}
	if session == "" {
		return fail()
	}
	query := spec
	query.Args = []string{"export", session, "--pure"}
	query.Stdin = ""
	query.MaxResponseBytes = openCodeControlsLimit
	queryCtx, stopQuery := context.WithTimeout(ctx, openCodeControlsTimeout)
	defer stopQuery()
	lines, err := runCapturedLines(queryCtx, r.next, query)
	if err != nil {
		return fail()
	}
	raw := []byte(strings.Join(lines, "\n"))
	out, err := decodeOpenCodeSession(raw)
	if err != nil {
		return fail()
	}
	if out.Info.ParentID != "" {
		return fail()
	}
	emitted, err := openCodeForcingControls(query)
	if err != nil {
		return fail()
	}
	config, err := decodeOpenCodeControls(emitted)
	if err != nil {
		return fail()
	}
	values, err := openCodeControlValues(config, "{env:LUNAROUTE_API_KEY}")
	if err != nil {
		return fail()
	}
	threshold, err := openCodeCompactionWarmup(raw, session, values.Model, r.started)
	if err != nil {
		return fail()
	}
	forced, err := openCodeCompactionSpec(query, r.snapshot, threshold)
	if err != nil {
		return fail()
	}
	overlay, err := openCodeForcingControls(forced)
	if err != nil {
		return fail()
	}
	version := query
	version.Args = []string{"--version"}
	version.MaxResponseBytes = agentVersionLimit
	versionCtx, stopVersion := context.WithTimeout(ctx, agentVersionTimeout)
	defer stopVersion()
	versions, err := runCapturedLines(versionCtx, r.next, version)
	if err != nil || len(versions) != 1 || strings.TrimPrefix(strings.TrimSpace(versions[0]), "opencode ") != "1.19.5" {
		return fail()
	}
	debug := forced
	debug.Args = []string{"debug", "config", "--pure"}
	debugCtx, cancel := context.WithTimeout(ctx, openCodeControlsTimeout)
	defer cancel()
	effective, err := runCapturedLines(debugCtx, r.next, debug)
	if err != nil {
		return fail()
	}
	resolved := []byte(strings.Join(effective, "\n"))
	if validateOpenCodeCompactionControls(overlay, resolved) != nil {
		return fail()
	}
	resume := forced
	resume.Args = []string{"run", openCodeCompactionContinuation, "-m", "lunaroute/" + values.Model, "--session", session, "--dangerously-skip-permissions", "--format", "json", "--pure"}
	if _, err := runCapturedLines(ctx, r.next, resume); err != nil {
		return fail()
	}
	finalCtx, stopFinal := context.WithTimeout(ctx, openCodeControlsTimeout)
	defer stopFinal()
	lines, err = runCapturedLines(finalCtx, r.next, forced)
	if err != nil {
		return fail()
	}
	fresh := []byte(strings.Join(lines, "\n"))
	proof, err := parseOpenCodeCompaction(fresh, session, values.Model, "", r.started, time.Now().UTC())
	if err != nil {
		return fail()
	}
	completed, err := decodeOpenCodeSession(fresh)
	if err != nil || len(completed.Messages) == 0 {
		return fail()
	}
	last := completed.Messages[len(completed.Messages)-1].Info
	if last.Summary || last.Role != "assistant" || last.SessionID != session || last.ProviderID != "lunaroute" || last.ModelID != values.Model || last.Finish != "stop" || last.Time.Created < proof.FinishedMS || last.Time.Completed <= last.Time.Created || last.Time.Completed > time.Now().UnixMilli() || len(last.Error) > 0 && string(last.Error) != "null" {
		return fail()
	}
	return bufferedOpenCodeLines(initial)
}

// The ordinary projection omits experiment-only controls, so compare these
// explicitly. An ignored reserve/input must fail before the resumed launch.
func validateOpenCodeCompactionControls(emitted, resolved []byte) error {
	values, err := validateOpenCodeEffectiveControls(emitted, resolved)
	if err != nil {
		return errAgentEvidence
	}
	var expected, actual struct {
		Compaction struct {
			Auto     *bool `json:"auto"`
			Reserved int   `json:"reserved"`
		} `json:"compaction"`
		Providers map[string]struct {
			Models map[string]struct {
				Limit struct {
					Input int `json:"input"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if json.Unmarshal(emitted, &expected) != nil || json.Unmarshal(resolved, &actual) != nil || actual.Compaction.Auto == nil || !*actual.Compaction.Auto || expected.Compaction.Reserved <= 0 || actual.Compaction.Reserved != expected.Compaction.Reserved || actual.Providers["lunaroute"].Models[values.Model].Limit.Input != expected.Providers["lunaroute"].Models[values.Model].Limit.Input {
		return errAgentEvidence
	}
	return nil
}

func bufferedOpenCodeLines(lines []string) (<-chan string, func() error, error) {
	output := make(chan string, len(lines))
	for _, line := range lines {
		output <- line
	}
	close(output)
	return output, func() error { return nil }, nil
}
