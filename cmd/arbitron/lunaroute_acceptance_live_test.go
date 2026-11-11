package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

const (
	acceptanceReadLimit     = 4 << 20
	acceptanceIdentityLimit = 200
	acceptanceWaitLimit     = 15 * time.Minute
	acceptancePollInterval  = 100 * time.Millisecond
)

var errAcceptanceEvidence = errors.New("incomplete or unsafe MCP acceptance evidence; release readiness pending")

// parseAcceptanceTrigger reads the handler's exact {id, queued} schema. A UUID
// in prose, a profile, or a different field is not the triggered instance id.
func parseAcceptanceTrigger(raw string) (uuid.UUID, error) {
	var result struct {
		ID     string `json:"id"`
		Queued *bool  `json:"queued"`
	}
	if len(raw) > acceptanceReadLimit || !uniqueAcceptanceJSON(json.NewDecoder(strings.NewReader(raw))) {
		return uuid.Nil, errAcceptanceEvidence
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || result.Queued == nil || d.Decode(new(any)) != io.EOF {
		return uuid.Nil, errAcceptanceEvidence
	}
	id, err := uuid.Parse(result.ID)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errAcceptanceEvidence
	}
	return id, nil
}

func triggerViaMCP(t *testing.T, call func(string, map[string]any) (bool, string), name string) uuid.UUID {
	t.Helper()
	isErr, text := call("instance_trigger", map[string]any{"workflowName": name, "priority": "normal"})
	if isErr {
		t.Fatal("MCP instance_trigger failed; release readiness pending")
	}
	id, err := parseAcceptanceTrigger(text)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func waitLunaRouteTerminal(t *testing.T, ds *domainStack, id uuid.UUID) *instance.Instance {
	t.Helper()
	ctx, cancel := context.WithTimeout(adminCtxForLocalUser(t, ds), acceptanceWaitLimit)
	defer cancel()
	in, err := pollAcceptance(ctx, id, acceptanceWaitLimit, acceptancePollInterval, func(ctx context.Context) (*instance.Instance, error) {
		call := mcpToolCallerAs(t, ds, ctx)
		isErr, text := call("instance_get", map[string]any{"id": id.String()})
		if isErr || len(text) > acceptanceReadLimit || unsafeAcceptanceText(text, "") {
			return nil, errAcceptanceEvidence
		}
		var in instance.Instance
		if json.Unmarshal([]byte(text), &in) != nil {
			return nil, errAcceptanceEvidence
		}
		return &in, nil
	})
	if err != nil {
		var last string
		readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
		defer readCancel()
		if ds.db.QueryRowContext(readCtx, `SELECT COALESCE(error,'') FROM node_executions WHERE instance_id=? ORDER BY rowid DESC LIMIT 1`, id.String()).Scan(&last) != nil {
			t.Fatal("MCP acceptance did not complete; last node error unavailable")
		}
		t.Fatalf("MCP acceptance did not complete: %s", provider.RedactLunaRoute(last))
	}
	return in
}

func uniqueAcceptanceJSON(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] || !uniqueAcceptanceJSON(d) {
				return false
			}
			seen[key] = true
		}
	case '[':
		for d.More() {
			if !uniqueAcceptanceJSON(d) {
				return false
			}
		}
	default:
		return false
	}
	_, err = d.Token()
	return err == nil
}

// fetch must honor its context, as do the real context-bearing MCP calls.
func pollAcceptance(ctx context.Context, id uuid.UUID, budget, interval time.Duration, fetch func(context.Context) (*instance.Instance, error)) (*instance.Instance, error) {
	if id == uuid.Nil || budget <= 0 || budget > acceptanceWaitLimit || interval <= 0 || interval > budget || fetch == nil {
		return nil, errAcceptanceEvidence
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return nil, errAcceptanceEvidence
		}
		in, err := fetch(ctx)
		if err != nil || ctx.Err() != nil || in == nil || in.ID != id {
			return nil, errAcceptanceEvidence
		}
		if in.State == instance.StateCompleted {
			return in, nil
		}
		if in.State.IsTerminal() {
			return nil, errAcceptanceEvidence
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errAcceptanceEvidence
		case <-timer.C:
		}
	}
}

type acceptanceNodeInput struct {
	RunID                                            uuid.UUID
	Backend, Model, ExpectedOutput, Nonce, Workspace string
	Node                                             instance.NodeExecution
}

// This is only node/workspace proof. In particular, codex_turn_context is a
// harness declaration. A matrix collector must additionally validate current
// gateway captures and lifecycle references before publishing release evidence.
type acceptanceNodeProof struct {
	RunID     uuid.UUID              `json:"run_id"`
	Models    []engine.ModelEvidence `json:"model_evidence"`
	FileOK    bool                   `json:"file_ok"`
	CommandOK bool                   `json:"command_ok"`
}

func safeAcceptanceIdentity(s, key string) bool {
	return s != "" && len(s) <= acceptanceIdentityLimit && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) && provider.RedactLunaRoute(s, key) == s
}

