package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
)

// happy-path: user message wrapped in a content block; no system, no inferenceConfig.
func TestBuildBody_MinimalUserOnly(t *testing.T) {
	raw, err := buildBody(engine.ChatRequest{Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "user" {
		t.Errorf("role = %v, want user", m0["role"])
	}
	content := m0["content"].([]any)
	if content[0].(map[string]any)["text"] != "hello" {
		t.Errorf("content text = %v, want hello", content[0])
	}
	if _, ok := got["system"]; ok {
		t.Error("system must be omitted when empty")
	}
	if _, ok := got["inferenceConfig"]; ok {
		t.Error("inferenceConfig must be omitted when all-zero")
	}
}

// edge-case: system prompt -> system[].text; max tokens + temperature -> inferenceConfig.
func TestBuildBody_SystemAndInferenceConfig(t *testing.T) {
	raw, err := buildBody(engine.ChatRequest{
		SystemPrompt: "be brief", Prompt: "hi", MaxTokens: 256, Temperature: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	sys := got["system"].([]any)
	if sys[0].(map[string]any)["text"] != "be brief" {
		t.Errorf("system text = %v", sys[0])
	}
	ic := got["inferenceConfig"].(map[string]any)
	if ic["maxTokens"].(float64) != 256 {
		t.Errorf("maxTokens = %v, want 256", ic["maxTokens"])
	}
	if ic["temperature"].(float64) != 0.5 {
		t.Errorf("temperature = %v, want 0.5", ic["temperature"])
	}
}

// edge-case: zero temperature is omitted (matches openrouter buildBody semantics).
func TestBuildBody_ZeroTemperatureOmitted(t *testing.T) {
	raw, _ := buildBody(engine.ChatRequest{Prompt: "x", MaxTokens: 10})
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	ic := got["inferenceConfig"].(map[string]any)
	if _, ok := ic["temperature"]; ok {
		t.Error("temperature must be omitted when zero")
	}
}
