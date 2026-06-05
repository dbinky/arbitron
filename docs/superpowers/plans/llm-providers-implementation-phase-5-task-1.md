# LLM Providers — Phase 5, Task 1: ConnectionProbe port + ProbeResult + ProbeSelector + per-provider probe adapters

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a domain-side `ConnectionProbe` port (`Probe(ctx, ResolvedBinding) (ProbeResult, error)`) plus a `ProbeSelector` that dispatches by `binding.Provider` to per-provider probe adapters, so an operator-facing CLI (Task 2) can validate any configured connection — confirm credentials authenticate + the endpoint is reachable, and list models where the provider exposes that capability — depending only on the domain port and never importing an adapter package.

**Architecture:** Mirror the existing `ChatProviderSelector`/`Factory` selection pattern (`domain/engine/ports.go` + `adapter/provider/factory.go`). A new driven port `provider.ConnectionProbe` is defined in `domain/provider`. A new `adapter/probe` package supplies one probe per provider family reusing each chat adapter's auth + bounded-read + no-leak helpers (Bearer for OpenAI-compatible, `x-api-key`+`anthropic-version` for Anthropic, the Phase-3 SigV4 signer for Bedrock, the `CommandRunner` binary seam for the Claude CLI), and a `ProbeFactory` implementing `provider.ProbeSelector` that maps a `ResolvedBinding.Provider` string to the right probe. Probes return `ProbeResult{Reachable, ModelsUnsupported, Models}`; reachability-only providers (Claude CLI subscription) set `ModelsUnsupported = true`.

**Tech Stack:** Go, `testing` + `testify` (`assert`/`require`), `net/http/httptest` for HTTP probe seams, a fake `claudecli.CommandRunner` for the Claude CLI probe, an injectable clock for the Bedrock SigV4 signature. `make test` == `go test ./...` (hermetic; no live network).

---

## Dependencies

- **Phases 1–3 endpoints/auth/signer (reused, not reimplemented):**
  - **OpenAI / OpenRouter / Ollama / Azure (OpenAI-compatible), Phase 1:** Bearer-key auth (`Authorization: Bearer <key>`) against `{base_url}`; the listing endpoint is `GET {base_url}/models` (when the base already ends in `/v1`, e.g. `https://api.openai.com/v1`) — i.e. `{base}/models`. The probe reuses the `checkScheme` (http|https) and `scrubControl`/bounded-read patterns from `adapter/openrouter/{provider.go,request.go}`.
  - **Anthropic API, Phase 2:** `GET {base_url}/v1/models` with headers `x-api-key: <key>` and `anthropic-version: 2023-06-01` (the pinned constant from `adapter/anthropicapi`), `base_url` defaulting to `https://api.anthropic.com`. Reuses Phase 2's no-leak/bounded-read helpers.
  - **Amazon Bedrock, Phase 3:** `bedrock:ListFoundationModels` — `GET https://bedrock.{region}.amazonaws.com/foundation-models` (the **control-plane** `bedrock` host, NOT the `bedrock-runtime` Converse host), SigV4-signed with service `bedrock`, region from `Extras["region"]`, access-key-id from `Extras["access-key-id"]`, and the sealed secret-access-key (+ optional session token) from `RevealKey`. **REUSE the Phase 3 signer** (`adapter/bedrock/sigv4.go`) — do not write a second signer; the clock is injectable for deterministic test signatures.
  - **Claude CLI subscription, existing:** reachability only via the binary, run through the `claudecli.CommandRunner` seam (`adapter/claudecli/runner.go`), e.g. `claude --version`.
- **Domain seams already present:** `provider.ResolvedBinding{Kind,Provider,BaseURL,Extras,Model,AllowedTools,RevealKey}` (`domain/provider/provider.go`); `provider.ErrConnectionNotFound` (`domain/provider/connection.go`); the `normalizeProvider` (trim + lowercase) selection idiom and canonical provider constants (`adapter/provider/factory.go`).
- **Task ordering:** This task depends on no other Phase-5 task. Task 2 (the CLI command) depends on this task's `provider.ConnectionProbe` / `provider.ProbeSelector` / `provider.ProbeResult` and the `adapter/probe.ProbeFactory`.

