# LLM Providers — Phase 3, Task 2: Bedrock Converse Adapter, Factory Routing & ValidateConnection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the Bedrock chat `Provider` (Converse + converse-stream) that signs requests with the Task-1 SigV4 signer, derives its regional endpoint from `Extras["region"]` + `Model`, maps Converse responses/streams to `engine.ChatResponse`/`engine.ChatChunk`, plus the factory routing (`bedrock` / `amazon-bedrock`) and the `ValidateConnection` bedrock rule (region + access-key-id required).

**Architecture:** New `adapter/bedrock` files `provider.go`, `endpoint.go`, `request.go`, `response.go` reuse the established HTTP-adapter shape (injectable `Config{Binding, HTTPClient, Timeout, MaxResponseBytes}`, bounded reader, `scrubControl` on error snippets, `ErrUpstreamStatus`). Unlike `openrouter`, Bedrock derives its own endpoint and **signs** each request (SigV4) instead of setting a Bearer header. The factory adds a dedicated `bedrock` arm; `ValidateConnection` gains a bedrock rule beside the Azure `api-version` rule.

**Tech Stack:** Go stdlib (`net/http`, `net/http/httptest`, `encoding/json`, `bufio`, `context`, `io`). Reuses `domain/engine`, `domain/provider`, `adapter/procenv`. Depends on Task-1 `signRequest`/`parseCredential`/`credential`. Tests are hermetic (`httptest.Server`), `make test` == `go test ./...`.

---

## Dependencies

- **Upstream (required):** Phase-3 **Task 1** — `signRequest`, `parseCredential`, `credential` in `adapter/bedrock/{sigv4.go,credentials.go}`. Do this task only after Task 1 is green.
- **Reference symbols (read, mirror, do not break):**
  - `adapter/openrouter/provider.go` — `Config`, `Provider`, `boundedReader`, `limitedReader`, `errResponseTooLarge`, `statusError`, `scrubControl`, `send`; replicate these patterns (each adapter owns its own copies today).
  - `adapter/openrouter/response.go` — `ErrUpstreamStatus` sentinel pattern.
  - `domain/engine/ports.go` — `ChatProvider`, `ChatRequest{Model,SystemPrompt,Prompt,MaxTokens,Temperature,OutputSchema}`, `ChatResponse{Output,Model,TokensIn,TokensOut}`, `ChatChunk{Token,Done,Error,TokensIn,TokensOut}`.
  - `domain/provider/provider.go` — `ResolvedBinding{Provider,BaseURL,Extras,Model,RevealKey}`.
  - `domain/provider/connection.go` — `ValidateConnection`, `ErrAzureAPIVersionRequired`, `azureProviders` (the rule to mirror).
  - `adapter/provider/factory.go` — `ChatProviderFor`, provider const block, `normalizeProvider`.
  - `cmd/arbitron/main.go` ~464-476 — `Deps` wiring (no new field needed; bedrock reuses `HTTPClient`, `HTTPTimeout`, `MaxResponseBytes`).

## File Structure

- Create: `adapter/bedrock/endpoint.go` — `endpointURL`, sentinels `ErrMissingRegion`, `ErrMissingModel`.
- Create: `adapter/bedrock/request.go` — `buildBody` (Converse JSON).
- Create: `adapter/bedrock/response.go` — Converse decode types, `decodeConverse`, `ErrUpstreamStatus`, stream-event decode (`parseConverseEvent`).
- Create: `adapter/bedrock/provider.go` — `Config`, `Provider`, `New`, `Chat`, `StreamChat`, `boundedReader`/`limitedReader`/`errResponseTooLarge`, `statusError`, `scrubControl`, `send`, plus `signedRequest` (build+sign helper) and `ErrMissingAccessKeyID`.
- Modify: `adapter/provider/factory.go` — add `providerBedrock`/`providerAmazonBedrock` consts + a `case` in `ChatProviderFor`.
- Modify: `domain/provider/connection.go` — add `ErrBedrockCredentialsRequired` + bedrock branch in `ValidateConnection`.
- Test: `adapter/bedrock/endpoint_test.go`, `adapter/bedrock/request_test.go`, `adapter/bedrock/response_test.go`, `adapter/bedrock/provider_test.go`
- Test: `adapter/provider/factory_test.go` (extend), `domain/provider/connection_test.go` (extend).

## Precise Signatures (defined in this task)

```go
// adapter/bedrock/endpoint.go
package bedrock

var ErrMissingRegion = errors.New("bedrock: binding Extras[\"region\"] is empty")
var ErrMissingModel  = errors.New("bedrock: binding Model (modelId) is empty")

// endpointURL derives the regional Converse endpoint from Extras["region"] and
// Model. stream=false → /converse, stream=true → /converse-stream:
//   https://bedrock-runtime.{region}.amazonaws.com/model/{modelId}/converse[-stream]
// The modelId is path-escaped (model ids contain dots and, for inference
// profiles, can contain ':' and '/'). Empty region/model are rejected.
func endpointURL(b provider.ResolvedBinding, stream bool) (string, error)
```

```go
// adapter/bedrock/request.go
package bedrock

// buildBody assembles the Bedrock Converse request JSON from a ChatRequest:
//   {"messages":[{"role":"user","content":[{"text":...}]}],
//    "system":[{"text":...}]?,                              // omitted when empty
//    "inferenceConfig":{"maxTokens":N,"temperature":T}?}    // fields omitted when zero
// Converse takes the model id in the URL path, NOT the body, so model is not
// embedded here. OutputSchema is not part of Converse's body; it is ignored at
// the adapter (the domain executor remains the sole structured-output parser).
func buildBody(req engine.ChatRequest) ([]byte, error)
```

