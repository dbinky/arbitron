package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"go.yaml.in/yaml/v3"
)

const (
	hostedRoot              = "https://gw.lunaroute.com"
	invalidGatewayKey       = "lr_arbitron-deliberately-invalid-contract-key"
	maxGatewayResponseBytes = 4 << 20
	gatewayOutputTokens     = 1024
	gatewayPrompt           = "Reply with PONG only."
)

type gatewayModels struct{ Chat, Messages, Responses string }
type gatewayObservation struct {
	Dialect       string            `json:"dialect"`
	Path          string            `json:"path"`
	AuthHeader    string            `json:"auth_header"`
	Status        int               `json:"status"`
	Model         string            `json:"model,omitempty"`
	ReportedModel string            `json:"reported_model,omitempty"`
	Schema        map[string]string `json:"schema"`
	ModelCount    int               `json:"model_count"`
	Terminal      bool              `json:"terminal"`
	HasContent    bool              `json:"has_content"`
	ErrorCode     string            `json:"error_code,omitempty"`
}

func liveKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("ARBITRON_LUNAROUTE_E2E_KEY")
	if key == "" {
		if os.Getenv("ARBITRON_LUNAROUTE_RELEASE_GATE") == "1" {
			t.Fatal("ARBITRON_LUNAROUTE_E2E_KEY required; release acceptance remains pending")
		}
		t.Skip("no ARBITRON_LUNAROUTE_E2E_KEY; release acceptance remains pending")
	}
	if !provider.ValidLunaRouteKey(key) {
		t.Fatal("invalid LunaRoute live credential; nothing sent")
	}
	return key
}
func liveClient() *http.Client {
	return &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func shape(body []byte) map[string]string {
	var object map[string]any
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil
	}
	out := make(map[string]string, len(object))
	for k, v := range object {
		switch v.(type) {
		case nil:
			out[k] = "null"
		case string:
			out[k] = "string"
		case bool:
			out[k] = "boolean"
		case float64:
			out[k] = "number"
		case []any:
			out[k] = "array"
		case map[string]any:
			out[k] = "object"
		}
	}
	return out
}

// Only the live entry point chooses the fixed hosted root. The parameter is a
// test seam for exercising actual request/auth/body and persisted observation sinks.
func runGatewayContract(ctx context.Context, client *http.Client, root, key string, models gatewayModels) ([]gatewayObservation, error) {
	observations := make([]gatewayObservation, 0, 5)
	if !provider.ValidLunaRouteKey(key) {
		return observations, errors.New("invalid LunaRoute live credential; nothing sent")
	}
	for _, model := range []string{models.Chat, models.Messages, models.Responses} {
		fixed, err := modelidentity.NewVersionResolver().SpecificVersion(ctx, modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: model})
		if err != nil || fixed != model {
			return observations, errors.New("each dialect requires an explicit reviewed model")
		}
	}
	request := func(dialect, method, path, credential, model string, payload any) ([]byte, int, error) {
		var body bytes.Buffer
		if payload != nil && json.NewEncoder(&body).Encode(payload) != nil {
			return nil, 0, errors.New("cannot encode gateway request")
		}
		req, err := http.NewRequestWithContext(ctx, method, root+path, &body)
		if err != nil {
			return nil, 0, errors.New("cannot build gateway request")
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if dialect == "messages" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, 0, errors.New("gateway transport failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxGatewayResponseBytes+1))
		closeErr := res.Body.Close()
		o := gatewayObservation{Dialect: dialect, Path: path, AuthHeader: "Authorization", Status: res.StatusCode, Model: model}
		if readErr == nil && len(raw) <= maxGatewayResponseBytes {
			o.Schema = shape(raw)
		}
		observations = append(observations, o)
		if readErr != nil || closeErr != nil {
			return nil, res.StatusCode, errors.New("gateway response read failed")
		}
		if len(raw) > maxGatewayResponseBytes {
			return nil, res.StatusCode, errors.New("gateway response too large")
		}
		if o.Schema == nil {
			return nil, res.StatusCode, errors.New("gateway response is not a JSON object")
		}
		return raw, res.StatusCode, nil
	}
	raw, status, err := request("models", http.MethodGet, "/v1/models", key, "", nil)
	if err != nil {
		return observations, err
	}
	if status != http.StatusOK {
		return observations, errors.New("gateway listing failed")
	}
	ids, err := listingIDs(raw)
	if err != nil {
		return observations, err
	}
	observations[0].ModelCount = len(ids)
	if len(ids) == 0 {
		return observations, errors.New("empty availability cannot satisfy selected live profiles")
	}
	for _, model := range []string{models.Chat, models.Messages, models.Responses} {
		if !ids[model] {
			return observations, errors.New("explicit model absent from authoritative listing")
		}
	}
	for _, d := range []struct{ dialect, path, model string }{
		{"chat", "/v1/chat/completions", models.Chat},
		{"messages", "/v1/messages", models.Messages},
		{"responses", "/v1/responses", models.Responses},
	} {
		payload := map[string]any{"model": d.model, "stream": false}
		if d.dialect == "responses" {
			payload["input"] = gatewayPrompt
			payload["store"] = false
			payload["max_output_tokens"] = gatewayOutputTokens
		} else {
			payload["messages"] = []map[string]string{{"role": "user", "content": gatewayPrompt}}
			payload["max_tokens"] = gatewayOutputTokens
		}
		raw, status, err = request(d.dialect, http.MethodPost, d.path, key, d.model, payload)
		if err != nil {
			return observations, err
		}
		if status < 200 || status >= 300 {
			return observations, errors.New("gateway inference rejected; inspect safe status/schema observation")
		}
		reported, terminal, content := terminalResponse(d.dialect, raw)
		o := &observations[len(observations)-1]
		o.ReportedModel = reported
		o.Terminal = terminal
		o.HasContent = content
		if !terminal || !content {
			return observations, errors.New("gateway inference did not complete with content")
		}
		if !modelidentity.LunaRouteExecutedModelAccepted(d.model, reported) {
			return observations, errors.New("gateway reported model does not match reviewed selection")
		}
	}
	raw, status, err = request("invalid-key", http.MethodGet, "/v1/models", invalidGatewayKey, "", nil)
	if err != nil {
		return observations, err
	}
	var envelope struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return observations, errors.New("invalid-key response malformed")
	}
	code := envelope.Error.Code
	if code == "" {
		code = envelope.Code
	}
	if envelope.Code != "" && envelope.Error.Code != "" && envelope.Code != envelope.Error.Code {
		code = ""
	}
	if code == "INVALID_API_KEY" {
		observations[len(observations)-1].ErrorCode = code
	}
	if status != http.StatusUnauthorized || code != "INVALID_API_KEY" {
		return observations, errors.New("invalid key did not produce documented credentials verdict")
	}
	return observations, nil
}
func listingIDs(raw []byte) (map[string]bool, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, errors.New("invalid listing envelope")
	}
	data, ok := envelope["data"]
	if !ok || len(data) == 0 || data[0] != '[' {
		return nil, errors.New("listing requires data array")
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return nil, errors.New("malformed listing ids")
	}
	ids := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			return nil, errors.New("listing id is empty")
		}
		ids[entry.ID] = true
	}
	return ids, nil
}
func terminalResponse(dialect string, raw []byte) (string, bool, bool) {
	var r struct {
		Model      string `json:"model"`
		Type       string `json:"type"`
		Status     string `json:"status"`
		StopReason string `json:"stop_reason"`
		Choices    []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", false, false
	}
	switch dialect {
	case "chat":
		if len(r.Choices) > 0 {
			return r.Model, r.Choices[0].FinishReason == "stop", strings.TrimSpace(r.Choices[0].Message.Content) != ""
		}
	case "messages":
		content := false
		for _, c := range r.Content {
			content = content || c.Type == "text" && strings.TrimSpace(c.Text) != ""
		}
		return r.Model, r.Type == "message" && r.StopReason == "end_turn", content
	case "responses":
		content := false
		for _, o := range r.Output {
			for _, c := range o.Content {
				content = content || o.Type == "message" && c.Type == "output_text" && strings.TrimSpace(c.Text) != ""
			}
		}
		return r.Model, r.Status == "completed", content
	}
	return r.Model, false, false
}