> **Note on Phases 1–3 being un-merged in this worktree:** the per-provider chat adapters (`adapter/anthropicapi`, `adapter/bedrock`, and the `openai`/`ollama` Factory routing) are defined by the Phase 1–3 designs but may not all be merged here yet. Each probe below is **self-contained**: it constructs its own HTTP request and reuses only the *patterns* (Bearer header, `x-api-key`+`anthropic-version`, the SigV4 signer file, the bounded-reader + `scrubControl` no-leak helpers). If `adapter/bedrock/sigv4.go` exists, import and reuse it; if it does not yet exist in this worktree, the SigV4 signer is built **once** in `adapter/probe/sigv4.go` and the Phase-3 Converse adapter reuses *it* — there must remain exactly one signer. Confirm with `ls adapter/bedrock/sigv4.go` before Step "implement Bedrock probe" and follow whichever branch holds; the test list and signatures below are unchanged either way.

## Spec / design references

- Spec `docs/specs/llm-providers-spec.md` — "Connection Validation & Model Listing"; success criterion #5 (validation + model listing where supported, reachability-only otherwise); #6 (no secret leakage).
- Design `docs/superpowers/specs/llm-providers-design-phase-5.md` — "Domain port", "Per-provider probes", "Error handling", "Testing".

## File Structure

| File | Responsibility |
|---|---|
| `domain/provider/probe.go` | **New.** Domain driven port: `ConnectionProbe`, `ProbeResult`, `ProbeSelector`, `ErrNoProbeForProvider`. No I/O, no adapter imports. |
| `domain/provider/probe_test.go` | **New.** Port-shape + zero-value tests (interface compile-time assertions live in the adapter package). |
| `adapter/probe/factory.go` | **New.** `ProbeFactory` implementing `provider.ProbeSelector`; `normalizeProvider` dispatch by `binding.Provider` to the per-provider probe; `Deps{HTTPClient, Timeout, MaxResponseBytes, Clock, Runner, ClaudeBinaryPath}`. |
| `adapter/probe/openai_compat.go` | **New.** `openAICompatProbe` — `GET {base}/models`, Bearer auth, lists model ids. Serves `openai`/`openrouter`/`litellm`/`ollama`/`azure-openai`/`azure-foundry`. |
| `adapter/probe/ollama_native.go` | **New.** `ollamaNativeFallback` — `GET {base}/api/tags` used only when the OpenAI-compat `/models` is absent (404). |
| `adapter/probe/anthropic.go` | **New.** `anthropicProbe` — `GET {base}/v1/models` with `x-api-key` + `anthropic-version`. |
| `adapter/probe/bedrock.go` | **New.** `bedrockProbe` — SigV4-signed `GET …/foundation-models` (ListFoundationModels); reuses the Phase-3 signer. |
| `adapter/probe/claudecli.go` | **New.** `claudeCLIProbe` — runs `claude --version` via the `CommandRunner` seam; `ModelsUnsupported=true`. |
| `adapter/probe/httpshared.go` | **New.** Shared `boundedReader`/`limitedReader`, `scrubControl`, `statusError`, `checkScheme`, and the `ProbeError` classification (`ErrCredentialsRejected`, `ErrUnreachable`, `ErrRegionConfig`). |
| `adapter/probe/*_test.go` | **New.** Per-file `_test.go` covering the five TDD categories with `httptest` / fake runner / injected clock seams. |

## Files

- **Create:** `domain/provider/probe.go`

```go
package provider

import (
	"context"
	"errors"
)

// ConnectionProbe validates a resolved connection and, where the provider
// exposes a model listing, returns its models. It is a driven port: the CLI
// (and any future caller) depends on this interface; adapters implement it.
//
// Probe authenticates + checks reachability for a resolved binding, returning
// the available models when the provider exposes a listing, or
// ModelsUnsupported=true when it does not (reachability only). A non-nil error
// means validation failed (credentials rejected, endpoint unreachable, or a
// region/config fault); the error never carries the connection's secret.
type ConnectionProbe interface {
	Probe(ctx context.Context, b ResolvedBinding) (ProbeResult, error)
}

// ProbeResult is the outcome of a successful Probe (err == nil).
type ProbeResult struct {
	// Reachable is true when credentials authenticated and the endpoint
	// responded. A successful Probe always sets this true.
	Reachable bool
	// ModelsUnsupported is true when the provider exposes no model listing
	// (e.g. the Claude CLI subscription). Models is then empty by contract.
	ModelsUnsupported bool
	// Models holds the provider's model identifiers when listing is supported.
	// It may be empty even when supported (a reachable provider with no models).
	Models []string
}

// ProbeSelector chooses the concrete ConnectionProbe for a resolved binding,
// dispatching by binding.Provider. It mirrors engine.ChatProviderSelector so
// the CLI depends on this domain port and never imports an adapter package.
type ProbeSelector interface {
	ProbeFor(b ResolvedBinding) (ConnectionProbe, error)
}

// ErrNoProbeForProvider is returned by a ProbeSelector when a binding names a
// provider that has no probe mapping.
var ErrNoProbeForProvider = errors.New("provider: no probe for provider")
```