```go
// adapter/bedrock/response.go
package bedrock

var ErrUpstreamStatus = errors.New("bedrock: upstream returned non-2xx status")

// decodeConverse parses a non-streaming Converse response and returns the joined
// assistant text and token usage. It rejects an empty body or a response whose
// output.message has no text content.
//   output.message.content[].text  (joined), usage.inputTokens, usage.outputTokens
func decodeConverse(body []byte) (text string, tokensIn, tokensOut int, err error)

// parseConverseEvent decodes one Bedrock Converse event-stream JSON frame
// (one line of the simplified test stream) into a token delta, optional usage,
// and a done flag. The production Converse stream is AWS event-stream framed;
// the adapter consumes the decoded JSON event objects. Recognized events:
//   contentBlockDelta -> delta.text (token)
//   metadata          -> usage.inputTokens / outputTokens
//   messageStop       -> done=true
func parseConverseEvent(line []byte) (delta string, tokensIn, tokensOut int, done bool, err error)
```

```go
// adapter/bedrock/provider.go
package bedrock

var ErrMissingAccessKeyID = errors.New("bedrock: binding Extras[\"access-key-id\"] is empty")

type Config struct {
    Binding          provider.ResolvedBinding
    HTTPClient       *http.Client
    Timeout          time.Duration
    MaxResponseBytes int64
    // Now is the injectable clock used for SigV4 X-Amz-Date. nil → time.Now.
    Now func() time.Time
}

type Provider struct { /* binding, httpClient, timeout, maxResponseBytes, now */ }
var _ engine.ChatProvider = (*Provider)(nil)

func New(cfg Config) *Provider
func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)
func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)

// signedRequest builds the POST to the derived endpoint, reveals+parses the
// sealed credential, and SigV4-signs it. region/access-key-id come from Extras;
// the secret/session token come from RevealKey via parseCredential. It guards
// missing region/access-key-id BEFORE revealing the secret.
func (p *Provider) signedRequest(ctx context.Context, body []byte, stream bool) (*http.Request, error)
```

```go
// domain/provider/connection.go (additions)
var ErrBedrockCredentialsRequired = errors.New("bedrock connections require region and access-key-id in extras")

// In ValidateConnection: for provider "bedrock", require non-empty
// Extras["region"] AND Extras["access-key-id"], else ErrBedrockCredentialsRequired.
```

```go
// adapter/provider/factory.go (additions)
const (
    providerBedrock       = "bedrock"
    providerAmazonBedrock = "amazon-bedrock" // normalized → bedrock
)
// ChatProviderFor case: providerBedrock, providerAmazonBedrock → bedrock.New(...)
```

---

## Task 2A: `ValidateConnection` bedrock rule

**Files:**
- Modify: `domain/provider/connection.go`
- Test: `domain/provider/connection_test.go`

- [ ] **Step 1: Write the failing tests (input-validation category)**

```go
// append to domain/provider/connection_test.go
func TestValidateConnection_BedrockRequiresRegionAndAccessKey(t *testing.T) {
    cases := []struct {
        name   string
        extras map[string]string
        wantOK bool
    }{
        {"both present", map[string]string{"region": "us-east-1", "access-key-id": "AKID"}, true},
        {"missing region", map[string]string{"access-key-id": "AKID"}, false},
        {"missing access-key-id", map[string]string{"region": "us-east-1"}, false},
        {"empty region value", map[string]string{"region": "", "access-key-id": "AKID"}, false},
        {"nil extras", nil, false},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            err := provider.ValidateConnection(provider.Connection{
                Name: "bd", Provider: "bedrock", Extras: tc.extras,
            })
            if tc.wantOK && err != nil {
                t.Fatalf("want ok, got %v", err)
            }
            if !tc.wantOK && !errors.Is(err, provider.ErrBedrockCredentialsRequired) {
                t.Fatalf("want ErrBedrockCredentialsRequired, got %v", err)
            }
        })
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./domain/provider/ -run TestValidateConnection_Bedrock -v`
Expected: FAIL — `undefined: provider.ErrBedrockCredentialsRequired`.

- [ ] **Step 3: Implement the rule**

```go
// in domain/provider/connection.go, beside ErrAzureAPIVersionRequired:
var ErrBedrockCredentialsRequired = errors.New("bedrock connections require region and access-key-id in extras")

// in ValidateConnection, before the final `return nil`:
    if c.Provider == "bedrock" {
        if c.Extras["region"] == "" || c.Extras["access-key-id"] == "" {
            return ErrBedrockCredentialsRequired
        }
    }
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./domain/provider/ -run TestValidateConnection_Bedrock -v`
Expected: PASS (all five subtests).

- [ ] **Step 5: Commit**

```bash
git add domain/provider/connection.go domain/provider/connection_test.go
git commit -m "feat(provider): ValidateConnection rule for bedrock region+access-key-id

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2B: Endpoint derivation

**Files:**
- Create: `adapter/bedrock/endpoint.go`
- Test: `adapter/bedrock/endpoint_test.go`

- [ ] **Step 1: Write the failing tests (happy-path, edge-cases, input-validation)**

```go
// adapter/bedrock/endpoint_test.go
package bedrock

import (
    "errors"
    "testing"

    "github.com/arbitron/arbitron/domain/provider"
)

func binding(region, model string) provider.ResolvedBinding {
    return provider.ResolvedBinding{
        Provider: "bedrock",
        Extras:   map[string]string{"region": region, "access-key-id": "AKID"},
        Model:    model,
    }
}

// happy-path: region drives the host; non-stream uses /converse.
func TestEndpointURL_Converse(t *testing.T) {
    got, err := endpointURL(binding("us-east-1", "anthropic.claude-3-sonnet-20240229-v1:0"), false)
    if err != nil {
        t.Fatal(err)
    }
    const want = "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-sonnet-20240229-v1%3A0/converse"
    if got != want {
        t.Errorf("got  %q\nwant %q", got, want)
    }
}

