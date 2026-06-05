# LLM Providers — Phase 5 Design: Connection validation & model listing

**Spec:** `docs/specs/llm-providers-spec.md`
**Phase:** 5 of 5
**Depends on:** Phases 1–3 (provider strings/connections), reuses their endpoints/auth.

## Goal

Let an operator validate any configured connection — confirm credentials authenticate and
the endpoint is reachable — and list the provider's available models where the provider
exposes that capability, without running a workflow. Surface: an operator-facing CLI
subcommand.

## Domain port

New driven port in `domain/provider` (or a focused sub-package):

```go
// ConnectionProbe validates a connection and, where supported, lists its models.
type ConnectionProbe interface {
    // Probe authenticates + checks reachability for a resolved connection,
    // returning the available models when the provider exposes a listing,
    // or ModelsUnsupported=true when it does not (reachability only).
    Probe(ctx context.Context, b provider.ResolvedBinding) (ProbeResult, error)
}

type ProbeResult struct {
    Reachable         bool
    ModelsUnsupported bool      // true => listing not available for this provider
    Models            []string  // populated when listing is supported
}
```

A `ProbeSelector` (mirroring the chat/agent selectors) dispatches by `binding.Provider`
to the right per-provider probe, so the CLI depends on the domain port and never imports
adapter packages.

## Per-provider probes (adapter side)

| Provider | Probe |
|---|---|
| OpenAI / OpenRouter / Ollama / Azure (OpenAI-compatible) | `GET {base}/models` (or `/v1/models`); 200 + key auth = reachable, body lists model ids |
| Anthropic API | `GET {base}/v1/models` with `x-api-key` + `anthropic-version` |
| Amazon Bedrock | `bedrock:ListFoundationModels` (SigV4-signed, reuses Phase 3 signer) |
| Claude CLI (subscription) | reachability only — probe the binary (e.g. `claude --version`); `ModelsUnsupported = true` |
| Ollama (native option) | `GET {base}/api/tags` may be used where the OpenAI-compat `/models` is absent |

Each probe reuses the auth + bounded-read + no-leak patterns from its phase's chat
adapter. Providers without a listing endpoint set `ModelsUnsupported = true` and report
reachability only.

## CLI surface

Add an operator subcommand under the existing connection command group:

```
arbitron connection validate <name>
```

- Resolves `<name>` → connection → `ResolvedBinding` (reusing the existing resolver /
  ConnectionStore), invokes `ProbeSelector` → `Probe`, and prints a clear result:
  - success + model list (when supported), or
  - success + "model listing not supported for this provider" (reachability only), or
  - a specific failure: invalid credentials (401/403), unreachable endpoint (transport),
    region/config error.
- Exit code reflects success/failure for scripting.
- Never prints the secret.

## Data flow

`arbitron connection validate <name>` → resolve binding → `ProbeSelector.ProbeFor(binding)`
→ per-provider probe → HTTP/CLI call with the connection's auth → `ProbeResult` → formatted
operator output.

## Error handling

- Unknown connection name → clear "no such connection" error (reuse
  `ErrConnectionNotFound`).
- 401/403 → "credentials rejected"; transport error → "endpoint unreachable"; Bedrock
  region/sig error → "region/credential error". All distinguish configuration faults from
  transient faults and never leak the secret.

## Testing (strict TDD, 5 categories)

- **Happy-path:** each OpenAI-compatible probe lists models from an `httptest` server;
  Anthropic probe lists models; Bedrock probe lists foundation models (signed); Claude CLI
  probe reports reachable + ModelsUnsupported.
- **Input-validation:** unknown connection name handled; empty/missing region for Bedrock
  handled.
- **Error-handling:** 401/403 → credentials-rejected; transport failure → unreachable;
  malformed listing body handled.
- **Edge-cases:** provider with no listing endpoint reports ModelsUnsupported (not an
  error); empty model list reported as "reachable, no models".
- **Security:** secret never printed in output/logs; response bounded; control chars
  scrubbed from any upstream error echoed to the operator.

## Out of scope (this phase)

Auto-refresh / caching of model lists, model-name validation at workflow-author time,
exposing the probe as an MCP tool (CLI only per the design decision).