- **Create:** `adapter/probe/httpshared.go`

```go
// Package probe implements provider.ConnectionProbe per provider family and the
// provider.ProbeSelector that dispatches by binding.Provider. Each probe reuses
// its phase's chat-adapter auth + bounded-read + no-leak patterns. SECURITY: a
// revealed key sets the auth header only and never appears in returned errors,
// which carry a control-scrubbed, length-capped upstream snippet.
package probe

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Classification sentinels distinguish configuration faults from transport
// faults so the CLI can print a specific, actionable result and pick an exit
// code. They never carry the secret.
var (
	// ErrCredentialsRejected maps a 401/403 (and Bedrock SigV4 rejection).
	ErrCredentialsRejected = errors.New("probe: credentials rejected")
	// ErrUnreachable maps a transport error (connection refused, DNS, TLS,
	// timeout) — the endpoint could not be reached at all.
	ErrUnreachable = errors.New("probe: endpoint unreachable")
	// ErrRegionConfig maps a Bedrock region/credential configuration fault
	// (missing region/access-key-id, or an endpoint-derivation failure).
	ErrRegionConfig = errors.New("probe: region/configuration error")
	// ErrUpstreamStatus maps any other non-2xx upstream status (400/404/429/5xx).
	ErrUpstreamStatus = errors.New("probe: upstream returned an error status")
	// errResponseTooLarge guards the bounded read (OOM DoS backstop).
	errResponseTooLarge = errors.New("probe: response exceeds MaxResponseBytes")
)

// limitedReader is io.LimitReader that errors (not EOF) past the cap.
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

// boundedReader caps r at max bytes; max <= 0 means no cap.
func boundedReader(r io.Reader, max int64) io.Reader {
	if max <= 0 {
		return r
	}
	return &limitedReader{r: r, remaining: max}
}

// scrubControl replaces ASCII control chars with a space so an upstream-shaped
// snippet cannot inject newlines/escapes into logs. Mirrors openrouter.scrubControl.
func scrubControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0 && r < 0x20) || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// classifyStatus maps an HTTP status to a classified sentinel, reading a
// bounded, scrubbed snippet of the body for diagnostics. 401/403 ->
// ErrCredentialsRejected; everything else non-2xx -> ErrUpstreamStatus.
func classifyStatus(status int, body io.Reader) error {
	snippet, _ := io.ReadAll(body)
	const max = 512
	if len(snippet) > max {
		snippet = snippet[:max]
	}
	scrubbed := scrubControl(string(snippet))
	switch status {
	case 401, 403:
		return fmt.Errorf("%w: status=%d body=%s", ErrCredentialsRejected, status, scrubbed)
	default:
		return fmt.Errorf("%w: status=%d body=%s", ErrUpstreamStatus, status, scrubbed)
	}
}

// checkScheme rejects a non-http(s) base_url scheme (defensive belt-and-suspenders;
// the authoritative SSRF policy is enforced at connection write time).
func checkScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("probe: parse base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("probe: base_url scheme must be http or https: got %q", u.Scheme)
	}
	return nil
}
```

- **Create:** `adapter/probe/factory.go`

