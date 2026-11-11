package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	openCodeSubagentLimit = 16
	openCodeIdentityLimit = 128
)

// v1.19.5 TaskTool records synchronous child identity in state.metadata;
// export preserves it unless --sanitize is requested. Raw transcripts stay
// in bounded memory. See sources/lr06-subagent-lifecycle.md.
type openCodeSessionExport struct {
	Info struct {
		ID       string `json:"id"`
		ParentID string `json:"parentID"`
		Title    string `json:"title"`
	} `json:"info"`
	Messages []struct {
		Info struct {
			ID         string `json:"id"`
			SessionID  string `json:"sessionID"`
			ParentID   string `json:"parentID"`
			Role       string `json:"role"`
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
			Finish     string `json:"finish"`
			Agent      string `json:"agent"`
			Mode       string `json:"mode"`
			Summary    bool   `json:"summary"`
			Model      struct {
				ProviderID string `json:"providerID"`
				ModelID    string `json:"modelID"`
			} `json:"model"`
			Error  json.RawMessage `json:"error"`
			Tokens json.RawMessage `json:"tokens"`
			Time   struct {
				Created   int64 `json:"created"`
				Completed int64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
		Parts []struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			Type      string `json:"type"`
			Tool      string `json:"tool"`
			Auto      *bool  `json:"auto"`
			Text      string `json:"text"`
			State     struct {
				Status string `json:"status"`
				Time   struct {
					Start int64 `json:"start"`
					End   int64 `json:"end"`
				} `json:"time"`
				Metadata struct {
					ParentSession string `json:"parentSessionId"`
					ChildSession  string `json:"sessionId"`
					Background    bool   `json:"background"`
					Model         struct {
						ProviderID string `json:"providerID"`
						ModelID    string `json:"modelID"`
					} `json:"model"`
				} `json:"metadata"`
			} `json:"state"`
		} `json:"parts"`
	} `json:"messages"`
}
type openCodeSubagentTask struct {
	ParentSession, ChildSession, ToolID, Model string
	StartedMS, FinishedMS                      int64
}
type openCodeSubagentProof struct {
	ChildSession string   `json:"child_session"`
	ToolID       string   `json:"tool_id"`
	MessageIDs   []string `json:"message_ids"`
}

func openCodeID(id, prefix string) bool {
	if !safeCaptureText(id, "") || !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) || len(id) > openCodeIdentityLimit {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func decodeOpenCodeSession(raw []byte) (openCodeSessionExport, error) {
	var out openCodeSessionExport
	if len(raw) == 0 || len(raw) > openCodeControlsLimit {
		return out, errAgentEvidence
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var extra any
	if !uniqueJSONFields(d) || d.Decode(&extra) != io.EOF || json.Unmarshal(raw, &out) != nil {
		return openCodeSessionExport{}, errAgentEvidence
	}
	return out, nil
}
func parseOpenCodeSubagentTasks(raw []byte, session, model string) ([]openCodeSubagentTask, error) {
	out, err := decodeOpenCodeSession(raw)
	if err != nil || !openCodeID(session, "ses_") || out.Info.ID != session {
		return nil, errAgentEvidence
	}
	if _, err := explicitLiveModel(func(string) (string, bool) { return model, true }, "model"); err != nil {
		return nil, errAgentEvidence
	}
	var tasks []openCodeSubagentTask
	seen := map[string]bool{}
	children := map[string]bool{}
	for _, message := range out.Messages {
		for _, part := range message.Parts {
			if part.Type != "tool" || part.Tool != "task" {
				continue
			}
			state := part.State
			meta := state.Metadata
			if message.Info.Role != "assistant" || message.Info.SessionID != session || message.Info.ProviderID != "lunaroute" || message.Info.ModelID != model || !openCodeID(message.Info.ID, "msg_") {
				return nil, errAgentEvidence
			}
			if part.MessageID != message.Info.ID || part.SessionID != session || !openCodeID(part.ID, "prt_") || seen[part.ID] {
				return nil, errAgentEvidence
			}
			if state.Status != "completed" || state.Time.Start <= 0 || state.Time.End <= state.Time.Start {
				return nil, errAgentEvidence
			}
			if meta.ParentSession != session || !openCodeID(meta.ChildSession, "ses_") || meta.ChildSession == session || children[meta.ChildSession] || meta.Background {
				return nil, errAgentEvidence
			}
			if meta.Model.ProviderID != "lunaroute" || meta.Model.ModelID != model {
				return nil, errAgentEvidence
			}
			seen[part.ID] = true
			children[meta.ChildSession] = true
			tasks = append(tasks, openCodeSubagentTask{ParentSession: session, ChildSession: meta.ChildSession, ToolID: part.ID, Model: model, StartedMS: state.Time.Start, FinishedMS: state.Time.End})
			if len(tasks) > openCodeSubagentLimit {
				return nil, errAgentEvidence
			}
		}
	}
	if len(tasks) == 0 {
		return nil, errAgentEvidence
	}
	return tasks, nil
}
func validateOpenCodeSubagentChild(raw []byte, task openCodeSubagentTask) (openCodeSubagentProof, error) {
	fail := func() (openCodeSubagentProof, error) { return openCodeSubagentProof{}, errAgentEvidence }
	out, err := decodeOpenCodeSession(raw)
	if err != nil || out.Info.ID != task.ChildSession || out.Info.ParentID != task.ParentSession {
		return fail()
	}
	proof := openCodeSubagentProof{ChildSession: task.ChildSession, ToolID: task.ToolID}
	seen := map[string]bool{}
	users := map[string]int64{}
	lastCompleted := task.StartedMS
	lastRole, lastFinish := "", ""
	for _, message := range out.Messages {
		info := message.Info
		if lastFinish == "stop" || !openCodeID(info.ID, "msg_") || seen[info.ID] || info.SessionID != task.ChildSession || info.Time.Created < task.StartedMS || info.Time.Created > task.FinishedMS {
			return fail()
		}
		seen[info.ID] = true
		lastRole = info.Role
		if info.Role == "user" {
			users[info.ID] = info.Time.Created
			continue
		}
		userCreated, exists := users[info.ParentID]
		if info.Role != "assistant" || !exists || userCreated > info.Time.Created || info.ProviderID != "lunaroute" || info.ModelID != task.Model || len(info.Error) > 0 && string(info.Error) != "null" || info.Time.Created < lastCompleted || info.Time.Completed <= info.Time.Created || info.Time.Completed > task.FinishedMS || info.Finish != "stop" && info.Finish != "tool-calls" {
			return fail()
		}
		lastCompleted = info.Time.Completed
		lastFinish = info.Finish
		proof.MessageIDs = append(proof.MessageIDs, info.ID)
	}
	if len(proof.MessageIDs) == 0 || lastRole != "assistant" || lastFinish != "stop" {
		return fail()
	}
	for _, part := range out.Messages[len(out.Messages)-1].Parts {
		if part.Type == "tool" && part.State.Status == "error" {
			return fail()
		}
	}
	return proof, nil
}

// The experiment root validates the actual adapter-requested parent export and
// queries children with that same binary/env/cwd. A successful root mission is
// refused if its required synchronous subagent lifecycle is absent or invalid.
// Lifecycle validity is separate from captured gateway routing correlation.
type openCodeSubagentsRunner struct {
	next      claudecli.CommandRunner
	startedAt time.Time
	capture   *inferenceCapture
}

func (r openCodeSubagentsRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	if len(spec.Args) == 0 || spec.Args[0] != "export" {
		return r.next.Run(ctx, spec)
	}
	var overlay string
	keys, overlays := 0, 0
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "LUNAROUTE_API_KEY=") {
			keys++
		}
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			overlays++
			overlay = strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")
		}
	}
	if keys == 0 {
		return r.next.Run(ctx, spec)
	}
	fail := func() (<-chan string, func() error, error) {
		return nil, nil, provider.LocalLunaRouteError("subagent lifecycle", errAgentEvidence)
	}
	if r.next == nil || keys != 1 || overlays != 1 || spec.Dir == "" || len(spec.Args) != 3 || spec.Args[2] != "--pure" || !openCodeID(spec.Args[1], "ses_") {
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
	queryCtx, cancel := context.WithTimeout(ctx, openCodeControlsTimeout)
	defer cancel()
	query := spec
	query.Stdin = ""
	query.MaxResponseBytes = openCodeControlsLimit
	lines, err := runCapturedLines(queryCtx, r.next, query)
	if err != nil {
		return fail()
	}
	tasks, err := parseOpenCodeSubagentTasks([]byte(strings.Join(lines, "\n")), spec.Args[1], values.Model)
	if err != nil {
		return fail()
	}
	version := ""
	if r.capture != nil {
		versionQuery := query
		versionQuery.Args = []string{"--version"}
		versionQuery.MaxResponseBytes = agentVersionLimit
		versions, err := runCapturedLines(queryCtx, r.next, versionQuery)
		if err != nil || len(versions) != 1 {
			return fail()
		}
		version = strings.TrimPrefix(strings.TrimSpace(versions[0]), "opencode ")
		if version != "1.19.5" {
			return fail()
		}
	}
	for _, task := range tasks {
		if r.startedAt.IsZero() || task.StartedMS < r.startedAt.UnixMilli() || task.FinishedMS > time.Now().UnixMilli() {
			return fail()
		}
		query.Args = []string{"export", task.ChildSession, "--pure"}
		child, err := runCapturedLines(queryCtx, r.next, query)
		if err != nil {
			return fail()
		}
		proof, err := validateOpenCodeSubagentChild([]byte(strings.Join(child, "\n")), task)
		if err != nil {
			return fail()
		}
		if r.capture != nil && correlateOpenCodeSubagent(r.capture, task, proof, version) != nil {
			return fail()
		}
	}
	if r.capture != nil {
		raw := []byte(strings.Join(lines, "\n"))
		if correlateOpenCodeTitle(r.capture, raw, spec.Args[1], values.Model, version, r.startedAt) != nil || correlateOpenCodeCompaction(r.capture, raw, spec.Args[1], values.Model, version, r.startedAt) != nil {
			return fail()
		}
	}
	return bufferedOpenCodeLines(lines)
}
