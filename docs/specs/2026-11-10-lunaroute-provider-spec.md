# LunaRoute Provider — Product Specification

## Purpose

Make LunaRoute a first-class provider for Arbitron workflow authors working through an LLM over MCP. Authors can discover available models, assess their known capabilities, and select LunaRoute for chat or agent work using Claude Code, Codex, OpenCode, or Pi.

The goal is to make LunaRoute usable in real workflows through the existing connection and profile experience, including both open-weight and proprietary models available through the service.

## Features

### Connection setup

Authors can configure a named LunaRoute connection through MCP using a LunaRoute credential. Connections use LunaRoute’s hosted service only and require no separate login or setup utility.

Credentials remain protected from disclosure. Credentials belonging to other providers are rejected rather than forwarded through LunaRoute.

### Model discovery and selection

An LLM operating through MCP can inspect models available through a selected LunaRoute connection. Discovery includes known model family, supported task types, and context/output limits. Unknown information is explicitly marked; availability alone does not imply task or backend compatibility.

The LLM chooses a model and backend explicitly. Both open-weight and proprietary models are allowed. Model-family safeguards reflect the actual model vendor, including aliases. An unresolved family cannot bypass those safeguards.

### Chat and agent workflows

Authors can select LunaRoute models through existing profiles and workflow steps for:

- Chat.
- Agent work through Claude Code.
- Agent work through Codex.
- Agent work through OpenCode.
- Agent work through Pi.

Each agent backend must support compatible LunaRoute models, but every catalog model need not support every backend.

Supporting calls remain within the selected LunaRoute connection and use compatible models. Settings known to conflict with the chosen model’s capabilities are rejected.

### Failure behavior

Authors receive actionable explanations for invalid credentials, unavailable models, incompatible task/backend selections, and service failures. Errors distinguish these conditions when known and identify uncertainty when the cause is unknown.

Incompatible selections stop without silently substituting another model, backend, or provider.

## Outcomes

The release is ready when an LLM operating through MCP can:

- Configure a LunaRoute connection, inspect its available models and known capabilities, and select an appropriate profile.
- Complete a representative chat task through LunaRoute.
- Complete a representative tool-using agent task through each of Claude Code, Codex, OpenCode, and Pi, using a compatible LunaRoute model for each.
- Receive actionable errors for invalid credentials, unavailable models, and known incompatibilities, without credential disclosure or silent substitution.
- Preserve model-family safeguards when selecting proprietary models or aliases.

Successful launches alone do not satisfy readiness; the tasks must finish successfully.

## Non-goals

- Automatic model recommendations, task-based model selection, or fallback to another model or backend.
- Custom service destinations or forwarding credentials belonging to other providers through LunaRoute.
- New workflow step types or changes to existing subscription-based connections.
- System One decision models, embeddings, reranking, images, audio, OCR, document conversion, web search, or LunaRoute’s own MCP service.
- LunaRoute-specific scheduling, automatic priority selection, usage attribution, billing reports, or performance/cost comparisons.
- A dedicated dashboard model-browsing experience; discovery in this release serves the LLM through MCP.