```go
package probe

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// Canonical provider strings after normalization (trim + lowercase). These
// mirror adapter/provider/factory.go's constants so the two selectors agree.
const (
	providerClaudeCLI    = "claude-cli"
	providerOpenAI       = "openai"
	providerOpenRouter   = "openrouter"
	providerLiteLLM      = "litellm"
	providerOllama       = "ollama"
	providerOllamaCloud  = "ollama-cloud"
	providerAzureOpenAI  = "azure-openai"
	providerAzureFoundry = "azure-foundry"
	providerAnthropicAPI = "anthropic-api"
	providerBedrock      = "bedrock"
	providerAmazonBedrock = "amazon-bedrock"
)

// Deps carries the shared construction inputs threaded into each probe.
type Deps struct {
	// HTTPClient is used by the HTTP probes; nil -> a default *http.Client.
	HTTPClient *http.Client
	// Timeout bounds each probe call; 0 -> no per-call timeout.
	Timeout time.Duration
	// MaxResponseBytes caps each probe's response read (OOM backstop); 0 -> no cap.
	MaxResponseBytes int64
	// Clock supplies the SigV4 signing time for the Bedrock probe; nil -> time.Now.
	Clock func() time.Time
	// Runner is the subprocess seam for the Claude CLI probe; nil -> the shared
	// production os/exec runner (claudecli.NewExecRunner()).
	Runner claudecli.CommandRunner
	// ClaudeBinaryPath overrides PATH lookup for the claude binary; "" -> "claude".
	ClaudeBinaryPath string
}

// ProbeFactory selects a concrete ConnectionProbe per resolved binding. It
// implements provider.ProbeSelector, so the CLI depends on the domain port and
// never imports this package's probe types directly.
type ProbeFactory struct {
	deps Deps
}

func NewProbeFactory(deps Deps) *ProbeFactory { return &ProbeFactory{deps: deps} }

var _ domprovider.ProbeSelector = (*ProbeFactory)(nil)

func normalizeProvider(p string) string { return strings.ToLower(strings.TrimSpace(p)) }

// ProbeFor dispatches by binding.Provider. The empty/claude-cli path returns the
// reachability-only CLI probe; OpenAI-compatible families return the
// {base}/models probe; anthropic-api and bedrock return their dedicated probes.
// Unknown providers return ErrNoProbeForProvider.
func (f *ProbeFactory) ProbeFor(b domprovider.ResolvedBinding) (domprovider.ConnectionProbe, error) {
	switch normalizeProvider(b.Provider) {
	case "", providerClaudeCLI:
		return newClaudeCLIProbe(f.deps), nil
	case providerOpenAI, providerOpenRouter, providerLiteLLM,
		providerOllama, providerOllamaCloud, providerAzureOpenAI, providerAzureFoundry:
		return newOpenAICompatProbe(f.deps), nil
	case providerAnthropicAPI:
		return newAnthropicProbe(f.deps), nil
	case providerBedrock, providerAmazonBedrock:
		return newBedrockProbe(f.deps), nil
	default:
		return nil, fmt.Errorf("%w: %q", domprovider.ErrNoProbeForProvider, b.Provider)
	}
}
```

- **Create:** `adapter/probe/openai_compat.go`

```go
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// openAICompatProbe lists models from any OpenAI-compatible endpoint via
// GET {base}/models with Bearer auth, falling back to Ollama's native /api/tags
// when /models is absent (404).
type openAICompatProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
}

func newOpenAICompatProbe(d Deps) *openAICompatProbe {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &openAICompatProbe{
		httpClient:       hc,
		timeout:          func(ctx context.Context) (context.Context, context.CancelFunc) { return procenv.WithTimeout(ctx, d.Timeout) },
		maxResponseBytes: d.MaxResponseBytes,
	}
}

var _ domprovider.ConnectionProbe = (*openAICompatProbe)(nil)

// modelsList is the OpenAI-shaped list body: {"data":[{"id":"..."}]}.
type modelsList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (p *openAICompatProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	target := strings.TrimRight(b.BaseURL, "/") + "/models"
	if err := checkScheme(target); err != nil {
		return domprovider.ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	if b.RevealKey != nil {
		key, kerr := b.RevealKey(ctx)
		if kerr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("probe: reveal key: %w", kerr)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)

	// Ollama-native fallback when the OpenAI-compat /models path is absent.
	if resp.StatusCode == http.StatusNotFound {
		return p.probeOllamaNative(ctx, b)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
	var list modelsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode model list: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
```

- **Create:** `adapter/probe/ollama_native.go`

```go
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// tagsList is Ollama's native list body: {"models":[{"name":"llama3:8b"}]}.
type tagsList struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// probeOllamaNative lists models via GET {base}/api/tags. The base may already
// carry an OpenAI-compat /v1 suffix, so /v1 is trimmed before appending /api/tags.
func (p *openAICompatProbe) probeOllamaNative(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	base := strings.TrimRight(b.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	target := base + "/api/tags"
	if err := checkScheme(target); err != nil {
		return domprovider.ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
	var list tagsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode tags list: %w", err)
	}
	ids := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		if m.Name != "" {
			ids = append(ids, m.Name)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
```

- **Create:** `adapter/probe/anthropic.go`