// edge-case: streaming variant appends /converse-stream.
func TestEndpointURL_ConverseStream(t *testing.T) {
    got, err := endpointURL(binding("eu-west-1", "anthropic.claude-v2"), true)
    if err != nil {
        t.Fatal(err)
    }
    const want = "https://bedrock-runtime.eu-west-1.amazonaws.com/model/anthropic.claude-v2/converse-stream"
    if got != want {
        t.Errorf("got %q want %q", got, want)
    }
}

// edge-case: a different region changes only the host segment.
func TestEndpointURL_RegionDrivesHost(t *testing.T) {
    got, _ := endpointURL(binding("ap-southeast-2", "amazon.titan-text-express-v1"), false)
    const want = "https://bedrock-runtime.ap-southeast-2.amazonaws.com/model/amazon.titan-text-express-v1/converse"
    if got != want {
        t.Errorf("got %q want %q", got, want)
    }
}

// input-validation: empty region rejected.
func TestEndpointURL_MissingRegion(t *testing.T) {
    if _, err := endpointURL(binding("", "m"), false); !errors.Is(err, ErrMissingRegion) {
        t.Fatalf("err = %v, want ErrMissingRegion", err)
    }
}

// input-validation: empty model rejected.
func TestEndpointURL_MissingModel(t *testing.T) {
    if _, err := endpointURL(binding("us-east-1", "  "), false); !errors.Is(err, ErrMissingModel) {
        t.Fatalf("err = %v, want ErrMissingModel", err)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./adapter/bedrock/ -run TestEndpointURL -v`
Expected: FAIL — `undefined: endpointURL`.

- [ ] **Step 3: Implement**

```go
// adapter/bedrock/endpoint.go
package bedrock

import (
    "errors"
    "net/url"
    "strings"

    "github.com/arbitron/arbitron/domain/provider"
)

var ErrMissingRegion = errors.New("bedrock: binding Extras[\"region\"] is empty")
var ErrMissingModel = errors.New("bedrock: binding Model (modelId) is empty")

func endpointURL(b provider.ResolvedBinding, stream bool) (string, error) {
    region := strings.TrimSpace(b.Extras["region"])
    if region == "" {
        return "", ErrMissingRegion
    }
    model := strings.TrimSpace(b.Model)
    if model == "" {
        return "", ErrMissingModel
    }
    action := "converse"
    if stream {
        action = "converse-stream"
    }
    return "https://bedrock-runtime." + region + ".amazonaws.com/model/" +
        url.PathEscape(model) + "/" + action, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./adapter/bedrock/ -run TestEndpointURL -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/bedrock/endpoint.go adapter/bedrock/endpoint_test.go
git commit -m "feat(bedrock): regional Converse endpoint derivation

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2C: Converse request body

**Files:**
- Create: `adapter/bedrock/request.go`
- Test: `adapter/bedrock/request_test.go`

- [ ] **Step 1: Write the failing tests (happy-path + edge-cases)**

```go
// adapter/bedrock/request_test.go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./adapter/bedrock/ -run TestBuildBody -v`
Expected: FAIL — `undefined: buildBody`.

- [ ] **Step 3: Implement**

```go
// adapter/bedrock/request.go
package bedrock

import (
    "encoding/json"
    "fmt"

    "github.com/arbitron/arbitron/domain/engine"
)

func buildBody(req engine.ChatRequest) ([]byte, error) {
    type textBlock struct {
        Text string `json:"text"`
    }
    type message struct {
        Role    string      `json:"role"`
        Content []textBlock `json:"content"`
    }

    body := map[string]any{
        "messages": []message{
            {Role: "user", Content: []textBlock{{Text: req.Prompt}}},
        },
    }
    if req.SystemPrompt != "" {
        body["system"] = []textBlock{{Text: req.SystemPrompt}}
    }
    inference := map[string]any{}
    if req.MaxTokens > 0 {
        inference["maxTokens"] = req.MaxTokens
    }
    if req.Temperature != 0 {
        inference["temperature"] = req.Temperature
    }
    if len(inference) > 0 {
        body["inferenceConfig"] = inference
    }

    out, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("bedrock: marshal converse body: %w", err)
    }
    return out, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./adapter/bedrock/ -run TestBuildBody -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/bedrock/request.go adapter/bedrock/request_test.go
git commit -m "feat(bedrock): Converse request body builder

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2D: Converse response + stream-event decode

**Files:**
- Create: `adapter/bedrock/response.go`
- Test: `adapter/bedrock/response_test.go`

- [ ] **Step 1: Write the failing tests (happy-path, error-handling, edge-cases)**

```go
// adapter/bedrock/response_test.go
package bedrock

import (
    "testing"
)

// happy-path: joined assistant text + usage.
func TestDecodeConverse_HappyPath(t *testing.T) {
    body := []byte(`{
      "output":{"message":{"role":"assistant","content":[{"text":"Hello "},{"text":"world"}]}},
      "usage":{"inputTokens":11,"outputTokens":7}
    }`)
    text, in, out, err := decodeConverse(body)
    if err != nil {
        t.Fatal(err)
    }
    if text != "Hello world" {
        t.Errorf("text = %q, want %q", text, "Hello world")
    }
    if in != 11 || out != 7 {
        t.Errorf("tokens = %d/%d, want 11/7", in, out)
    }
}

// error-handling: empty body rejected.
func TestDecodeConverse_EmptyBody(t *testing.T) {
    if _, _, _, err := decodeConverse([]byte("  ")); err == nil {
        t.Fatal("expected error for empty body")
    }
}

// error-handling: malformed JSON rejected.
func TestDecodeConverse_MalformedJSON(t *testing.T) {
    if _, _, _, err := decodeConverse([]byte(`{"output": BROKEN`)); err == nil {
        t.Fatal("expected error for malformed JSON")
    }
}

// edge-case: a message with no text content is rejected (no silent empty result).
func TestDecodeConverse_NoTextContent(t *testing.T) {
    body := []byte(`{"output":{"message":{"role":"assistant","content":[]}},"usage":{}}`)
    if _, _, _, err := decodeConverse(body); err == nil {
        t.Fatal("expected error when message has no text content")
    }
}

// happy-path: contentBlockDelta -> token, metadata -> usage, messageStop -> done.
func TestParseConverseEvent_DeltaUsageStop(t *testing.T) {
    delta, _, _, _, err := parseConverseEvent([]byte(`{"contentBlockDelta":{"delta":{"text":"tok"}}}`))
    if err != nil || delta != "tok" {
        t.Fatalf("delta = %q err = %v", delta, err)
    }
    _, in, out, _, err := parseConverseEvent([]byte(`{"metadata":{"usage":{"inputTokens":3,"outputTokens":4}}}`))
    if err != nil || in != 3 || out != 4 {
        t.Fatalf("usage = %d/%d err = %v", in, out, err)
    }
    _, _, _, done, err := parseConverseEvent([]byte(`{"messageStop":{"stopReason":"end_turn"}}`))
    if err != nil || !done {
        t.Fatalf("done = %v err = %v", done, err)
    }
}

// edge-case: an unrecognized/blank event is ignored (all-zero return, no error).
func TestParseConverseEvent_IgnoredEvent(t *testing.T) {
    delta, in, out, done, err := parseConverseEvent([]byte(`{"messageStart":{"role":"assistant"}}`))
    if err != nil || delta != "" || in != 0 || out != 0 || done {
        t.Fatalf("unexpected non-zero for ignored event: %q %d %d %v %v", delta, in, out, done, err)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./adapter/bedrock/ -run 'TestDecodeConverse|TestParseConverseEvent' -v`
Expected: FAIL — `undefined: decodeConverse`, `undefined: parseConverseEvent`.

- [ ] **Step 3: Implement**

```go
// adapter/bedrock/response.go
package bedrock

import (
    "bytes"
    "encoding/json"
    "errors"
    "fmt"
    "strings"
)

var ErrUpstreamStatus = errors.New("bedrock: upstream returned non-2xx status")

type converseText struct {
    Text string `json:"text"`
}
type converseMessage struct {
    Role    string         `json:"role"`
    Content []converseText `json:"content"`
}
type converseUsage struct {
    InputTokens  int `json:"inputTokens"`
    OutputTokens int `json:"outputTokens"`
}
type converseResponse struct {
    Output struct {
        Message converseMessage `json:"message"`
    } `json:"output"`
    Usage converseUsage `json:"usage"`
}

func decodeConverse(body []byte) (string, int, int, error) {
    if len(bytes.TrimSpace(body)) == 0 {
        return "", 0, 0, errors.New("bedrock: empty response body")
    }
    var r converseResponse
    if err := json.Unmarshal(body, &r); err != nil {
        return "", 0, 0, fmt.Errorf("bedrock: decode converse response: %w", err)
    }
    var b strings.Builder
    found := false
    for _, c := range r.Output.Message.Content {
        if c.Text != "" {
            b.WriteString(c.Text)
            found = true
        }
    }
    if !found {
        return "", 0, 0, errors.New("bedrock: response has no text content")
    }
    return b.String(), r.Usage.InputTokens, r.Usage.OutputTokens, nil
}

type converseEvent struct {
    ContentBlockDelta *struct {
        Delta converseText `json:"delta"`
    } `json:"contentBlockDelta"`
    Metadata *struct {
        Usage converseUsage `json:"usage"`
    } `json:"metadata"`
    MessageStop *struct {
        StopReason string `json:"stopReason"`
    } `json:"messageStop"`
}

func parseConverseEvent(line []byte) (delta string, tokensIn, tokensOut int, done bool, err error) {
    line = bytes.TrimSpace(line)
    if len(line) == 0 {
        return "", 0, 0, false, nil
    }
    var e converseEvent
    if err := json.Unmarshal(line, &e); err != nil {
        return "", 0, 0, false, fmt.Errorf("bedrock: decode stream event: %w", err)
    }
    if e.ContentBlockDelta != nil {
        delta = e.ContentBlockDelta.Delta.Text
    }
    if e.Metadata != nil {
        tokensIn = e.Metadata.Usage.InputTokens
        tokensOut = e.Metadata.Usage.OutputTokens
    }
    if e.MessageStop != nil {
        done = true
    }
    return delta, tokensIn, tokensOut, done, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./adapter/bedrock/ -run 'TestDecodeConverse|TestParseConverseEvent' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/bedrock/response.go adapter/bedrock/response_test.go
git commit -m "feat(bedrock): Converse response + stream-event decode

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2E: Provider (Chat + StreamChat, signing, bounded reader, no-leak errors)

**Files:**
- Create: `adapter/bedrock/provider.go`
- Test: `adapter/bedrock/provider_test.go`

- [ ] **Step 1: Write the failing tests (happy-path, error-handling, security via httptest)**

```go
// adapter/bedrock/provider_test.go
package bedrock

import (
    "context"
    "errors"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/arbitron/arbitron/domain/engine"
    "github.com/arbitron/arbitron/domain/provider"
)

const testSecret = "TOP-SECRET-ACCESS-KEY"
const testSession = "TOP-SECRET-SESSION-TOKEN"

func fixedNow() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }

// bindingTo points a bedrock binding at srv, supplying a sealed bare secret.
func bindingTo(srvURL, region, model string, withSession bool) provider.ResolvedBinding {
    sealed := testSecret
    if withSession {
        sealed = `{"secret":"` + testSecret + `","session_token":"` + testSession + `"}`
    }
    return provider.ResolvedBinding{
        Provider: "bedrock",
        Extras:   map[string]string{"region": region, "access-key-id": "AKID"},
        Model:    model,
        // BaseURL is unused by bedrock (endpoint is derived); kept empty.
        RevealKey: func(context.Context) (string, error) { return sealed, nil },
    }
}

// newProvider builds a Provider whose HTTP client is rerouted to srv via a
// rewriting transport, so the derived bedrock-runtime URL hits the test server.
func newProvider(t *testing.T, srv *httptest.Server, b provider.ResolvedBinding, maxBytes int64) *Provider {
    t.Helper()
    return New(Config{
        Binding:          b,
        HTTPClient:       &http.Client{Transport: rewriteTo(srv.URL)},
        MaxResponseBytes: maxBytes,
        Now:              fixedNow,
    })
}

// rewriteTo returns a RoundTripper that redirects every request to base (the
// httptest server) while preserving the original path — so SigV4 signs the real
// bedrock host but the bytes hit the local server. It also records the outgoing
// request for header assertions.
type recordingTransport struct {
    base string
    last *http.Request
}

func rewriteTo(base string) *recordingTransport { return &recordingTransport{base: base} }

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    rt.last = r.Clone(r.Context())
    target, _ := http.NewRequestWithContext(r.Context(), r.Method, rt.base+r.URL.Path, r.Body)
    target.Header = r.Header
    return http.DefaultTransport.RoundTrip(target)
}

// happy-path: Chat returns assistant text + usage; request is SigV4-signed.
func TestChat_HappyPath(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
            t.Errorf("missing SigV4 Authorization: %q", r.Header.Get("Authorization"))
        }
        if r.Header.Get("X-Amz-Date") == "" {
            t.Error("missing X-Amz-Date")
        }
        _, _ = io.WriteString(w, `{"output":{"message":{"content":[{"text":"hi there"}]}},"usage":{"inputTokens":2,"outputTokens":3}}`)
    }))
    defer srv.Close()

    p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "anthropic.claude-v2", false), 0)
    resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
    if err != nil {
        t.Fatal(err)
    }
    if resp.Output != "hi there" || resp.TokensIn != 2 || resp.TokensOut != 3 {
        t.Errorf("resp = %+v", resp)
    }
}

// error-handling: 403 -> ErrUpstreamStatus (config-class auth), secret NOT in error.
func TestChat_403AuthError_NoSecretLeak(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusForbidden)
        _, _ = io.WriteString(w, `{"message":"The security token included in the request is invalid"}`)
    }))
    defer srv.Close()

    p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", true), 0)
    _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
    if !errors.Is(err, ErrUpstreamStatus) {
        t.Fatalf("err = %v, want ErrUpstreamStatus", err)
    }
    if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testSession) {
        t.Errorf("error leaked a secret: %v", err)
    }
}

// error-handling: 400 (bad model) and 429/500 map to ErrUpstreamStatus.
func TestChat_StatusErrorsMapped(t *testing.T) {
    for _, code := range []int{400, 429, 500} {
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(code)
            _, _ = io.WriteString(w, `{"message":"nope"}`)
        }))
        p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 0)
        _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
        if !errors.Is(err, ErrUpstreamStatus) {
            t.Errorf("status %d: err = %v, want ErrUpstreamStatus", code, err)
        }
        srv.Close()
    }
}

// input-validation: missing region guarded BEFORE revealing the secret.
func TestChat_MissingRegionGuarded(t *testing.T) {
    revealed := false
    b := provider.ResolvedBinding{
        Provider:  "bedrock",
        Extras:    map[string]string{"access-key-id": "AKID"}, // no region
        Model:     "m",
        RevealKey: func(context.Context) (string, error) { revealed = true; return testSecret, nil },
    }
    p := New(Config{Binding: b, Now: fixedNow})
    if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); !errors.Is(err, ErrMissingRegion) {
        t.Fatalf("err = %v, want ErrMissingRegion", err)
    }
    if revealed {
        t.Error("secret was revealed despite a missing-region guard failure")
    }
}

// input-validation: missing access-key-id guarded.
func TestChat_MissingAccessKeyIDGuarded(t *testing.T) {
    b := provider.ResolvedBinding{
        Provider:  "bedrock",
        Extras:    map[string]string{"region": "us-east-1"}, // no access-key-id
        Model:     "m",
        RevealKey: func(context.Context) (string, error) { return testSecret, nil },
    }
    p := New(Config{Binding: b, Now: fixedNow})
    if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); !errors.Is(err, ErrMissingAccessKeyID) {
        t.Fatalf("err = %v, want ErrMissingAccessKeyID", err)
    }
}

// security: response body capped by MaxResponseBytes (OOM DoS guard).
func TestChat_MaxResponseBytesCap(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.WriteString(w, strings.Repeat("A", 1024)) // 1KiB > 16B cap
    }))
    defer srv.Close()
    p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 16)
    if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); err == nil {
        t.Fatal("expected an over-large-body error with a 16-byte cap")
    }
}

// security: the secret/session token are signed in but never appear in any
// outgoing header other than the (hex) signature / X-Amz-Security-Token.
func TestChat_NoSecretInOutgoingHeaders(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.WriteString(w, `{"output":{"message":{"content":[{"text":"ok"}]}},"usage":{}}`)
    }))
    defer srv.Close()
    rt := rewriteTo(srv.URL)
    b := bindingTo(srv.URL, "us-east-1", "m", true)
    p := New(Config{Binding: b, HTTPClient: &http.Client{Transport: rt}, Now: fixedNow})
    if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); err != nil {
        t.Fatal(err)
    }
    for name, vals := range rt.last.Header {
        if http.CanonicalHeaderKey(name) == "X-Amz-Security-Token" {
            continue
        }
        for _, v := range vals {
            if strings.Contains(v, testSecret) {
                t.Errorf("header %q leaked secret-access-key: %q", name, v)
            }
            if strings.Contains(v, testSession) {
                t.Errorf("header %q leaked session token: %q", name, v)
            }
        }
    }
    if rt.last.Header.Get("X-Amz-Security-Token") != testSession {
        t.Error("X-Amz-Security-Token should carry the session token")
    }
}

// happy-path: StreamChat yields ordered tokens then a Done chunk with usage.
func TestStreamChat_OrderedTokensThenDone(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.WriteString(w, strings.Join([]string{
            `{"contentBlockDelta":{"delta":{"text":"Hel"}}}`,
            `{"contentBlockDelta":{"delta":{"text":"lo"}}}`,
            `{"metadata":{"usage":{"inputTokens":5,"outputTokens":6}}}`,
            `{"messageStop":{"stopReason":"end_turn"}}`,
        }, "\n"))
    }))
    defer srv.Close()
    p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 0)
    ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
    if err != nil {
        t.Fatal(err)
    }
    var tokens []string
    var done engine.ChatChunk
    for c := range ch {
        if c.Error != nil {
            t.Fatalf("stream error: %v", c.Error)
        }
        if c.Done {
            done = c
            continue
        }
        tokens = append(tokens, c.Token)
    }
    if strings.Join(tokens, "") != "Hello" {
        t.Errorf("tokens = %v, want Hello", tokens)
    }
    if !done.Done || done.TokensIn != 5 || done.TokensOut != 6 {
        t.Errorf("done = %+v, want Done with usage 5/6", done)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./adapter/bedrock/ -run 'TestChat|TestStreamChat' -v`
Expected: FAIL — `undefined: New` / `undefined: ErrMissingAccessKeyID`.

- [ ] **Step 3: Implement the Provider**

```go
// adapter/bedrock/provider.go
// Package bedrock implements engine.ChatProvider against the Amazon Bedrock
// Converse API. Unlike the OpenAI-compatible adapters it derives its own
// regional endpoint and SIGNS each request with AWS SigV4 (service "bedrock")
// rather than setting a Bearer header. The HTTP and clock seams are injectable
// so tests run hermetically against httptest with a deterministic signature.
package bedrock

import (
    "bufio"
    "bytes"
    "context"
    "errors"
    "fmt"
    "io"
    "net/http"
    "strings"
    "time"

    "github.com/arbitron/arbitron/adapter/procenv"
    "github.com/arbitron/arbitron/domain/engine"
    "github.com/arbitron/arbitron/domain/provider"
)

var ErrMissingAccessKeyID = errors.New("bedrock: binding Extras[\"access-key-id\"] is empty")

const sigService = "bedrock"

type Config struct {
    Binding          provider.ResolvedBinding
    HTTPClient       *http.Client
    Timeout          time.Duration
    MaxResponseBytes int64
    Now              func() time.Time
}

type Provider struct {
    binding          provider.ResolvedBinding
    httpClient       *http.Client
    timeout          time.Duration
    maxResponseBytes int64
    now              func() time.Time
}

var _ engine.ChatProvider = (*Provider)(nil)

func New(cfg Config) *Provider {
    hc := cfg.HTTPClient
    if hc == nil {
        hc = &http.Client{}
    }
    now := cfg.Now
    if now == nil {
        now = time.Now
    }
    return &Provider{
        binding:          cfg.Binding,
        httpClient:       hc,
        timeout:          cfg.Timeout,
        maxResponseBytes: cfg.MaxResponseBytes,
        now:              now,
    }
}

var errResponseTooLarge = errors.New("bedrock: response exceeds MaxResponseBytes")

type limitedReader struct {
    r         io.Reader
    remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
    if l.remaining <= 0 {
        return 0, errResponseTooLarge
    }
    if int64(len(p)) > l.remaining+1 {
        p = p[:l.remaining+1]
    }
    n, err := l.r.Read(p)
    l.remaining -= int64(n)
    if l.remaining < 0 {
        return int(l.remaining + int64(n)), errResponseTooLarge
    }
    return n, err
}

func (p *Provider) boundedReader(r io.Reader) io.Reader {
    if p.maxResponseBytes <= 0 {
        return r
    }
    return &limitedReader{r: r, remaining: p.maxResponseBytes}
}

// signedRequest derives the endpoint, guards region/access-key-id BEFORE
// revealing the secret, reveals + parses the sealed credential, and SigV4-signs
// the POST. The revealed secret is used only by signRequest.
func (p *Provider) signedRequest(ctx context.Context, body []byte, stream bool) (*http.Request, error) {
    accessKeyID := strings.TrimSpace(p.binding.Extras["access-key-id"])
    if accessKeyID == "" {
        return nil, ErrMissingAccessKeyID
    }
    region := strings.TrimSpace(p.binding.Extras["region"])
    if region == "" {
        return nil, ErrMissingRegion
    }
    target, err := endpointURL(p.binding, stream)
    if err != nil {
        return nil, err
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("bedrock: build request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")

    sealed := ""
    if p.binding.RevealKey != nil {
        sealed, err = p.binding.RevealKey(ctx)
        if err != nil {
            return nil, fmt.Errorf("bedrock: reveal key: %w", err)
        }
    }
    cred, err := parseCredential(sealed)
    if err != nil {
        return nil, err
    }
    if err := signRequest(httpReq, body, accessKeyID, cred, region, sigService, p.now()); err != nil {
        return nil, err
    }
    return httpReq, nil
}

func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
    ctx, cancel := procenv.WithTimeout(ctx, p.timeout)
    defer cancel()

    body, err := buildBody(req)
    if err != nil {
        return nil, err
    }
    httpReq, err := p.signedRequest(ctx, body, false)
    if err != nil {
        return nil, err
    }
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("bedrock: chat call: %w", err)
    }
    defer func() { _ = resp.Body.Close() }()

    reader := p.boundedReader(resp.Body)
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return nil, p.statusError(resp.StatusCode, reader)
    }
    raw, err := io.ReadAll(reader)
    if err != nil {
        return nil, fmt.Errorf("bedrock: read response: %w", err)
    }
    text, in, out, err := decodeConverse(raw)
    if err != nil {
        return nil, err
    }
    return &engine.ChatResponse{
        Output:    text,
        Model:     p.binding.Model,
        TokensIn:  in,
        TokensOut: out,
    }, nil
}

func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
    streamCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

    body, err := buildBody(req)
    if err != nil {
        cancel()
        return nil, err
    }
    httpReq, err := p.signedRequest(streamCtx, body, true)
    if err != nil {
        cancel()
        return nil, err
    }
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        cancel()
        return nil, fmt.Errorf("bedrock: stream call: %w", err)
    }

    out := make(chan engine.ChatChunk)
    go func() {
        defer cancel()
        defer close(out)
        defer func() { _ = resp.Body.Close() }()

        reader := p.boundedReader(resp.Body)
        if resp.StatusCode < 200 || resp.StatusCode >= 300 {
            send(streamCtx, out, engine.ChatChunk{Error: p.statusError(resp.StatusCode, reader)})
            return
        }
        var tin, tout int
        scanner := bufio.NewScanner(reader)
        scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
        for scanner.Scan() {
            delta, in, o, done, perr := parseConverseEvent(scanner.Bytes())
            if perr != nil {
                send(streamCtx, out, engine.ChatChunk{Error: perr})
                return
            }
            if in != 0 {
                tin = in
            }
            if o != 0 {
                tout = o
            }
            if done {
                break
            }
            if delta != "" {
                if !send(streamCtx, out, engine.ChatChunk{Token: delta}) {
                    return
                }
            }
        }
        if serr := scanner.Err(); serr != nil {
            send(streamCtx, out, engine.ChatChunk{Error: fmt.Errorf("bedrock: read stream: %w", serr)})
            return
        }
        send(streamCtx, out, engine.ChatChunk{Done: true, TokensIn: tin, TokensOut: tout})
    }()
    return out, nil
}

func send(ctx context.Context, out chan<- engine.ChatChunk, c engine.ChatChunk) bool {
    select {
    case out <- c:
        return true
    case <-ctx.Done():
        return false
    }
}

// statusError reads the bounded error body and wraps ErrUpstreamStatus with a
// control-scrubbed snippet. It never includes any credential.
func (p *Provider) statusError(status int, body io.Reader) error {
    snippet, _ := io.ReadAll(body)
    const max = 512
    if len(snippet) > max {
        snippet = snippet[:max]
    }
    return fmt.Errorf("%w: status=%d body=%s", ErrUpstreamStatus, status, scrubControl(string(snippet)))
}

func scrubControl(s string) string {
    return strings.Map(func(r rune) rune {
        if r == '\t' || r == '\n' || r == '\r' || (r >= 0 && r < 0x20) || r == 0x7f {
            return ' '
        }
        return r
    }, s)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./adapter/bedrock/ -run 'TestChat|TestStreamChat' -v`
Expected: PASS. (If `TestChat_HappyPath` fails on the `Authorization` prefix assertion, the rewriting transport is dropping headers — confirm `recordingTransport` copies `r.Header` onto the rewritten request.)

- [ ] **Step 5: Run the full bedrock package + vet**

Run: `go test ./adapter/bedrock/ -v && go vet ./adapter/bedrock/`
Expected: PASS, no findings.

- [ ] **Step 6: Commit**

```bash
git add adapter/bedrock/provider.go adapter/bedrock/provider_test.go
git commit -m "feat(bedrock): Converse Chat + StreamChat provider (SigV4-signed)

Derived regional endpoint, bounded reader (MaxResponseBytes), no-leak
status errors, credential guarded before reveal. Reuses Task-1 signer.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2F: Factory routing (`bedrock` / `amazon-bedrock`)

**Files:**
- Modify: `adapter/provider/factory.go`
- Test: `adapter/provider/factory_test.go`

- [ ] **Step 1: Write the failing tests (happy-path + edge-case normalization)**

```go
// append to adapter/provider/factory_test.go
func TestChatProviderFor_BedrockRoutes(t *testing.T) {
    f := provider.NewFactory(provider.Deps{}) // adjust to the existing constructor used in this test file
    for _, name := range []string{"bedrock", "amazon-bedrock", "  Bedrock  ", "AMAZON-BEDROCK"} {
        b := domprovider.ResolvedBinding{
            Kind:     domprovider.ProfileKindChat,
            Provider: name,
            Extras:   map[string]string{"region": "us-east-1", "access-key-id": "AKID"},
            Model:    "anthropic.claude-v2",
        }
        cp, err := f.ChatProviderFor(b)
        if err != nil {
            t.Fatalf("provider %q: unexpected error %v", name, err)
        }
        if _, ok := cp.(*bedrock.Provider); !ok {
            t.Errorf("provider %q: got %T, want *bedrock.Provider", name, cp)
        }
    }
}
```

(Import `bedrock "github.com/arbitron/arbitron/adapter/bedrock"` and reuse whatever `provider`/`domprovider` aliases the existing `factory_test.go` already declares; match the existing `NewFactory(Deps{...})` call shape in that file.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./adapter/provider/ -run TestChatProviderFor_BedrockRoutes -v`
Expected: FAIL — `ErrUnknownProvider` (no bedrock arm yet) / unused import until the arm is added.

- [ ] **Step 3: Implement the routing**

```go
// adapter/provider/factory.go — add to the import block:
    "github.com/arbitron/arbitron/adapter/bedrock"

// add to the const block:
    providerBedrock       = "bedrock"
    providerAmazonBedrock = "amazon-bedrock"

// in ChatProviderFor's switch, before default:
    case providerBedrock, providerAmazonBedrock:
        return bedrock.New(bedrock.Config{
            Binding:          b,
            HTTPClient:       f.deps.HTTPClient,
            Timeout:          f.deps.HTTPTimeout,
            MaxResponseBytes: f.deps.MaxResponseBytes,
        }), nil
```

(`normalizeProvider` already trims+lowercases, so `"  Bedrock  "` and `"AMAZON-BEDROCK"` match. `Config.Now` is left nil → `time.Now` in production, which is correct.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./adapter/provider/ -run TestChatProviderFor_BedrockRoutes -v`
Expected: PASS.

- [ ] **Step 5: Build the whole tree + run all tests**

Run: `go build ./... && make test`
Expected: build clean; `make test` (== `go test ./...`) green.

- [ ] **Step 6: Commit**

```bash
git add adapter/provider/factory.go adapter/provider/factory_test.go
git commit -m "feat(provider): route bedrock/amazon-bedrock chat to adapter/bedrock

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Acceptance Criteria (Task 2)

- `ValidateConnection` rejects a `bedrock` connection missing `region` or `access-key-id` with `ErrBedrockCredentialsRequired`, and accepts one with both; the Azure rule is unchanged.
- `endpointURL` derives `https://bedrock-runtime.{region}.amazonaws.com/model/{modelId}/converse` (and `/converse-stream`), path-escaping the model id; empty region/model rejected.
- `buildBody` produces a Converse body with `messages[].content[].text`, optional `system[].text`, and `inferenceConfig{maxTokens,temperature}` with zero fields omitted; the model id is **not** in the body.
- `Chat` returns `ChatResponse{Output, Model, TokensIn, TokensOut}` from a Converse response; `StreamChat` yields ordered token chunks then a `Done` chunk carrying usage.
- Each request is SigV4-signed (Task-1 signer); region/access-key-id are guarded **before** `RevealKey` is invoked; `403→`/`400→`/`429→`/`500→` all map to `ErrUpstreamStatus`; no credential appears in any error.
- `MaxResponseBytes` caps the response read (over-large body is a hard error).
- The factory routes `bedrock` and `amazon-bedrock` (case/whitespace-insensitive) to `bedrock.New`, distinct from the openrouter Bearer arm; no new `Deps` field required.
- `go build ./...`, `go vet ./...`, and `make test` all pass; `git diff go.mod` is empty (no AWS SDK).

## Five-Category Coverage Map (Task 2)

- **Happy-path:** `TestEndpointURL_Converse`, `TestBuildBody_MinimalUserOnly`, `TestDecodeConverse_HappyPath`, `TestParseConverseEvent_DeltaUsageStop`, `TestChat_HappyPath`, `TestStreamChat_OrderedTokensThenDone`, `TestChatProviderFor_BedrockRoutes`.
- **Input-validation:** `TestValidateConnection_BedrockRequiresRegionAndAccessKey`, `TestEndpointURL_MissingRegion`, `TestEndpointURL_MissingModel`, `TestChat_MissingRegionGuarded`, `TestChat_MissingAccessKeyIDGuarded`.
- **Error-handling:** `TestChat_403AuthError_NoSecretLeak`, `TestChat_StatusErrorsMapped`, `TestDecodeConverse_EmptyBody`, `TestDecodeConverse_MalformedJSON`.
- **Edge-cases:** `TestEndpointURL_ConverseStream`, `TestEndpointURL_RegionDrivesHost`, `TestBuildBody_SystemAndInferenceConfig`, `TestBuildBody_ZeroTemperatureOmitted`, `TestDecodeConverse_NoTextContent`, `TestParseConverseEvent_IgnoredEvent`, `TestChatProviderFor_BedrockRoutes` (normalization cases).
- **Security:** `TestChat_403AuthError_NoSecretLeak`, `TestChat_NoSecretInOutgoingHeaders`, `TestChat_MaxResponseBytesCap`, `TestChat_MissingRegionGuarded` (secret not revealed on guard failure).

## Notes / Self-Review

- **Spec coverage:** "Universal chat" success criterion #2 (Bedrock executes a chat node against a faked endpoint) → `TestChat_HappyPath` + `TestStreamChat_*`. "No secret leakage" #6 → the security-category tests + Task-1 golden/no-leak tests. Validation #5 (reachability) is exercised by `Chat` against `httptest`; on-demand connection validation + model listing is explicitly **out of scope** for Phase 3 (design doc: Phase 5).
- **Out of scope (this phase):** ambient AWS credential chain, Bedrock on the agent path (Phase 4 / opencode), Bedrock model listing (Phase 5).
- **Type consistency:** `signRequest`/`parseCredential`/`credential` referenced here are exactly as defined in Task 1; `ErrUpstreamStatus`, `ErrMissingRegion`, `ErrMissingModel`, `ErrMissingAccessKeyID`, `ErrBedrockCredentialsRequired` are each defined once in this task.
- **Test seam caveat:** `recordingTransport` rewrites the host to the httptest server while preserving the SigV4-signed headers and path; this lets the adapter sign the real `bedrock-runtime.{region}.amazonaws.com` host yet send bytes to loopback without needing `provider.AllowAnyURLPolicy` (bedrock derives its endpoint and does not pass through the BaseURL write-path policy).