func collectAcceptanceNode(in acceptanceNodeInput, key string) (acceptanceNodeProof, error) {
	fail := func() (acceptanceNodeProof, error) { return acceptanceNodeProof{}, errAcceptanceEvidence }
	n := in.Node
	if in.RunID == uuid.Nil || n.InstanceID != in.RunID || n.NodeID == "" || n.Status != instance.NodeStatusSucceeded || n.StartedAt.IsZero() || n.CompletedAt == nil || n.CompletedAt.Before(n.StartedAt) || n.LLMModel == nil || in.ExpectedOutput == "" {
		return fail()
	}
	if output, ok := n.Output["response"].(string); !ok || output != in.ExpectedOutput || unsafeAcceptanceText(output, key) {
		return fail()
	}
	if !safeAcceptanceIdentity(in.Model, key) || !safeAcceptanceIdentity(*n.LLMModel, key) || !modelidentity.LunaRouteExecutedModelAccepted(in.Model, *n.LLMModel) {
		return fail()
	}
	proof := acceptanceNodeProof{RunID: in.RunID}
	if in.Backend == "" {
		proof.Models = []engine.ModelEvidence{{Model: *n.LLMModel, Source: "chat_response"}}
		return proof, nil
	}
	if *n.LLMModel != in.Model {
		return fail()
	}
	source := map[string]string{"claude-code": "claude_model_usage", "codex": "codex_turn_context", "opencode": "opencode_export", "pi": "pi_message_end"}[in.Backend]
	if source == "" {
		return fail()
	}
	// Persisted JSON maps and in-memory executor values share this schema.
	raw, err := json.Marshal(n.LLMConversation)
	if err != nil || len(raw) > acceptanceReadLimit || unsafeAcceptanceText(string(raw), key) {
		return fail()
	}
	var trace struct {
		Steps  []engine.AgentStep     `json:"steps"`
		Models []engine.ModelEvidence `json:"model_evidence"`
	}
	if json.Unmarshal(raw, &trace) != nil || len(trace.Models) == 0 {
		return fail()
	}
	tool := false
	for _, s := range trace.Steps {
		tool = tool || s.Kind == "tool_call"
	}
	if !tool {
		return fail()
	}
	for _, m := range trace.Models {
		if !safeAcceptanceIdentity(m.Model, key) || m.Source != source || m.Provider != "" && m.Provider != provider.CanonicalLunaRoute || !modelidentity.LunaRouteExecutedModelAccepted(in.Model, m.Model) {
			return fail()
		}
	}
	if in.Nonce == "" || len(in.Nonce) > acceptanceIdentityLimit || unsafeAcceptanceText(in.Nonce, key) {
		return fail()
	}
	file, err := acceptanceWorkspaceFile(in.Workspace, "lunaroute-ok.txt", n.StartedAt, *n.CompletedAt)
	if err != nil || string(file) != in.Nonce {
		return fail()
	}
	command, err := acceptanceWorkspaceFile(in.Workspace, "listing.txt", n.StartedAt, *n.CompletedAt)
	if err != nil || strings.TrimSpace(string(command)) != strconv.Itoa(len(in.Nonce)) {
		return fail()
	}
	proof.Models, proof.FileOK, proof.CommandOK = trace.Models, true, true
	return proof, nil
}

func acceptanceWorkspaceFile(ws, name string, start, end time.Time) ([]byte, error) {
	if ws == "" {
		return nil, errAcceptanceEvidence
	}
	st, err := os.Lstat(ws)
	if err != nil || !st.IsDir() {
		return nil, errAcceptanceEvidence
	}
	p := filepath.Join(ws, name)
	st, err = os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() || st.ModTime().Before(start) {
		return nil, errAcceptanceEvidence
	}
	// SQLite truncates persisted node timestamps to seconds. Include that
	// second, but never a later one; subsecond inputs retain their exact bound.
	if end.Nanosecond() == 0 {
		if !st.ModTime().Before(end.Add(time.Second)) {
			return nil, errAcceptanceEvidence
		}
	} else if st.ModTime().After(end) {
		return nil, errAcceptanceEvidence
	}
	return readAcceptanceFile(p)
}

func readAcceptanceFile(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0444 == 0 || st.Size() > acceptanceReadLimit {
		return nil, errAcceptanceEvidence
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errAcceptanceEvidence
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, acceptanceReadLimit+1))
	if err != nil || len(raw) > acceptanceReadLimit {
		return nil, errAcceptanceEvidence
	}
	return raw, nil
}

var acceptanceSecret = regexp.MustCompile(`(?i)(lr_[a-z0-9][a-z0-9._~+/=-]*|sk-[a-z0-9_-]+|bearer\s+\S+|(?:x-api-key|authorization)\s*[:=]\s*\S+)`)
var acceptanceEscape = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)

func unsafeAcceptanceText(s, key string) bool {
	// Logs can contain JSON-escaped diagnostics. Scan before truncation and
	// decode escapes repeatedly so an escaped credential cannot evade the scan.
	for {
		next := acceptanceEscape.ReplaceAllStringFunc(s, func(x string) string { n, _ := strconv.ParseUint(x[2:], 16, 16); return string(rune(n)) })
		next = strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\/`, "/", `\\`, `\`).Replace(next)
		if next == s {
			break
		}
		s = next
	}
	return key != "" && strings.Contains(s, key) || acceptanceSecret.MatchString(s)
}

func checkAcceptanceLog(path, key string) error {
	raw, err := readAcceptanceFile(path)
	if err != nil || unsafeAcceptanceText(string(bytes.TrimSpace(raw)), key) {
		return errAcceptanceEvidence
	}
	return nil
}