```go
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// anthropicVersion is the pinned Messages-API version header (mirrors adapter/anthropicapi).
const anthropicVersion = "2023-06-01"

// defaultAnthropicBase is used when the connection omits a base_url.
const defaultAnthropicBase = "https://api.anthropic.com"

type anthropicProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
}

func newAnthropicProbe(d Deps) *anthropicProbe {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &anthropicProbe{
		httpClient:       hc,
		timeout:          func(ctx context.Context) (context.Context, context.CancelFunc) { return procenv.WithTimeout(ctx, d.Timeout) },
		maxResponseBytes: d.MaxResponseBytes,
	}
}

var _ domprovider.ConnectionProbe = (*anthropicProbe)(nil)

func (p *anthropicProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	base := b.BaseURL
	if strings.TrimSpace(base) == "" {
		base = defaultAnthropicBase
	}
	target := strings.TrimRight(base, "/") + "/v1/models"
	if err := checkScheme(target); err != nil {
		return domprovider.ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	// Anthropic always requires a key; a missing/empty key is a config fault we
	// let the upstream 401 surface as ErrCredentialsRejected.
	if b.RevealKey != nil {
		key, kerr := b.RevealKey(ctx)
		if kerr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("probe: reveal key: %w", kerr)
		}
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
	}
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
	// Anthropic list body: {"data":[{"id":"claude-..."}]}.
	var list modelsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode model list: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
```

- **Create:** `adapter/probe/bedrock.go`

```go
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// bedrockProbe lists foundation models via the control-plane ListFoundationModels
// endpoint (GET https://bedrock.{region}.amazonaws.com/foundation-models),
// SigV4-signed with service "bedrock". REUSE the Phase-3 signer; do not add a
// second one (see plan note).
type bedrockProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
	clock            func() time.Time
}

func newBedrockProbe(d Deps) *bedrockProbe {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	clk := d.Clock
	if clk == nil {
		clk = time.Now
	}
	return &bedrockProbe{
		httpClient:       hc,
		timeout:          func(ctx context.Context) (context.Context, context.CancelFunc) { return procenv.WithTimeout(ctx, d.Timeout) },
		maxResponseBytes: d.MaxResponseBytes,
		clock:            clk,
	}
}

var _ domprovider.ConnectionProbe = (*bedrockProbe)(nil)

// foundationModels is the ListFoundationModels body:
// {"modelSummaries":[{"modelId":"anthropic.claude-..."}]}.
type foundationModels struct {
	ModelSummaries []struct {
		ModelID string `json:"modelId"`
	} `json:"modelSummaries"`
}

func (p *bedrockProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	region := strings.TrimSpace(b.Extras["region"])
	accessKeyID := strings.TrimSpace(b.Extras["access-key-id"])
	if region == "" || accessKeyID == "" {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: bedrock requires region and access-key-id in extras", ErrRegionConfig)
	}
	// Reveal the sealed secret (bare string or {"secret","session_token"} JSON).
	var secret, sessionToken string
	if b.RevealKey != nil {
		payload, kerr := b.RevealKey(ctx)
		if kerr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("probe: reveal key: %w", kerr)
		}
		secret, sessionToken = parseSealedAWS(payload)
	}

	target := fmt.Sprintf("https://bedrock.%s.amazonaws.com/foundation-models", region)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	// signAWS reuses the Phase-3 SigV4 signer (service "bedrock"); the injected
	// clock makes the signature deterministic in tests. It sets Authorization,
	// X-Amz-Date, and X-Amz-Security-Token (when sessionToken != "").
	if err := signAWS(req, nil, region, "bedrock", accessKeyID, secret, sessionToken, p.clock()); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrRegionConfig, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 403 from Bedrock is a credentials/SigV4 rejection -> credentials class.
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
	var list foundationModels
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode model list: %w", err)
	}
	ids := make([]string, 0, len(list.ModelSummaries))
	for _, m := range list.ModelSummaries {
		if m.ModelID != "" {
			ids = append(ids, m.ModelID)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}

// parseSealedAWS tolerates both the bare secret-access-key string and the
// {"secret":"...","session_token":"..."} composite (Phase-3 sealed forms).
func parseSealedAWS(payload string) (secret, sessionToken string) {
	trimmed := strings.TrimSpace(payload)
	if strings.HasPrefix(trimmed, "{") {
		var c struct {
			Secret       string `json:"secret"`
			SessionToken string `json:"session_token"`
		}
		if err := json.Unmarshal([]byte(trimmed), &c); err == nil {
			return c.Secret, c.SessionToken
		}
	}
	return payload, ""
}
```

> **`signAWS` sourcing:** if `adapter/bedrock/sigv4.go` exists in this worktree, export its signer (e.g. a `bedrock.SignV4(...)` func) and call it here instead of redeclaring `signAWS` — keep exactly one signer. If Phase 3 is not yet merged here, implement `signAWS` once in `adapter/probe/sigv4.go` (canonical request, string-to-sign, signing-key derivation, `Authorization` + `X-Amz-Date` (+ `X-Amz-Security-Token`)) and have the Phase-3 Converse adapter reuse it. The Security test below pins the signature against a fixed key+time+request golden.