func marshalRegister(r register) ([]byte, error) { return yaml.Marshal(r) }
func escalateGateway(root string) error {
	path := evidencePath + "/register.yaml"
	raw, err := readRepoFile(root, path)
	if err != nil {
		return errors.New("cannot read register for escalation")
	}
	r, err := parseRegister(raw)
	if err != nil {
		return err
	}
	item, ok := r.Items["LR-01"]
	if !ok {
		return errors.New("LR-01 missing from register")
	}
	item.Status = "escalated"
	item.Escalation = "Direct live gateway regression failed for a decided path, Bearer auth, envelope or terminal response. Review the safe gateway observation and resolve the contract before release; hosted constants remain unchanged."
	r.Items["LR-01"] = item
	raw, err = marshalRegister(r)
	if err != nil {
		return errors.New("cannot encode escalation")
	}
	p, err := safeRepoPath(root, path)
	if err != nil {
		return err
	}
	if os.WriteFile(p, raw, 0600) != nil {
		return errors.New("cannot persist LR-01 escalation")
	}
	return nil
}

func TestLiveGatewayContract(t *testing.T) {
	if hostedRoot != provider.LunaRouteHostedRoot {
		t.Fatal("gateway experiment and production hosted root differ")
	}
	key := liveKey(t)
	models := gatewayModels{Chat: os.Getenv("ARBITRON_LUNAROUTE_E2E_CHAT_MODEL"), Messages: os.Getenv("ARBITRON_LUNAROUTE_E2E_CLAUDE_MODEL"), Responses: os.Getenv("ARBITRON_LUNAROUTE_E2E_CODEX_MODEL")}
	if models.Chat == "" || models.Messages == "" || models.Responses == "" {
		if os.Getenv("ARBITRON_LUNAROUTE_RELEASE_GATE") == "1" {
			t.Fatal("explicit CHAT_MODEL, CLAUDE_MODEL and CODEX_MODEL live inputs required")
		}
		t.Skip("explicit reviewed models required for all three dialects; release acceptance remains pending")
	}
	for _, model := range []string{models.Chat, models.Messages, models.Responses} {
		if _, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: model}); err != nil {
			t.Fatal("live input requires reviewed executable ids; nothing sent, verification remains pending")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	observations, err := runGatewayContract(ctx, liveClient(), hostedRoot, key, models)
	// No status changes on missing prerequisites or local HTTP fixture tests.
	// A real attempted regression failure escalates LR-01, never edits constants.
	name := "lr01-gateway-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
	if err != nil {
		escalationErr := escalateGateway(repoRoot(t))
		if len(observations) > 0 {
			writeObservation(t, key, name, observations)
		}
		if escalationErr != nil {
			t.Fatal("live contract failed and LR-01 escalation could not be persisted")
		}
		t.Fatal(err.Error())
	}
	path := writeObservation(t, key, name, observations)
	t.Log("credential-free gateway observation: " + filepath.ToSlash(path) + "; human review and runtime application still required")
}