- **Create:** `adapter/probe/claudecli.go`

```go
package probe

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/claudecli"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// claudeCLIProbe checks reachability of the local Claude subscription binary
// (no key, no listing endpoint). It runs `claude --version` through the
// CommandRunner seam and reports Reachable + ModelsUnsupported.
type claudeCLIProbe struct {
	runner claudecli.CommandRunner
	bin    string
}

func newClaudeCLIProbe(d Deps) *claudeCLIProbe {
	r := d.Runner
	if r == nil {
		r = claudecli.NewExecRunner()
	}
	bin := d.ClaudeBinaryPath
	if bin == "" {
		bin = "claude"
	}
	return &claudeCLIProbe{runner: r, bin: bin}
}

var _ domprovider.ConnectionProbe = (*claudeCLIProbe)(nil)

func (p *claudeCLIProbe) Probe(ctx context.Context, _ domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	lines, wait, err := p.runner.Run(ctx, claudecli.RunSpec{Bin: p.bin, Args: []string{"--version"}})
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	// Contract: drain lines to completion before calling wait (runner.go).
	for range lines {
	}
	if werr := wait(); werr != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, werr)
	}
	return domprovider.ProbeResult{Reachable: true, ModelsUnsupported: true}, nil
}
```

- **Test:** `domain/provider/probe_test.go`, `adapter/probe/factory_test.go`, `adapter/probe/openai_compat_test.go`, `adapter/probe/ollama_native_test.go`, `adapter/probe/anthropic_test.go`, `adapter/probe/bedrock_test.go`, `adapter/probe/claudecli_test.go` (per the TDD list below).

## TDD test list (five categories)

**Seams:** `httptest.NewServer` (HTTP probes), a fake `claudecli.CommandRunner` (CLI probe), `Deps.Clock` returning a fixed `time.Time` (Bedrock signature), `Deps.MaxResponseBytes` (bounded-read). A binding helper `b := provider.ResolvedBinding{Provider: ..., BaseURL: srv.URL, Extras: ..., RevealKey: func(context.Context)(string,error){...}}` is constructed per test. **No live network.** Each test follows: write failing test → run (`go test ./adapter/probe/... -run <Name> -v`, expect FAIL: undefined/assertion) → minimal impl → run (expect PASS) → commit.

### Happy-path

- [ ] `TestOpenAICompatProbe_ListsModels` — `httptest` server returns `200` with `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}` at `GET /models`. Build `newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})`, `b.Provider="openai"`, `b.BaseURL=srv.URL`. Assert `require.NoError`, `res.Reachable == true`, `res.ModelsUnsupported == false`, `res.Models == []string{"gpt-4o","gpt-4o-mini"}`. (Run once per family via a sub-test loop over `openrouter`/`ollama`/`azure-openai` to prove each OpenAI-compatible provider lists models through the same probe.)
- [ ] `TestAnthropicProbe_ListsModels` — `httptest` server at `GET /v1/models` asserts the request carried `x-api-key: sk-ant-TEST` and `anthropic-version: 2023-06-01`, returns `{"data":[{"id":"claude-sonnet-4"}]}`. Assert `require.NoError`, `res.Reachable`, `res.Models == []string{"claude-sonnet-4"}`.
- [ ] `TestBedrockProbe_ListFoundationModels_Signed` — `httptest` server asserts the request has a non-empty `Authorization` header beginning `AWS4-HMAC-SHA256` and an `X-Amz-Date` header, returns `{"modelSummaries":[{"modelId":"anthropic.claude-3-5-sonnet"}]}`. `Deps.Clock` returns a fixed time; `b.Extras = {"region":"us-east-1","access-key-id":"AKIA..."}`, `b.RevealKey` returns the bare secret. Assert `require.NoError`, `res.Models == []string{"anthropic.claude-3-5-sonnet"}`. (Point the probe at `srv.URL` by overriding the endpoint via a test seam — see note below.)
- [ ] `TestClaudeCLIProbe_ReachableModelsUnsupported` — fake `CommandRunner` whose `Run` returns a closed `lines` channel and a `wait` returning `nil` (simulating `claude --version` exit 0). Assert `require.NoError`, `res.Reachable == true`, `res.ModelsUnsupported == true`, `res.Models` empty. Also assert the fake captured `spec.Bin == "claude"` and `spec.Args == []string{"--version"}`.
- [ ] `TestProbeFactory_ProbeFor_DispatchesByProvider` — table over `{"openai"→*openAICompatProbe, "ollama"→*openAICompatProbe, "azure-foundry"→*openAICompatProbe, "anthropic-api"→*anthropicProbe, "bedrock"→*bedrockProbe, "amazon-bedrock"→*bedrockProbe, ""→*claudeCLIProbe, "claude-cli"→*claudeCLIProbe}`. For each: `p, err := f.ProbeFor(provider.ResolvedBinding{Provider: name})`, `require.NoError`, `assert.IsType(want, p)`.

### Input-validation

- [ ] `TestProbeFactory_ProbeFor_UnknownProvider_Errors` — `f.ProbeFor(provider.ResolvedBinding{Provider: "made-up"})` → `require.Error`, `assert.ErrorIs(err, provider.ErrNoProbeForProvider)`, `assert.Contains(err.Error(), "made-up")`.
- [ ] `TestProbeFactory_ProbeFor_NormalizesCaseWhitespace` — `Provider: "  Anthropic-API "` selects `*anthropicProbe`; `"  Amazon-Bedrock"` selects `*bedrockProbe`. `require.NoError` + `assert.IsType`.
- [ ] `TestBedrockProbe_MissingRegion_RegionConfigError` — `b.Extras = {"access-key-id":"AKIA..."}` (no region). Probe → `require.Error`, `assert.ErrorIs(err, ErrRegionConfig)`. Repeat with missing `access-key-id`.
- [ ] `TestOpenAICompatProbe_BadScheme_Rejected` — `b.BaseURL = "file:///etc/passwd"`. Probe → `require.Error`, `assert.Contains(err.Error(), "scheme")`; the HTTP client is never called (use a client whose transport fails the test if invoked).

### Error-handling

- [ ] `TestOpenAICompatProbe_401_CredentialsRejected` — server returns `401`. Assert `require.Error`, `assert.ErrorIs(err, ErrCredentialsRejected)`, `assert.NotErrorIs(err, ErrUnreachable)`. Repeat with `403`.
- [ ] `TestAnthropicProbe_401_CredentialsRejected` — server returns `403` at `/v1/models`. `assert.ErrorIs(err, ErrCredentialsRejected)`.
- [ ] `TestBedrockProbe_403_CredentialsRejected` — server returns `403`. `assert.ErrorIs(err, ErrCredentialsRejected)` (SigV4 rejection class).
- [ ] `TestOpenAICompatProbe_Transport_Unreachable` — point `b.BaseURL` at a closed port (`httptest` server created then `srv.Close()`), or a custom `http.RoundTripper` returning a dial error. Assert `require.Error`, `assert.ErrorIs(err, ErrUnreachable)`, `assert.NotErrorIs(err, ErrCredentialsRejected)`.
- [ ] `TestClaudeCLIProbe_BinaryMissing_Unreachable` — fake `CommandRunner` whose `Run` returns `("", nil, errors.New("exec: \"claude\": not found"))`. Assert `assert.ErrorIs(err, ErrUnreachable)`.
- [ ] `TestOpenAICompatProbe_MalformedBody_Errors` — server returns `200` with `not-json`. Assert `require.Error`, `assert.Contains(err.Error(), "decode")`, and that the error is NOT classified as credentials/unreachable.

### Edge-cases

- [ ] `TestOpenAICompatProbe_404_FallsBackToOllamaNative` — server returns `404` at `/models` and `200` `{"models":[{"name":"llama3:8b"}]}` at `/api/tags`. `b.Provider="ollama"`, `b.BaseURL=srv.URL+"/v1"`. Assert `require.NoError`, `res.Models == []string{"llama3:8b"}` (proves `/v1` is trimmed and the native fallback fires). 
- [ ] `TestOpenAICompatProbe_EmptyModelList_ReachableNoModels` — server returns `200` `{"data":[]}`. Assert `require.NoError`, `res.Reachable == true`, `res.ModelsUnsupported == false`, `len(res.Models) == 0` (reachable, no models — not an error).
- [ ] `TestClaudeCLIProbe_NoListingEndpoint_NotAnError` — re-asserts the happy-path: even though no models are listed, `err == nil` and `res.ModelsUnsupported == true` (a provider with no listing is success, not failure).
- [ ] `TestOpenAICompatProbe_TrailingSlashBaseURL` — `b.BaseURL = srv.URL + "/"` still produces a single-slash `/models` path (server asserts `r.URL.Path == "/models"`). `require.NoError`.
- [ ] `TestBedrockProbe_SessionToken_SecurityTokenHeader` — `b.RevealKey` returns `{"secret":"S","session_token":"TKN"}`; server asserts an `X-Amz-Security-Token: TKN` header is present. `require.NoError`. Also assert `parseSealedAWS` returns `("S","TKN")` and that a bare-string payload returns `(payload,"")`.

### Security

- [ ] `TestOpenAICompatProbe_SecretNeverInError` — server returns `403` with a body echoing back nothing sensitive; `b.RevealKey` returns the sentinel `"sk-SECRET-SENTINEL"`. Build with a buffer-backed `slog` default. Probe → `require.Error`; assert `assert.NotContains(err.Error(), "sk-SECRET-SENTINEL")` AND `assert.NotContains(logBuf.String(), "sk-SECRET-SENTINEL")` (the key sets the auth header only and never appears in errors/logs). Repeat for `anthropicProbe` (key in `x-api-key`) and `bedrockProbe` (secret only in the SigV4 signature, never echoed).
- [ ] `TestOpenAICompatProbe_BoundedResponse` — server streams a body larger than `Deps.MaxResponseBytes` (e.g. cap 64, body 1 MiB of `{`). Probe → `require.Error`, `assert.ErrorIs(err, errResponseTooLarge)` (OOM DoS backstop; an attacker-pointed base_url cannot stream an unbounded body).
- [ ] `TestOpenAICompatProbe_ControlCharsScrubbed` — server returns `401` with a body containing `"\n\rINJECTED\x1b[2J"`. Assert the returned error string contains no raw `\n`/`\r`/`\x1b` (i.e. `scrubControl` ran): `assert.NotContains(err.Error(), "\n")` and `assert.NotContains(err.Error(), "\x1b")`, while still being classified `ErrCredentialsRejected`.

> **Test seam for the Bedrock endpoint host:** `bedrockProbe` derives `https://bedrock.{region}.amazonaws.com/...`. To point it at an `httptest` server without a live AWS call, give `bedrockProbe` an unexported `endpoint func(region string) string` field defaulting to the real derivation, and set it in tests to `func(string) string { return srv.URL }`. Construct this seam in `newBedrockProbe` (read from an optional `Deps.BedrockEndpoint func(region string) string`, nil → default). Document it as test-only. The Security/happy-path SigV4 assertions then inspect the request the `httptest` handler received.

## Acceptance criteria (what `go test ./...` proves)

- `go test ./domain/provider/... ./adapter/probe/...` passes, proving:
  - Each OpenAI-compatible provider (`openai`, `openrouter`, `ollama`, `azure-*`) lists models via `GET {base}/models`; Anthropic lists via `/v1/models` with `x-api-key` + `anthropic-version`; Bedrock lists via a SigV4-signed ListFoundationModels; the Claude CLI probe reports `Reachable + ModelsUnsupported` with no listing.
  - `ProbeSelector.ProbeFor` dispatches every known provider string (case/whitespace-insensitive) to the right probe and returns `ErrNoProbeForProvider` for unknown providers.
  - `401/403` (and Bedrock `403`) classify as `ErrCredentialsRejected`; transport failures classify as `ErrUnreachable`; missing Bedrock region/access-key-id classifies as `ErrRegionConfig` — the three fault classes are distinguishable via `errors.Is`.
  - A provider with no listing endpoint (Claude CLI) and an empty model list (OpenAI-compatible `{"data":[]}`) are both **successes**, not errors.
  - No revealed secret appears in any returned error or in `slog` output; responses are bounded by `MaxResponseBytes`; control characters in upstream snippets are scrubbed.
- `go test ./...` (`make test`) stays green — the additions are new packages plus a new domain port file; no existing package depends on them yet (Task 2 wires the CLI).

## "No secret in logs/errors" security checks

- Probes invoke `b.RevealKey(ctx)` lazily and use the result **only** to set `Authorization`/`x-api-key`/the SigV4 signature. The revealed value is never logged and never interpolated into an error string. The `Test*_SecretNeverInError` tests assert this for all three keyed families.
- `classifyStatus` reads at most 512 bytes of the upstream body, runs `scrubControl`, and includes only the scrubbed snippet — never request headers — in `ErrCredentialsRejected`/`ErrUpstreamStatus`.
- The Bedrock secret-access-key and session token appear only inside the SigV4 `Authorization` signature and the `X-Amz-Security-Token` header (sent to AWS), never in returned errors or logs.
